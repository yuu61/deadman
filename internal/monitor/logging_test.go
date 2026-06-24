package monitor

import (
	"testing"

	"github.com/yuu61/deadman/internal/ping"
)

// A failed probe must be distinguishable in the log from a successful one: it is
// marked "down" and carries no RTT (0.000), not the previous success's stale value.
func TestAppendLogDistinguishesUpDown(t *testing.T) {
	dir := t.TempDir()
	tg := &Target{Name: "host"}
	now := testTime

	up := ping.Result{Success: true, Code: ping.Success, RTT: 5}
	tg.Consume(up)

	err := AppendLogLine(dir, tg.Name, up, tg.Avg, tg.Snt, now)
	if err != nil {
		t.Fatal(err)
	}

	down := ping.Result{Code: ping.Failed}
	tg.Consume(down)

	err = AppendLogLine(dir, tg.Name, down, tg.Avg, tg.Snt, now)
	if err != nil {
		t.Fatal(err)
	}

	lines := readLogLines(t, dir, "host")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2", len(lines))
	}

	// Fields: <date> <time> <status> <rtt> <avg> <snt>.
	up0 := lines[0]
	down1 := lines[1]

	if len(up0) != 6 || up0[2] != "up" || up0[3] != "5.000" {
		t.Errorf("up line fields = %v, want status=up rtt=5.000", up0)
	}

	if len(down1) != 6 || down1[2] != "down" || down1[3] != "0.000" {
		t.Errorf("down line fields = %v, want status=down rtt=0.000 (not stale 5.000)", down1)
	}
}
