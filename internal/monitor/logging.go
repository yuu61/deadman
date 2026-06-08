package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// File permissions for the log directory and the per-target log files.
const (
	logDirPerm  os.FileMode = 0o750
	logFilePerm os.FileMode = 0o600
)

// AppendLog appends one line per probe to <dir>/<target name>, matching the
// original format: "<timestamp> <rtt> <avg> <snt>". The timestamp is passed in so
// the function stays testable.
func AppendLog(dir string, t *Target, now time.Time) error {
	err := os.MkdirAll(dir, logDirPerm)
	if err != nil {
		return err
	}

	// #nosec G304 -- dir is the operator-supplied log directory and t.Name comes
	// from the trusted local config file, not from any remote input.
	f, err := os.OpenFile(
		filepath.Join(dir, t.Name),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		logFilePerm,
	)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	line := fmt.Sprintf(
		"%s %v %v %d\n",
		now.Format("2006-01-02 15:04:05.000000"),
		t.RTT,
		t.Avg,
		t.Snt,
	)
	_, err = f.WriteString(line)

	return err
}
