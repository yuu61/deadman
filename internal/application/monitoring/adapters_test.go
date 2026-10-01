package monitoring

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func awaitClose(t *testing.T, p *closingPinger) {
	t.Helper()

	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("adapter was not released")
	}

	select {
	case <-p.closed:
		t.Fatal("adapter was released twice")
	default:
	}
}

// reloadingService returns a service whose config starts empty and, once the returned
// source's targets are set, builds p for each target.
func reloadingService(p *closingPinger, load ConfigSource) *Service {
	return NewService(Ports{
		NewPinger:  func(probe.Plan, string) (probe.Pinger, error) { return p, nil },
		LoadConfig: load,
		Host:       capable,
	})
}

// A prepared reload whose result the frontend never delivers (it has exited) is still
// released: its adapters follow the session's lifetime, whether the reload finished
// before the session was closed or only after.
func TestUnreceivedReloadFollowsLifetime(t *testing.T) {
	for _, name := range []string{"queued", "completed_after_exit"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			p := &closingPinger{closed: make(chan struct{}, 2)}
			src := source()
			svc := NewService(Ports{
				NewPinger: func(probe.Plan, string) (probe.Pinger, error) {
					if name == "completed_after_exit" {
						cancel() // The program exits after the reload builds an adapter.
					}

					return p, nil
				},
				LoadConfig: src.load,
				Host:       capable,
			})

			s, _, err := svc.Open(ctx, false)
			if err != nil {
				t.Fatal(err)
			}

			src.cfg.Lines = []config.Line{config.Target{Name: "h", Addr: "192.0.2.1"}}
			// A reload canceled while it builds reports nothing; its adapters are
			// released all the same.
			event, reported := s.Reload()()

			cancel()
			// No Update: the frontend has exited without receiving the result.
			awaitClose(t, p)

			if !reported {
				return
			}

			if out := s.Update(event, time.Now()); out.Reload != nil || len(p.closed) != 0 {
				t.Fatal("a result delivered after exit was applied or released again")
			}
		})
	}
}

func TestPreparedTransferAndCancellationRace(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		p := &closingPinger{closed: make(chan struct{}, 2)}
		src := source()
		svc := reloadingService(p, src.load)

		s, _, err := svc.Open(ctx, false)
		if err != nil {
			t.Fatal(err)
		}

		src.cfg.Lines = []config.Line{config.Target{Name: "h", Addr: "192.0.2.1"}}
		event := mustEvent(t, s.Reload())

		var workers sync.WaitGroup
		workers.Go(cancel)

		s.Update(event, time.Now())

		workers.Wait()
		s.Close()
		awaitClose(t, p)
	}
}
