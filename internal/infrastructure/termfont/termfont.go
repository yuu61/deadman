// Package termfont probes whether the terminal deadman draws on can display a given
// set of characters, so the CLI can fall back to an ASCII result bar where the block
// elements would render as garbage.
//
// Two signals are checked. Both are conservative: anything that cannot be inspected
// counts as renderable, since the block bar is the default and -g can always force a
// set.
//
//   - The locale: an explicitly non-UTF-8 LC_ALL / LC_CTYPE / LANG (e.g. "C" or
//     "ja_JP.eucJP") means the terminal is not expected to decode UTF-8 (the test tmux
//     applies). Skipped on Windows, whose console takes UTF-16 from Go whatever LANG says.
//   - On Linux, when the output is a virtual console (the kernel's own text console on
//     tty1-tty63), the loaded font's Unicode map, which lists exactly the characters the
//     font can draw. The common default Lat15 font lacks ▁▂▃▄▅▆▇.
//
// A pty (a terminal emulator, ssh, tmux, fbterm) cannot be inspected: the font on the
// far side decides, so it counts as renderable.
package termfont

import (
	"os"
	"runtime"
	"strings"
)

// localeChecked gates the locale test. Go writes to a Windows console as UTF-16
// (WriteConsoleW), so LANG — rarely set there anyway — says nothing about rendering.
const localeChecked = runtime.GOOS != "windows"

// CanRender reports whether every rune of s is expected to display on the terminal
// behind f: false when the locale is explicitly non-UTF-8, or when f is a Linux virtual
// console whose font has no glyph for some rune of s; true otherwise.
func CanRender(f *os.File, s string) bool {
	if localeChecked && !utf8Locale(os.Getenv) {
		return false
	}

	return !consoleLacks(f, s)
}

// utf8Locale reports whether the effective character-type locale is UTF-8. It follows
// the POSIX precedence (the first non-empty of LC_ALL, LC_CTYPE, LANG) and matches the
// codeset loosely ("UTF-8", "utf8", macOS's bare LC_CTYPE=UTF-8). An entirely unset
// locale is not evidence of a non-UTF-8 terminal — minimal containers and ssh sessions
// often carry none while the terminal decodes UTF-8 — so it counts as UTF-8.
func utf8Locale(getenv func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := strings.ToLower(getenv(k)); v != "" {
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}

	return true
}
