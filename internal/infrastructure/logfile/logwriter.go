package logfile

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
)

// logQueueDepth bounds how many pending log lines are buffered before new ones are
// dropped. In async mode a round's results arrive together, one line per row, so the
// queue must hold thousands of rows' burst for a healthy store to write them all; the
// cap (a few MB of readings) is what keeps memory bounded when the backing store stalls.
const logQueueDepth = 1 << 14

// closeDrainTimeout caps how long Close waits for buffered lines to flush. A responsive
// store drains well within it; a stalled one would otherwise hang shutdown, so Close
// gives up after this and lets the process exit.
const closeDrainTimeout = 2 * time.Second

// errLogDrainTimeout reports that Close gave up draining queued lines because the backing
// store stalled past closeDrainTimeout, so some lines were lost. Close returns it instead
// of any write error since a stalled write never returns one.
var errLogDrainTimeout = errors.New(
	"log writer: drain timed out on close; some queued lines were lost",
)

// errLinesDropped reports that Log dropped lines because the queue was full, so the
// logs lack them. Close wraps it with the count.
var errLinesDropped = errors.New("log writer: the log directory could not keep up")

// logEntry is one queued log line: a detached reading, not a *Target, so the writer
// goroutine never reads live target state (only Update may touch it).
type logEntry struct {
	reading monitor.Reading
	now     time.Time
}

// LogWriter serializes per-probe log writes through a single background goroutine fed by
// a bounded channel. This keeps the Bubble Tea Update loop responsive (Log never blocks)
// AND bounds resources when the log filesystem stalls (NFS hang, full disk): a stalled
// write blocks only the one worker, pending lines fill the fixed buffer, and further
// lines are dropped rather than spawning unbounded goroutines. When the store recovers
// the worker drains and resumes.
type LogWriter struct {
	store *logStore // used by the run goroutine only.
	ch    chan logEntry
	done  chan struct{}

	// firstErr and health are shared with the session's nonblocking status reader.
	mu       sync.Mutex
	firstErr error     // the first storage failure, which Close reports.
	health   logHealth // whose writes are failing now, which Err reports.

	dropped atomic.Int64 // lines Log dropped because the queue was full.
}

// NewLogWriter starts a LogWriter writing under dir.
func NewLogWriter(dir string) *LogWriter {
	w := &LogWriter{
		store:  newLogStore(dir),
		ch:     make(chan logEntry, logQueueDepth),
		done:   make(chan struct{}),
		health: newLogHealth(),
	}
	go w.run()

	return w
}

// Log enqueues the line of the reading a probe result left its row at, without blocking:
// the writer goroutine names the file from the row's ID, and fills the line from its
// statistics. If the buffer is full (the writer is stalled on a slow filesystem), the
// line is dropped to keep the caller responsive and memory bounded, and Log reports
// false.
func (w *LogWriter) Log(reading monitor.Reading, now time.Time) bool {
	select {
	case w.ch <- logEntry{reading: reading, now: now}:
		return true
	default:
		w.dropped.Add(1)

		return false
	}
}

// Close stops the writer, draining any buffered lines first so a clean shutdown
// (q/Ctrl-C) does not silently lose queued log entries. It must be called only after the
// last Log (no concurrent send on the closed channel). It returns the first write error
// seen, or errLogDrainTimeout if the backing store stalled past closeDrainTimeout (in
// which case it gives up rather than hang the process exit), joined with how many lines
// Log dropped, if any.
func (w *LogWriter) Close() error {
	close(w.ch)

	var err error

	select {
	case <-w.done:
		err = w.Err()
	case <-time.After(closeDrainTimeout):
		// stalled store: stop waiting so shutdown isn't blocked; remaining lines are lost.
		err = errLogDrainTimeout
	}

	if n := w.dropped.Load(); n > 0 {
		err = errors.Join(err, fmt.Errorf("%w: %d lines were dropped", errLinesDropped, n))
	}

	return err
}

// Err reports the storage failure the writer is in now, without waiting for a filesystem
// operation: the most recent failure while a row's latest write failed, nil once the rows
// that failed have written again. Close still reports the first failure of the run.
func (w *LogWriter) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.health.current()
}

func (w *LogWriter) run() {
	defer close(w.done)
	defer func() { w.rememberError(w.store.close()) }()

	for e := range w.ch {
		err := w.store.write(e.reading, e.now)
		w.rememberError(err)
		w.recordHealth(e.reading.ID, err)
	}
}

func (w *LogWriter) recordHealth(id string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.health.record(id, err)
}

func (w *LogWriter) rememberError(err error) {
	if err == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.firstErr == nil {
		w.firstErr = err
	}
}
