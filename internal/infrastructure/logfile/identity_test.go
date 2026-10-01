package logfile

import (
	"testing"

	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Fixed identities and names ensure later versions keep appending to existing logs.
func TestLogUsesStableFileNames(t *testing.T) {
	for _, c := range []struct {
		id   string
		file string
	}{
		{`"direct":"192.0.2.1":"":"1":#1`, "target-5ddf138c33c3fe23e146cb26.log"},
		{`"direct":"192.0.2.1":"":"1":#2`, "target-69e540da5ded8eee468a73bd.log"},
		{`"direct":"2001:db8::1":"":"2":#1`, "target-44de757c4d4f347cfd17e81d.log"},
	} {
		t.Run(c.id, func(t *testing.T) {
			dir := t.TempDir()
			target := monitor.NewTarget(c.id, "web", "example.com")

			// Reopen the store like a restart, and require both records in the fixed file.
			for _, rtt := range []float64{1, 2} {
				store := newLogStore(dir)
				err := store.write(target.Consume(probe.SuccessResult(rtt)), testTime)
				closeStore(t, store)

				if err != nil {
					t.Fatal(err)
				}
			}

			lines := readLogLines(t, dir, c.file)
			if len(lines) != 2 || len(lines[0]) != 6 || len(lines[1]) != 6 ||
				lines[0][3] != "1.000" || lines[1][3] != "2.000" {
				t.Fatalf("records after restart = %v", lines)
			}
		})
	}
}
