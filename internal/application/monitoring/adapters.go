package monitoring

import (
	"context"
	"sync"
)

// The lifetime of the adapters the rows hold. An adapter holding resources is released
// exactly once: when its generation ends, or when the reload that built it is discarded
// before the session takes its rows.

// pingerCloser is an optional lifecycle port for adapters retaining resources
// between probes. Close must be idempotent and safe alongside a canceled Send and release
// idle resources. An adapter is closed once it is no longer needed: by its generation's
// cancellation when it was monitored, or when its reload is discarded when it never was.
type pingerCloser interface{ Close() }

// closeAdapters closes every row's adapter that holds resources.
func closeAdapters(rows []entry) {
	for _, row := range rows {
		if closer, ok := row.pinger.(pingerCloser); ok {
			closer.Close()
		}
	}
}

// preparedRows owns the adapters of a reread config until the session takes them. Its
// lifetime follows the reload's context even when the frontend never delivers the result
// back (a context already ended releases them at once), and take/discard can happen only
// once, whichever comes first. stop is the context's AfterFunc stop: it reports false
// once the context has ended and discard has started, which is how take tells the rows
// are no longer its to take.
type preparedRows struct {
	mu       sync.Mutex
	rows     []entry
	stop     func() bool
	done     bool
	released chan struct{} // closed after transfer or after adapter cleanup completes.
}

func prepareRows(ctx context.Context, rows []entry) *preparedRows {
	p := &preparedRows{rows: rows, released: make(chan struct{})}
	p.mu.Lock()
	p.stop = context.AfterFunc(ctx, p.discard)
	p.mu.Unlock()

	return p
}

// discard releases the adapters unless take already transferred them or another discard
// is releasing them. It is safe on a nil receiver (a reload whose read failed).
func (p *preparedRows) discard() {
	if p == nil {
		return
	}

	p.mu.Lock()
	if p.done {
		p.mu.Unlock()

		return
	}

	p.done = true
	p.stop()
	rows := p.rows
	p.rows = nil
	p.mu.Unlock()
	closeAdapters(rows)
	close(p.released)
}

// take transfers the rows to the caller, unless they were discarded or their context
// has ended.
func (p *preparedRows) take() ([]entry, bool) {
	if p == nil {
		return nil, false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// A false stop means the context has ended and discard is already releasing the rows.
	if p.done || !p.stop() {
		return nil, false
	}

	p.done = true
	rows := p.rows
	p.rows = nil
	close(p.released)

	return rows, true
}
