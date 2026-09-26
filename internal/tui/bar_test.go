package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/yuu61/deadman/internal/config"
)

// barKey is the 'b' keypress that cycles the RESULT-bar glyph set.
var barKey = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}

// lastGlyph returns the final cell of the row showing addr — the newest result glyph,
// since the single probe fed in by these tests is the whole bar.
func lastGlyph(out, addr string) string {
	ln := strings.TrimRight(ansi.Strip(lineWith(out, addr)), " ")
	if ln == "" {
		return ""
	}

	r := []rune(ln)

	return string(r[len(r)-1])
}

// TestBarKeyCyclesGlyphSet drives 'b' through block -> ascii -> digit -> block and
// checks both the legend label and that the stored result re-renders in the new set
// without a new probe. RTT 35 at scale 10 is band 3: ▄ / = / 3.
func TestBarKeyCyclesGlyphSet(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m := newModel(t, specs, Options{Scale: 10})

	m, out := drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 40}, okResult(m, 35))

	steps := []struct {
		label, glyph string
	}{
		{"(b)ar[block]", "▄"},
		{"(b)ar[ascii]", "="},
		{"(b)ar[digit]", "3"},
		{"(b)ar[block]", "▄"}, // wraps.
	}
	for i, s := range steps {
		if i > 0 {
			m, out = drive(t, m, barKey)
		}

		if !strings.Contains(out, s.label) {
			t.Errorf("step %d: legend missing %q\n---\n%s", i, s.label, out)
		}

		if got := lastGlyph(out, "1.2.3.4"); got != s.glyph {
			t.Errorf(
				"step %d (%s): RTT 35 renders %q, want %q\n---\n%s",
				i,
				s.label,
				got,
				s.glyph,
				out,
			)
		}
	}
}

// TestGlyphFromOptionsAtStartup checks the resolved Options.Glyph (CLI -g, config
// "glyph" or auto-detection, all resolved by the CLI) takes effect at the first paint,
// and that an unknown name falls back to the block set instead of failing.
func TestGlyphFromOptionsAtStartup(t *testing.T) {
	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	cases := []struct {
		glyph, label, want string
	}{
		{"ascii", "(b)ar[ascii]", "="},
		{"digit", "(b)ar[digit]", "3"},
		{"", "(b)ar[block]", "▄"},
		{"bogus", "(b)ar[block]", "▄"},
	}
	for _, c := range cases {
		m := newModel(t, specs, Options{Scale: 10, Glyph: c.glyph})

		_, out := drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 40}, okResult(m, 35))
		if !strings.Contains(out, c.label) {
			t.Errorf("Glyph %q: legend missing %q\n---\n%s", c.glyph, c.label, out)
		}

		if got := lastGlyph(out, "1.2.3.4"); got != c.want {
			t.Errorf("Glyph %q: RTT 35 renders %q, want %q\n---\n%s", c.glyph, got, c.want, out)
		}
	}
}

// TestReloadPreservesGlyph guards the documented reload asymmetry: like scale and
// precision, the live glyph set survives a reload even when the file carries a "glyph"
// directive, so a set picked by -g, detection or the 'b' key is not silently reset.
func TestReloadPreservesGlyph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deadman.conf")

	err := os.WriteFile(path, []byte("glyph block\nh 1.2.3.4\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m := newModel(t, specs, Options{Scale: 10, Glyph: "ascii", ConfigPath: path})

	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 40}, barKey) // ascii -> digit.

	_, out := drive(t, m, reloadMsg{})
	if !strings.Contains(out, "(b)ar[digit]") {
		t.Errorf("reload should keep the live glyph set (digit)\n---\n%s", out)
	}
}

// TestKeysLineClippedToWidth pins that the legend, now longer than a 120-column
// terminal, is clipped by View itself rather than overflowing the line budget.
func TestKeysLineClippedToWidth(t *testing.T) {
	const width = 100

	specs := []config.TargetSpec{{Name: "h", Addr: "1.2.3.4", Relay: map[string]string{}}}

	m := newModel(t, specs, Options{Scale: 10})

	_, out := drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

	lines := strings.Split(out, "\n")
	if len(lines) < 3 || !strings.Contains(lines[2], "Keys:") {
		t.Fatalf("line 2 is not the keys line\n---\n%s", out)
	}

	assertNoLineExceedsWidth(t, lines[2], width)
}
