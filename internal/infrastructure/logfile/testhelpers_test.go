package logfile

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// testTime is the fixed instant used across the log tests so written timestamps are
// deterministic.
var testTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// readLogLines reads <dir>/<name> and returns each non-empty line split into fields
// (<date> <time> <status> <rtt> <avg> <snt>).
func readLogLines(t *testing.T, dir, name string) [][]string {
	t.Helper()

	b, err := fs.ReadFile(os.DirFS(dir), name)
	if err != nil {
		t.Fatalf("read log %s: %v", name, err)
	}

	var out [][]string
	for ln := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		out = append(out, strings.Fields(ln))
	}

	return out
}
