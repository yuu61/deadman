package logfile

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// RLIMIT_FSIZE affects the whole process. Isolate the forced short write so unrelated
// tests (and the race detector's output) retain their normal file-size limit.
func TestShortWriteDoesNotCorruptOrReplayRecords(t *testing.T) {
	const childEnv = "DEADMAN_TEST_SHORT_WRITE"
	if os.Getenv(childEnv) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}

		cmd := exec.CommandContext(
			t.Context(),
			executable,
			"-test.run=^TestShortWriteDoesNotCorruptOrReplayRecords$",
		)

		cmd.Env = append(os.Environ(), childEnv+"=1")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated short write: %v\n%s", err, out)
		}

		return
	}

	dir := t.TempDir()
	store := newLogStore(dir)

	t.Cleanup(func() { closeStore(t, store) })

	target := monitor.NewTarget("row", "web", "192.0.2.1")

	first := target.Consume(probe.SuccessResult(1))

	err := store.write(first, testTime)
	if err != nil {
		t.Fatal(err)
	}

	var original unix.Rlimit

	err = unix.Getrlimit(unix.RLIMIT_FSIZE, &original)
	if err != nil {
		t.Fatal(err)
	}

	limit := original

	limit.Cur = uint64(len(logLine(first.Stats, testTime)) + len(logLine(first.Stats, testTime))/2)

	err = unix.Setrlimit(unix.RLIMIT_FSIZE, &limit)
	if err != nil {
		t.Fatal(err)
	}

	writeErr := store.write(target.Consume(probe.SuccessResult(2)), testTime)

	err = unix.Setrlimit(unix.RLIMIT_FSIZE, &original)
	if err != nil {
		t.Fatal(err)
	}

	if writeErr == nil {
		t.Fatal("forced partial write did not fail")
	}

	err = store.write(target.Consume(probe.SuccessResult(3)), testTime)
	if err != nil {
		t.Fatal(err)
	}

	lines := readLogLines(t, dir, logFileName("row"))
	if len(lines) != 2 || len(lines[0]) != 6 || len(lines[1]) != 6 || lines[0][5] != "1" ||
		lines[1][5] != "3" {
		t.Fatalf("partial or replayed log record: %v", lines)
	}
}
