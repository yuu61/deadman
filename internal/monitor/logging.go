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

// Per-probe log status words: a failed probe is "down" (no RTT), a reply is "up".
const (
	statusUp   = "up"
	statusDown = "down"
)

// AppendLog appends one probe line for target t. It snapshots the fields it logs and
// delegates to AppendLogLine. Callers that must run the (blocking) disk write off the
// Bubble Tea Update goroutine should snapshot t themselves on the Update goroutine and
// call AppendLogLine directly — t must not be read from another goroutine, since only
// Update may touch target stats.
func AppendLog(dir string, t *Target, res ping.Result, now time.Time) error {
	return AppendLogLine(dir, t.Name, res, t.Avg, t.Snt, now)
}

// AppendLogLine appends one line to <dir>/<name> in the format
// "<timestamp> <status> <rtt> <avg> <snt>", built from already-snapshotted values so it
// is safe to call from a command goroutine. status is "up"/"down" so a failed probe is
// distinguishable from a real reply (a failure has no RTT, logged as 0 rather than the
// previous success's stale value); rtt/avg are fixed-precision milliseconds and snt is
// the running send count. The timestamp is passed in so the function stays testable.
func AppendLogLine(dir, name string, res ping.Result, avg float64, snt int, now time.Time) error {
	err := os.MkdirAll(dir, logDirPerm)
	if err != nil {
		return err
	}

	// #nosec G304 -- dir is the operator-supplied log directory and name comes from
	// the trusted local config file, not from any remote input.
	f, err := os.OpenFile(
		filepath.Join(dir, name),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		logFilePerm,
	)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	status := statusDown
	rtt := 0.0 // a failed probe has no RTT; log 0, not the stale last-success RTT.

	if res.Success {
		status = statusUp
		rtt = res.RTT
	}

	line := fmt.Sprintf(
		"%s %s %.3f %.3f %d\n",
		now.Format("2006-01-02 15:04:05.000000"),
		status,
		rtt,
		avg,
		snt,
	)
	_, err = f.WriteString(line)

	return err
}
