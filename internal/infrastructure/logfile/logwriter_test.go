package logfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// observe folds res into tg and returns what a Session hands its ResultLog: the reading
// the fold left.
func observe(tg *monitor.Target, res probe.Result) monitor.Reading {
	return tg.Consume(res)
}

// LogWriter serializes writes through one goroutine; Close drains the buffer so the
// written lines are observable.
func TestLogWriterWritesQueuedLines(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)

	now := testTime
	tg := monitor.NewTarget("host#1", "host", "192.0.2.1")
	up := observe(tg, probe.Result{Code: probe.Success, RTT: 5})
	down := observe(tg, probe.Result{Code: probe.Failed})

	w.Log(up, now)
	w.Log(down, now)

	err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	lines := readLogLines(t, dir, logFileName("host#1"))
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2", len(lines))
	}

	if f := lines[0]; f[2] != "up" || f[3] != "5.000" {
		t.Errorf("first line = %v, want status=up rtt=5.000", f)
	}

	if f := lines[1]; f[2] != "down" || f[3] != "0.000" {
		t.Errorf("second line = %v, want status=down rtt=0.000", f)
	}
}

func TestLogWriterSeparatesTargetsWithTheSameName(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)
	first := monitor.NewTarget(`"direct":"192.0.2.1":"":"":#1`, "web", "192.0.2.1")
	second := monitor.NewTarget(`"direct":"192.0.2.2":"":"":#1`, "web", "192.0.2.2")
	up := observe(first, probe.SuccessResult(5))
	down := observe(second, probe.FailedResult())

	w.Log(up, testTime)
	w.Log(down, testTime)

	err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	firstFile := logFileName(up.ID)
	secondFile := logFileName(down.ID)

	if firstFile == secondFile {
		t.Fatal("distinct target identities resolved to the same log file")
	}

	got := readLogLines(t, dir, firstFile)
	if len(got) != 1 || got[0][2] != "up" {
		t.Fatalf("first target log = %v", got)
	}

	got = readLogLines(t, dir, secondFile)
	if len(got) != 1 || got[0][2] != "down" {
		t.Fatalf("second target log = %v", got)
	}
}

// Log must never block, even when the queue overflows (a stalled backing store): the
// excess is dropped rather than growing memory or blocking the Update goroutine, and Log
// says so. A burst far larger than logQueueDepth must return promptly.
func TestLogWriterDoesNotBlockOnOverflow(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)

	t.Cleanup(func() {
		// Close can time out with a large backlog. Wait for the worker before
		// TempDir cleanup removes files it may still be writing.
		select {
		case <-w.done:
		case <-time.After(10 * time.Second):
			t.Error("log writer did not finish after Close")
		}
	})

	now := testTime
	view := observe(monitor.NewTarget("h#1", "h", "192.0.2.1"), probe.SuccessResult(1))

	done := make(chan int)

	go func() {
		dropped := 0

		for range logQueueDepth * 4 {
			if !w.Log(view, now) {
				dropped++
			}
		}

		done <- dropped
	}()

	var dropped int

	select {
	case dropped = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Log blocked under overflow; want non-blocking drop")
	}

	err := w.Close()
	if dropped == 0 && err != nil {
		t.Fatal(err)
	}

	if dropped > 0 && !errors.Is(err, errLinesDropped) {
		t.Fatalf("Log dropped %d lines, but Close() = %v", dropped, err)
	}
}

// A burst of one line per row, as an async round of thousands of rows delivers, fits the
// queue of a healthy store: every line is written.
func TestLogWriterKeepsARoundOfThousandsOfRows(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)

	const rows = 4000

	for i := range rows {
		tg := monitor.NewTarget(fmt.Sprintf("h#%d", i), "h", "192.0.2.1")
		if !w.Log(observe(tg, probe.SuccessResult(1)), testTime) {
			t.Fatalf("line %d of a %d-row round was dropped", i, rows)
		}
	}

	// Let the backlog drain before Close so its shutdown deadline does not
	// impose a filesystem throughput requirement on this queue-capacity test.
	waitForLogQueue(t, w)

	err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != rows+1 {
		t.Fatalf("got %d files, want %d result logs and an index", len(files), rows)
	}
}

func waitForLogQueue(t *testing.T, w *LogWriter) {
	t.Helper()

	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()

	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()

	for len(w.ch) > 0 {
		select {
		case <-poll.C:
		case <-timeout.C:
			t.Fatal("log queue did not drain")
		}
	}
}

// A write that keeps failing (here: an un-creatable log dir, since its parent is a
// regular file) must not be silently swallowed — Close surfaces the first error so the
// caller can report it after the TUI exits.
func TestLogWriterCloseReportsWriteError(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")

	err := os.WriteFile(parent, []byte("x"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	// MkdirAll(parent/logs) fails because parent is a file, so every queued line errors.
	w := NewLogWriter(filepath.Join(parent, "logs"))
	now := testTime
	view := observe(
		monitor.NewTarget("host#1", "host", "192.0.2.1"),
		probe.SuccessResult(5),
	)
	w.Log(view, now)

	err = w.Close()
	if err == nil {
		t.Fatal("Close() = nil, want the write error to surface")
	}
}
