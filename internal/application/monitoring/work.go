package monitoring

import (
	"context"
	"sync"
	"time"
)

// sessionWork tracks running tasks and resource releases across all generations.
// Admission and shutdown share a lock so Wait never races an Add from an idle session.
type sessionWork struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	closing bool
	done    chan struct{}
}

func (w *sessionWork) begin(ctx context.Context) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closing || ctx.Err() != nil {
		return false
	}

	w.wg.Add(1)

	return true
}

func (w *sessionWork) task(ctx context.Context, task Task) Task {
	return func() (Event, bool) {
		if !w.begin(ctx) {
			return nil, false
		}
		defer w.wg.Done()

		return task()
	}
}

// release registers cleanup during initialization or while counted work is running.
// The reservation outlives the task that created it, even if no event reaches Update.
func (w *sessionWork) release(ctx context.Context, rows []entry) {
	w.wg.Add(1)
	context.AfterFunc(ctx, func() {
		defer w.wg.Done()

		closeAdapters(rows)
	})
}

func (w *sessionWork) prepared(rows *preparedRows) {
	if rows == nil {
		return
	}

	w.wg.Go(func() { <-rows.released })
}

func (w *sessionWork) close(stop context.CancelFunc, timeout time.Duration) {
	w.mu.Lock()
	if !w.closing {
		w.closing = true

		stop()

		go func() {
			w.wg.Wait()
			close(w.done)
		}()
	}
	w.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-w.done:
	case <-timer.C:
	}
}
