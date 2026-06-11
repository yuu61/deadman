package monitor

import (
	"time"

	"github.com/yuu61/deadman/internal/ping"
)

// logQueueDepth bounds how many pending log lines are buffered before new ones are
// dropped. A few hundred covers a normal burst; the cap is what keeps memory bounded
// when the backing store stalls.
const logQueueDepth = 256

// logEntry is one already-snapshotted log line. It carries values, not a *Target, so the
// writer goroutine never reads live target state (only Update may touch it).
type logEntry struct {
	name string
	res  ping.Result
	avg  float64
	snt  int
	now  time.Time
}

// LogWriter serializes per-probe log writes through a single background goroutine fed by
// a bounded channel. This keeps the Bubble Tea Update loop responsive (Log never blocks)
// AND bounds resources when the log filesystem stalls (NFS hang, full disk): a stalled
// write blocks only the one worker, pending lines fill the fixed buffer, and further
// lines are dropped rather than spawning unbounded goroutines. When the store recovers
// the worker drains and resumes.
type LogWriter struct {
	dir  string
	ch   chan logEntry
	done chan struct{}
}

// NewLogWriter starts a LogWriter writing under dir.
func NewLogWriter(dir string) *LogWriter {
	w := &LogWriter{
		dir:  dir,
		ch:   make(chan logEntry, logQueueDepth),
		done: make(chan struct{}),
	}
	go w.run()

	return w
}

// Log enqueues one line from caller-snapshotted values without blocking. If the buffer
// is full (the writer is stalled on a slow filesystem), the line is dropped to keep the
// caller responsive and memory bounded. Callers must snapshot the values on the Update
// goroutine, since the write happens off it.
func (w *LogWriter) Log(name string, res ping.Result, avg float64, snt int, now time.Time) {
	select {
	case w.ch <- logEntry{name: name, res: res, avg: avg, snt: snt, now: now}:
	default:
		// buffer full: drop this line rather than block or grow unbounded.
	}
}

// Close stops the writer, draining any buffered lines first. It is used for a clean
// shutdown and to make writes observable in tests; the process can also just exit.
func (w *LogWriter) Close() {
	close(w.ch)
	<-w.done
}

func (w *LogWriter) run() {
	defer close(w.done)

	for e := range w.ch {
		_ = AppendLogLine(w.dir, e.name, e.res, e.avg, e.snt, e.now)
	}
}
