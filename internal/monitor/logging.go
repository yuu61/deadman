package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AppendLog appends one line per probe to <dir>/<target name>, matching the
// original format: "<timestamp> <rtt> <avg> <snt>". The timestamp is passed in so
// the function stays testable.
func AppendLog(dir string, t *Target, now time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, t.Name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	line := fmt.Sprintf("%s %v %v %d\n", now.Format("2006-01-02 15:04:05.000000"), t.RTT, t.Avg, t.Snt)
	_, err = f.WriteString(line)
	return err
}
