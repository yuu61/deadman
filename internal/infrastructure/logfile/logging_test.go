package logfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// A failed probe must be distinguishable in the log from a successful one: it is
// marked "down" and carries no RTT (0.000), not the previous success's stale value.
func TestLogDistinguishesUpDown(t *testing.T) {
	dir := t.TempDir()
	store := newLogStore(dir)

	t.Cleanup(func() { closeStore(t, store) })

	tg := monitor.NewTarget("host#1", "host", "192.0.2.1")

	for _, res := range []probe.Result{probe.SuccessResult(5), probe.FailedResult()} {
		err := store.write(tg.Consume(res), testTime)
		if err != nil {
			t.Fatal(err)
		}
	}

	lines := readLogLines(t, dir, logFileName("host#1"))
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2", len(lines))
	}

	// Fields: <date> <time> <status> <rtt> <avg> <snt>.
	if up := lines[0]; len(up) != 6 || up[2] != "up" || up[3] != "5.000" {
		t.Errorf("up line fields = %v, want status=up rtt=5.000", up)
	}

	if down := lines[1]; len(down) != 6 || down[2] != "down" || down[3] != "0.000" {
		t.Errorf("down line fields = %v, want status=down rtt=0.000 (not stale 5.000)", down)
	}
}

func TestLogMarksUnavailableWithoutClaimingDown(t *testing.T) {
	dir := t.TempDir()
	store := newLogStore(dir)

	t.Cleanup(func() { closeStore(t, store) })

	target := monitor.NewTarget("host#1", "host", "192.0.2.1")
	target.Consume(probe.SuccessResult(5))

	err := store.write(target.Consume(probe.Result{Code: probe.RelayFailed}), testTime)
	if err != nil {
		t.Fatal(err)
	}

	fields := readLogLines(t, dir, logFileName("host#1"))
	if len(fields) != 1 || fields[0][2] != "unknown" || fields[0][3] != "0.000" ||
		fields[0][5] != "1" {
		t.Fatalf("unavailable probe log = %v", fields)
	}
}

// The index says which row each log file is for, since the file's name shows neither:
// once per row, and again when the row's name or address changes, so the last line of a
// file names its row as it stands. A tab in a name cannot split its line.
func TestIndexNamesEachFilesRow(t *testing.T) {
	dir := t.TempDir()
	store := newLogStore(dir)

	t.Cleanup(func() { closeStore(t, store) })

	web := monitor.NewTarget("web#1", "web", "192.0.2.1")
	renamed := monitor.NewTarget("web#1", "web\tfront", "192.0.2.1")
	db := monitor.NewTarget("db#1", "db", "192.0.2.2")

	for _, r := range []monitor.Reading{
		web.Consume(probe.SuccessResult(1)),
		web.Consume(probe.SuccessResult(1)),
		db.Consume(probe.FailedResult()),
		renamed.Consume(probe.SuccessResult(1)),
	} {
		err := store.write(r, testTime)
		if err != nil {
			t.Fatal(err)
		}
	}

	b, err := fs.ReadFile(os.DirFS(dir), indexFile)
	if err != nil {
		t.Fatal(err)
	}

	// Fixed names preserve the on-disk contract across changes to logFileName.
	want := strings.Join([]string{
		"target-d609cd3d7f0ee09e96276ebf.log\tweb\t192.0.2.1",
		"target-002b85f86e07e0e7ba752f57.log\tdb\t192.0.2.2",
		"target-d609cd3d7f0ee09e96276ebf.log\tweb front\t192.0.2.1",
	}, "\n") + "\n"
	if string(b) != want {
		t.Errorf("index =\n%s\nwant\n%s", b, want)
	}
}

// A log directory removed while deadman runs is created again at the next write, as it
// was when each write opened the directory afresh.
func TestLogRecreatesARemovedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	store := newLogStore(dir)
	defer closeStore(t, store)

	tg := monitor.NewTarget("host#1", "host", "192.0.2.1")

	err := store.write(tg.Consume(probe.SuccessResult(1)), testTime)
	if err != nil {
		t.Fatal(err)
	}

	err = os.RemoveAll(dir)
	if err != nil {
		t.Fatal(err)
	}

	err = store.write(tg.Consume(probe.SuccessResult(2)), testTime)
	if err != nil {
		t.Fatal(err)
	}

	lines := readLogLines(t, dir, logFileName("host#1"))
	if len(lines) != 1 || lines[0][3] != "2.000" {
		t.Fatalf("log after the directory was removed = %v", lines)
	}

	assertIndex(t, dir, logFileName("host#1")+"\thost\t192.0.2.1\n")
}

// Failed index or result writes must still permit replacing the directory,
// so the store can be repaired while the writer is still running.
func TestLogRecoversAfterWriteError(t *testing.T) {
	for _, file := range []string{indexFile, logFileName("host#1")} {
		t.Run(file, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "logs")
			store := newLogStore(dir)

			t.Cleanup(func() { closeStore(t, store) })

			target := monitor.NewTarget("host#1", "host", "192.0.2.1")

			// A directory at the file's path prevents opening it for append.
			err := os.MkdirAll(filepath.Join(dir, file), logDirPerm)
			if err != nil {
				t.Fatal(err)
			}

			err = store.write(target.Consume(probe.SuccessResult(1)), testTime)
			if err == nil {
				t.Fatal("write to a directory succeeded, want an error")
			}

			err = os.RemoveAll(dir)
			if err != nil {
				t.Fatal(err)
			}

			err = store.write(target.Consume(probe.SuccessResult(2)), testTime)
			if err != nil {
				t.Fatal(err)
			}

			lines := readLogLines(t, dir, logFileName("host#1"))
			if len(lines) != 1 || lines[0][3] != "2.000" {
				t.Fatalf("log after repairing the directory = %v", lines)
			}

			assertIndex(t, dir, logFileName("host#1")+"\thost\t192.0.2.1\n")
		})
	}
}

// Rotation must move subsequent results and their index to the configured path,
// including when the replacement directory is created by the writer itself.
func TestLogFollowsReplacedDirectory(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		t.Run(fmt.Sprintf("recreate=%t", recreate), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "logs")
			old := dir + ".old"
			store := newLogStore(dir)

			t.Cleanup(func() { closeStore(t, store) })

			web := monitor.NewTarget("web#1", "web", "192.0.2.1")
			db := monitor.NewTarget("db#1", "db", "192.0.2.2")

			targets := []*monitor.Target{web, db}
			for _, target := range targets {
				err := store.write(target.Consume(probe.SuccessResult(1)), testTime)
				if err != nil {
					t.Fatal(err)
				}
			}

			err := os.Rename(dir, old)
			if err != nil {
				t.Fatal(err)
			}

			if recreate {
				err = os.Mkdir(dir, logDirPerm)
				if err != nil {
					t.Fatal(err)
				}
			}

			for _, target := range targets {
				for range 2 {
					err = store.write(target.Consume(probe.SuccessResult(2)), testTime)
					if err != nil {
						t.Fatal(err)
					}
				}
			}

			for _, id := range []string{"web#1", "db#1"} {
				file := logFileName(id)

				lines := readLogLines(t, dir, file)
				if len(lines) != 2 || lines[0][3] != "2.000" || lines[1][3] != "2.000" {
					t.Errorf("%s: new directory log = %v", id, lines)
				}

				lines = readLogLines(t, old, file)
				if len(lines) != 1 || lines[0][3] != "1.000" {
					t.Errorf("%s: rotated directory log = %v", id, lines)
				}
			}

			index := logFileName("web#1") + "\tweb\t192.0.2.1\n" +
				logFileName("db#1") + "\tdb\t192.0.2.2\n"
			assertIndex(t, dir, index)
			assertIndex(t, old, index)
		})
	}
}

func assertIndex(t *testing.T, dir, want string) {
	t.Helper()

	b, err := fs.ReadFile(os.DirFS(dir), indexFile)
	if err != nil {
		t.Fatal(err)
	}

	if string(b) != want {
		t.Errorf("index =\n%s\nwant\n%s", b, want)
	}
}

func TestLogRepairsInterruptedTailAfterRestart(t *testing.T) {
	// The tail is read back in 4096-byte blocks from the end. A 4095-byte fragment puts
	// the last complete record's newline at the first byte of the first block read, and
	// an 8192-byte one needs blocks with no newline at all.
	for _, tail := range []string{"partial", strings.Repeat("x", 4095), strings.Repeat("x", 8192)} {
		dir := t.TempDir()
		target := monitor.NewTarget("host#1", "host", "192.0.2.1")
		first := target.Consume(probe.SuccessResult(1))

		filename := filepath.Join(dir, logFileName(first.ID))

		err := os.WriteFile(
			filename,
			[]byte(logLine(first.Stats, testTime)+tail),
			logFilePerm,
		)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(
			filepath.Join(dir, indexFile),
			[]byte("incomplete index"),
			logFilePerm,
		)
		if err != nil {
			t.Fatal(err)
		}

		store := newLogStore(dir)

		t.Cleanup(func() { closeStore(t, store) })

		err = store.write(target.Consume(probe.SuccessResult(2)), testTime)
		if err != nil {
			t.Fatal(err)
		}

		lines := readLogLines(t, dir, logFileName(first.ID))
		if len(lines) != 2 || len(lines[1]) != 6 || lines[1][5] != "2" {
			t.Fatalf("unrepaired log: %v", lines)
		}

		assertIndex(t, dir, logFileName(first.ID)+"\thost\t192.0.2.1\n")
	}
}

// Another writer of the same row (a second instance that died mid-append) can leave a
// fragment in a file this store has already appended to. The store's remembered end then
// no longer matches the file, so it repairs the tail rather than appending to the fragment.
func TestLogRepairsTailLeftAfterItsOwnAppend(t *testing.T) {
	dir := t.TempDir()
	store := newLogStore(dir)

	t.Cleanup(func() { closeStore(t, store) })

	target := monitor.NewTarget("host#1", "host", "192.0.2.1")

	first := target.Consume(probe.SuccessResult(1))

	err := store.write(first, testTime)
	if err != nil {
		t.Fatal(err)
	}

	name := logFileName(first.ID)

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	other, err := root.OpenFile(name, os.O_APPEND|os.O_WRONLY, logFilePerm)
	if err != nil {
		t.Fatal(err)
	}

	_, err = other.WriteString("partial")
	if err != nil {
		t.Fatal(err)
	}

	err = other.Close()
	if err != nil {
		t.Fatal(err)
	}

	second := target.Consume(probe.SuccessResult(2))

	err = store.write(second, testTime)
	if err != nil {
		t.Fatal(err)
	}

	got, err := fs.ReadFile(root.FS(), name)
	if err != nil {
		t.Fatal(err)
	}

	// The whole file, not the field count: a fragment glued to a record keeps its fields.
	want := logLine(first.Stats, testTime) + logLine(second.Stats, testTime)
	if string(got) != want {
		t.Fatalf("log =\n%q\nwant\n%q", got, want)
	}
}

func TestWriterReportsStorageErrorWhileRunning(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")

	err := os.WriteFile(parent, []byte("x"), logFilePerm)
	if err != nil {
		t.Fatal(err)
	}

	writer := NewLogWriter(filepath.Join(parent, "logs"))

	target := monitor.NewTarget("row", "web", "192.0.2.1")
	if !writer.Log(target.Consume(probe.SuccessResult(1)), testTime) {
		t.Fatal("queue unexpectedly full")
	}

	deadline := time.Now().Add(time.Second)
	for writer.Err() == nil && time.Now().Before(deadline) {
		<-time.After(time.Millisecond)
	}

	observed := writer.Err()

	closed := writer.Close()
	if observed == nil || closed == nil || !errors.Is(closed, observed) {
		t.Fatalf("storage error unavailable before Close: observed=%v close=%v", observed, closed)
	}
}

// Independent stores (separate deadman instances) can append the same row. Their cached
// complete lengths must be rechecked under the file lock when another writer advances.
func TestConcurrentStoresKeepWholeRecords(t *testing.T) {
	dir := t.TempDir()
	results := make(chan error, 2)

	const records = 50

	for range 2 {
		store := newLogStore(dir)
		go func() {
			defer closeStore(t, store)

			target := monitor.NewTarget("row", "web", "192.0.2.1")
			for range records {
				err := store.write(target.Consume(probe.SuccessResult(1)), testTime)
				if err != nil {
					results <- err

					return
				}
			}

			results <- nil
		}()
	}

	for range 2 {
		err := <-results
		if err != nil {
			t.Fatal(err)
		}
	}

	lines := readLogLines(t, dir, logFileName("row"))
	if len(lines) != records*2 {
		t.Fatalf("lost concurrent records: %d", len(lines))
	}

	for _, line := range lines {
		if len(line) != 6 {
			t.Fatalf("interleaved record: %v", line)
		}
	}
}

func closeStore(t *testing.T, store *logStore) {
	t.Helper()

	err := store.close()
	if err != nil {
		t.Error(err)
	}
}
