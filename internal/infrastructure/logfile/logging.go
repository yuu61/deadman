// Package logfile writes the per-probe log files of the -l option: one line per probe
// result, appended to a file named from each monitored row's identity, and an index of
// which row each file is for. LogWriter moves writes off the TUI's Update loop onto a
// single goroutine with a bounded queue.
package logfile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/yuu61/deadman/internal/domain/monitor"
)

// File permissions for the log directory and the per-target log files.
const (
	logDirPerm  os.FileMode = 0o750
	logFilePerm os.FileMode = 0o600
)

// indexFile lists which row each log file is for: a line "<file>\t<name>\t<address>" is
// appended the first time a run logs the row, and again whenever its name or address
// changes, so the last line of a file is its row as it stands. A log file's name comes
// from the row's identity alone, which neither shows.
const indexFile = "targets.tsv"

// statusWords are the log's words for the target states: an unavailable probe leaves the
// state unknown.
var statusWords = [...]string{
	monitor.Unknown: "unknown",
	monitor.Up:      "up",
	monitor.Down:    "down",
}

// logFileName uses only the stable row identity, so a display-name or credential
// change keeps writing to the same file. The raw identity is never a path component.
func logFileName(id string) string {
	sum := sha256.Sum256([]byte(id))

	return fmt.Sprintf("target-%x.log", sum[:12])
}

// logLine is the line a probe result appends to its row's log, in the format
// "<timestamp> <status> <rtt> <avg> <snt>", from the statistics the result left: status
// is the target's state, "up", "down", or "unknown" after an unavailable probe; rtt is
// the latest probe's, 0 without a reply (monitor.Target never keeps a stale one); rtt/avg
// are fixed-precision milliseconds and snt is the running send count.
func logLine(st monitor.Stats, now time.Time) string {
	return fmt.Sprintf(
		"%s %s %.3f %.3f %d\n",
		now.Format("2006-01-02 15:04:05.000000"),
		statusWords[st.State],
		st.RTT,
		st.Avg,
		st.Snt,
	)
}

// listing is what the index last said of a row.
type listing struct{ name, addr string }

// logStore is the log directory as the writer goroutine holds it: open, with each row's
// file name worked out once and what the index says of each row. Only that goroutine
// uses it. Files are opened inside the directory, so no name can write outside it. The
// directory is checked against the configured path before each write, and reopened
// when removed or replaced. Index listings belong to that open directory only.
type logStore struct {
	dir    string
	root   *os.Root
	lock   *logLock
	files  map[string]string  // row ID -> its log file's name.
	listed map[string]listing // row ID -> what the index says of it.
	ends   map[string]int64   // last complete file lengths, checked under the file lock.
}

func newLogStore(dir string) *logStore {
	return &logStore{
		dir:    dir,
		files:  make(map[string]string),
		listed: make(map[string]listing),
		ends:   make(map[string]int64),
	}
}

// write appends the line of reading, arrived at now, to its row's log, listing the row in
// the index first when the index does not say it yet.
func (s *logStore) write(reading monitor.Reading, now time.Time) error {
	file, ok := s.files[reading.ID]
	if !ok {
		file = logFileName(reading.ID)
		s.files[reading.ID] = file
	}

	err := s.writeOnce(file, reading, now)
	if err == nil {
		return nil
	}

	written, attempted := errors.AsType[*appendError](err)
	if attempted {
		return written // A write already started: replay could duplicate a committed line.
	}

	// A directory can disappear during a write. Retry the whole reading so the
	// replacement gets its index entry as well as its result log.
	closeErr := s.close()

	return errors.Join(closeErr, s.writeOnce(file, reading, now))
}

func (s *logStore) writeOnce(file string, reading monitor.Reading, now time.Time) error {
	err := s.open()
	if err != nil {
		return err
	}

	err = s.list(file, reading)
	if err != nil {
		return err
	}

	return s.append(file, logLine(reading.Stats, now))
}

// list appends the row's index line unless the index already says it.
func (s *logStore) list(file string, reading monitor.Reading) error {
	l := listing{name: reading.Name, addr: reading.Addr}
	if said, ok := s.listed[reading.ID]; ok && said == l {
		return nil
	}

	err := s.append(indexFile, file+"\t"+indexField(l.name)+"\t"+indexField(l.addr)+"\n")
	if err != nil {
		return err
	}

	s.listed[reading.ID] = l

	return nil
}

// indexField keeps a name or address to one field of one index line.
func indexField(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}

		return r
	}, s)
}

// open retains the directory only while the configured path still refers to it.
// An open Root follows a renamed directory, so successful appends alone cannot tell
// whether rotation has replaced it.
func (s *logStore) open() error {
	if s.root != nil {
		current, err := os.Stat(s.dir)

		held, rootErr := s.root.Stat(".")
		if err == nil && rootErr == nil && os.SameFile(current, held) {
			return nil
		}

		closeErr := s.close()
		if closeErr != nil {
			return closeErr
		}
	}

	err := os.MkdirAll(s.dir, logDirPerm)
	if err != nil {
		return err
	}

	root, err := openLogRoot(s.dir)
	if err != nil {
		return err
	}

	lock, err := newLogLock(root)
	if err != nil {
		return errors.Join(err, root.Close())
	}

	s.root, s.lock = root, lock

	return nil
}

// openLogRoot retains a directory without blocking Windows removal or rotation.
// On Windows os.OpenRoot uses a handle without FILE_SHARE_DELETE, while Root.OpenRoot
// permits deletion sharing. Reopen the same directory through the root and close the
// initial handle before returning the shareable one.
func openLogRoot(dir string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}

	if runtime.GOOS != "windows" {
		return root, nil
	}

	defer root.Close()

	return root.OpenRoot(".")
}

// append appends text to a file inside the open directory.
func (s *logStore) append(name, text string) error {
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_WRONLY, logFilePerm)
	if err != nil {
		return err
	}

	unlock, err := s.lock.acquire(f)
	if err != nil {
		return errors.Join(err, f.Close())
	}

	end, err := s.appendRecord(f, name, text)

	err = errors.Join(err, unlock(), f.Close())
	if err != nil {
		delete(s.ends, name)

		return &appendError{err: err}
	}

	s.ends[name] = end

	return nil
}

// appendError distinguishes an attempted write from an open/directory failure.
type appendError struct{ err error }

func (e *appendError) Error() string { return e.err.Error() }
func (e *appendError) Unwrap() error { return e.err }

func (s *logStore) appendRecord(file *os.File, name, text string) (int64, error) {
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}

	cleanEnd, known := s.ends[name]
	if !known || size != cleanEnd {
		size, err = s.repairTail(file, name, size)
		if err != nil {
			return 0, err
		}
	}

	n, err := file.WriteString(text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}

	if err != nil {
		return 0, &appendError{err: errors.Join(err, file.Truncate(size))}
	}

	return size + int64(n), nil
}

// repairTail only opens a read handle when a file is new to this store or changed.
// Normal appends keep write-only access, avoiding unnecessary read permissions and I/O.
func (s *logStore) repairTail(file *os.File, name string, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}

	reader, err := s.root.Open(name)
	if err != nil {
		return 0, err
	}

	complete, readErr := completeTail(reader, size)

	err = errors.Join(readErr, reader.Close())
	if err != nil {
		return 0, err
	}

	return trimTail(file, size, complete)
}

// completeTail finds the end of the last complete record after an interrupted append.
// The caller holds the writer lock while reading, repairing and appending.
func completeTail(file *os.File, size int64) (int64, error) {
	const tailBufferSize = 4096

	buffer := make([]byte, tailBufferSize)
	for end := size; end > 0; {
		start := max(end-int64(len(buffer)), 0)

		n, readErr := file.ReadAt(buffer[:end-start], start)
		if readErr != nil {
			return 0, readErr
		}

		if index := bytes.LastIndexByte(buffer[:n], '\n'); index >= 0 {
			return start + int64(index) + 1, nil
		}

		end = start
	}

	return 0, nil
}

func trimTail(file *os.File, size, complete int64) (int64, error) {
	if size == complete {
		return complete, nil
	}

	err := file.Truncate(complete)
	if err != nil {
		return 0, err
	}

	return file.Seek(complete, io.SeekStart)
}

// close lets go of the open directory and forgets what its index contained.
func (s *logStore) close() error {
	var err error
	if s.lock != nil {
		err = s.lock.close()
		s.lock = nil
	}

	if s.root != nil {
		err = errors.Join(err, s.root.Close())
		s.root = nil
	}

	clear(s.listed)
	clear(s.ends)

	return err
}
