package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/ping"
)

// LogWriter serializes writes through one goroutine; Close drains the buffer so the
// written lines are observable.
func TestLogWriterWritesQueuedLines(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	w.Log("host", ping.Result{Success: true, Code: ping.Success, RTT: 5}, 5, 1, now)
	w.Log("host", ping.Result{Code: ping.Failed}, 5, 2, now)
	w.Close()

	// #nosec G304 -- test reads back the file it just wrote in t.TempDir().
	b, err := os.ReadFile(filepath.Join(dir, "host"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2:\n%s", len(lines), b)
	}

	if f := strings.Fields(lines[0]); f[2] != "up" || f[3] != "5.000" {
		t.Errorf("first line = %v, want status=up rtt=5.000", f)
	}

	if f := strings.Fields(lines[1]); f[2] != "down" || f[3] != "0.000" {
		t.Errorf("second line = %v, want status=down rtt=0.000", f)
	}
}

// Log must never block, even when the queue overflows (a stalled backing store): the
// excess is dropped rather than growing memory or blocking the Update goroutine. A burst
// far larger than logQueueDepth must return promptly.
func TestLogWriterDoesNotBlockOnOverflow(t *testing.T) {
	dir := t.TempDir()
	w := NewLogWriter(dir)

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

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

	w.Close()
}
