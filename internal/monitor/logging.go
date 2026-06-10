package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yuu61/deadman/internal/ping"
)

// File permissions for the log directory and the per-target log files.
const (
	logDirPerm  os.FileMode = 0o750
	logFilePerm os.FileMode = 0o600
)

// AppendLog appends one line per probe to <dir>/<target name> in the format
// "<timestamp> <status> <rtt> <avg> <snt>". status is "up"/"down" so a failed probe
// is distinguishable from a real reply (a failure has no RTT, logged as 0 rather
// than the previous success's stale value); rtt/avg are fixed-precision milliseconds
// and snt is the running send count. The timestamp is passed in so the function
// stays testable.
func AppendLog(dir string, t *Target, res ping.Result, now time.Time) error {
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

	status := "down"
	rtt := 0.0 // a failed probe has no RTT; log 0, not the stale last-success RTT.

	if res.Success {
		status = "up"
		rtt = res.RTT
	}

	line := fmt.Sprintf(
		"%s %s %.3f %.3f %d\n",
		now.Format("2006-01-02 15:04:05.000000"),
		status,
		rtt,
		t.Avg,
		t.Snt,
	)
	_, err = f.WriteString(line)

	return err
}
