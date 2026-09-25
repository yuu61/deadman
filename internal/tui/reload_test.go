package tui

import (
	"path/filepath"
	"strings"
	"testing"
)

// A reload that cannot read its config keeps ok=false (the current rows are retained),
// but the reason must surface as a header warning rather than the reload silently doing
// nothing.
func TestLoadRowsFailureSurfacesWarning(t *testing.T) {
	r, ok := loadRows(filepath.Join(t.TempDir(), "does-not-exist.conf"), nil)
	if ok {
		t.Fatal("loadRows ok = true for a missing config, want false")
	}

	if len(r.warnings) != 1 || !strings.Contains(r.warnings[0], "reload failed") {
		t.Errorf("warnings = %v, want a single 'reload failed' entry", r.warnings)
	}
}
