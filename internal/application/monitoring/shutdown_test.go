package monitoring

import (
	"context"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Like SNMP Send, this probe keeps cleaning up after its context is canceled.
type cleanupPinger struct {
	started  chan struct{}
	cleaning chan struct{}
	release  chan struct{}
	cleaned  chan struct{}
}

func newCleanupPinger(t *testing.T) *cleanupPinger {
	t.Helper()

	p := &cleanupPinger{
		started: make(chan struct{}), cleaning: make(chan struct{}),
		release: make(chan struct{}), cleaned: make(chan struct{}),
	}

	t.Cleanup(func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
	})

	return p
}

func (p *cleanupPinger) Send(ctx context.Context) probe.Result {
	defer func() {
		close(p.cleaning)
		<-p.release
		close(p.cleaned)
	}()

	close(p.started)
	<-ctx.Done()

	return probe.UnavailableResult()
}

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("shutdown signal did not arrive")
	}
}

func assertStillClosing(t *testing.T, closed <-chan struct{}) {
	t.Helper()

	select {
	case <-closed:
		t.Fatal("Close returned before cleanup finished")
	case <-time.After(20 * time.Millisecond):
	}
}

func closeAsync(s *Session) <-chan struct{} {
	closed := make(chan struct{})

	go func() {
		s.Close()
		close(closed)
	}()

	return closed
}

func TestSessionCloseWaitsForProbeCleanupAcrossGenerations(t *testing.T) {
	for _, retired := range []bool{false, true} {
		name := "current"
		if retired {
			name = "retired"
		}

		t.Run(name, func(t *testing.T) {
			p := newCleanupPinger(t)
			s, _ := testSession(t, p, true)
			round := s.Update(mustEvent(t, s.Start()), time.Now())
			reported := runAsync(round.Tasks[0])

			awaitSignal(t, p.started)

			if retired {
				reload(s)
			}

			closed, alsoClosed := closeAsync(s), closeAsync(s)

			awaitSignal(t, p.cleaning)
			assertStillClosing(t, closed)
			assertStillClosing(t, alsoClosed)
			close(p.release)
			awaitSignal(t, closed)
			awaitSignal(t, alsoClosed)
			awaitSignal(t, p.cleaned)

			if receiveReported(t, reported) {
				t.Fatal("canceled probe reported an event")
			}

			// A command handed to the frontend may never run before it quits.
			if event, ok := round.Tasks[1](); ok {
				t.Fatalf("unstarted task ran after shutdown: %T", event)
			}
		})
	}
}

func TestSessionCloseBoundsUnresponsiveCleanup(t *testing.T) {
	p := newCleanupPinger(t)
	s, _ := testSession(t, p, true)
	round := s.Update(mustEvent(t, s.Start()), time.Now())
	reported := runAsync(round.Tasks[0])

	awaitSignal(t, p.started)

	closed := make(chan struct{})

	go func() {
		s.work.close(s.stop, 20*time.Millisecond)
		close(closed)
	}()

	awaitSignal(t, closed)

	select {
	case <-p.cleaned:
		t.Fatal("cleanup finished without being released")
	default:
	}

	close(p.release)

	if receiveReported(t, reported) {
		t.Fatal("canceled probe reported an event")
	}

	s.Close()
}

type cleanupAdapter struct {
	*cleanupPinger
}

func (p *cleanupAdapter) Close() {
	close(p.cleaning)
	<-p.release
	close(p.cleaned)
}

func (*cleanupAdapter) Send(context.Context) probe.Result { return probe.UnavailableResult() }

func TestSessionCloseWaitsForAdapterCleanup(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "generation"
		if pending {
			name = "undelivered reload"
		}

		t.Run(name, func(t *testing.T) {
			p := &cleanupAdapter{cleanupPinger: newCleanupPinger(t)}

			var current probe.Pinger = p
			if pending {
				current = pingerFunc(
					func(context.Context) probe.Result { return probe.UnavailableResult() },
				)
			}

			svc := NewService(Ports{
				NewPinger:  func(probe.Plan, string) (probe.Pinger, error) { return current, nil },
				LoadConfig: source(config.Target{Name: "h", Addr: "192.0.2.1"}).load,
				Host:       capable,
			})
			s, _ := openSession(t, svc, true)

			if pending {
				current = p

				mustEvent(t, s.Reload())
			}

			closed := closeAsync(s)

			awaitSignal(t, p.cleaning)
			assertStillClosing(t, closed)
			close(p.release)
			awaitSignal(t, closed)
			awaitSignal(t, p.cleaned)
		})
	}
}

func TestSessionCloseWaitsForReloadBuildingAdapters(t *testing.T) {
	p := &cleanupAdapter{cleanupPinger: newCleanupPinger(t)}
	building, finishBuild := make(chan struct{}), make(chan struct{})
	reloading := false
	svc := NewService(Ports{
		NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
			if reloading {
				close(building)
				<-finishBuild

				return p, nil
			}

			return pingerFunc(
				func(context.Context) probe.Result { return probe.UnavailableResult() },
			), nil
		},
		LoadConfig: source(config.Target{Name: "h", Addr: "192.0.2.1"}).load,
		Host:       capable,
	})
	s, _ := openSession(t, svc, true)
	t.Cleanup(func() {
		select {
		case <-finishBuild:
		default:
			close(finishBuild)
		}
	})

	reloading = true
	reported := runAsync(s.Reload())

	awaitSignal(t, building)

	closed := closeAsync(s)
	assertStillClosing(t, closed)
	close(finishBuild)
	awaitSignal(t, p.cleaning)
	assertStillClosing(t, closed)
	close(p.release)
	awaitSignal(t, closed)

	if receiveReported(t, reported) {
		t.Fatal("canceled reload reported an event")
	}
}
