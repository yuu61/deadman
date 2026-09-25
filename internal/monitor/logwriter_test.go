package monitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/ping"
)

// LogWriter serializes writes through one goroutine; Close drains the buffer so the
// written lines are observable.
func TestLogWriterWritesQueuedLines(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)

	now := testTime
	w.Log("host", ping.Result{Success: true, Code: ping.Success, RTT: 5}, 5, 1, now)
	w.Log("host", ping.Result{Code: ping.Failed}, 5, 2, now)

	err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	lines := readLogLines(t, dir, "host")
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

// Log must never block, even when the queue overflows (a stalled backing store): the
// excess is dropped rather than growing memory or blocking the Update goroutine. A burst
// far larger than logQueueDepth must return promptly.
func TestLogWriterDoesNotBlockOnOverflow(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)

	now := testTime

	done := make(chan struct{})

	go func() {
		for range logQueueDepth * 4 {
			w.Log("h", ping.Result{Success: true, Code: ping.Success, RTT: 1}, 1, 1, now)
		}

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Log blocked under overflow; want non-blocking drop")
	}

	err := w.Close()
	if err != nil {
		t.Fatal(err)
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
	w.Log("host", ping.Result{Success: true, Code: ping.Success, RTT: 5}, 5, 1, now)

	err = w.Close()
	if err == nil {
		t.Fatal("Close() = nil, want the write error to surface")
	}
}
