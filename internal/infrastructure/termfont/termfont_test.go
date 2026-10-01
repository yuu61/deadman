package termfont

import (
	"os"
	"testing"
)

func TestUTF8Locale(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"lang_utf8", map[string]string{"LANG": "en_US.UTF-8"}, true},
		{"lang_utf8_lower", map[string]string{"LANG": "ja_JP.utf8"}, true},
		{"c_utf8", map[string]string{"LANG": "C.UTF-8"}, true},
		{"macos_ctype", map[string]string{"LC_CTYPE": "UTF-8"}, true},
		{"lang_c", map[string]string{"LANG": "C"}, false},
		{"lang_posix", map[string]string{"LANG": "POSIX"}, false},
		{"lang_eucjp", map[string]string{"LANG": "ja_JP.eucJP"}, false},
		// POSIX precedence: LC_ALL overrides LC_CTYPE, which overrides LANG.
		{"lc_all_wins", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, false},
		{"lc_ctype_wins", map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}, true},
		// An empty variable is skipped, not taken as the answer.
		{"empty_lc_all", map[string]string{"LC_ALL": "", "LANG": "C"}, false},
		// Nothing set is not evidence of a non-UTF-8 terminal.
		{"unset", map[string]string{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := utf8Locale(func(k string) string { return c.env[k] }); got != c.want {
				t.Errorf("utf8Locale(%v) = %v, want %v", c.env, got, c.want)
			}
		})
	}
}

// TestCanRenderNonConsole checks the pty/pipe path: nothing about the font can be
// inspected, so only the locale can veto. A pipe stands in for any non-console output.
func TestCanRenderNonConsole(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "en_US.UTF-8")

	if !CanRender(w, "▁▂▃▄▅▆▇█") {
		t.Error("UTF-8 locale on a non-console: CanRender = false, want true")
	}

	t.Setenv("LANG", "C")

	if localeChecked && CanRender(w, "▁▂▃▄▅▆▇█") {
		t.Error("LANG=C: CanRender = true, want false (non-UTF-8 locale)")
	}
}
