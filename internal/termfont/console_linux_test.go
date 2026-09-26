//go:build linux

package termfont

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMapCovers(t *testing.T) {
	// A Lat15-like map: ASCII '#' and the full block, but no lower eighth blocks.
	lat15 := []unipair{{'#', 0x23}, {'█', 0xdb}, {'░', 0xb0}}

	cases := []struct {
		name string
		m    []unipair
		s    string
		want bool
	}{
		{"all_present", lat15, "#█", true},
		{"one_missing", lat15, "▁▂▃▄▅▆▇█", false},
		{"empty_string", lat15, "", true},
		{"empty_map", nil, "▁", false},
		{"non_bmp", lat15, "\U0001F600", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mapCovers(c.m, c.s); got != c.want {
				t.Errorf("mapCovers(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

// TestConsoleLacksNotAConsole checks that output which is not a virtual console (the
// kernel rejects GIO_UNIMAP on it) never vetoes, so CanRender falls back to the locale
// instead of wrongly rejecting a pty whose remote font may be fine.
func TestConsoleLacksNotAConsole(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = f.Close() })

	for name, out := range map[string]*os.File{"pipe": w, "file": f} {
		if consoleLacks(out, "▁") {
			t.Errorf("%s: consoleLacks = true, want false (not a virtual console)", name)
		}
	}
}
