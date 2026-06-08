package tui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/yuu61/deadman/internal/monitor"
)

// View renders the entire screen, mirroring the original curses layout.
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(m.centerTitle()) // line 0: centered program name
	b.WriteByte('\n')
	b.WriteString(m.titleLine()) // line 1: host info (+spinner) and version
	b.WriteByte('\n')
	b.WriteString(rear) // line 2
	fmt.Fprintf(&b, "RTT Scale %dms. Keys: (r)efresh", m.opts.Scale)
	b.WriteByte('\n')
	b.WriteByte('\n')             // line 3: blank
	b.WriteString(m.headerLine()) // line 4: column headers
	b.WriteByte('\n')

	for i, r := range m.rows {
		if r.Sep {
			b.WriteString(m.separatorLine())
		} else {
			b.WriteString(m.targetLine(i, r.Target))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m Model) centerTitle() string {
	pad := max((m.width-len(titleProgName))/2, 0)
	return strings.Repeat(" ", pad) + styleBold.Render(titleProgName)
}

func (m Model) titleLine() string {
	left := m.hostInfo
	if m.opts.Async {
		left += " " + spinner(m.tick)
	}
	leftW := runewidth.StringWidth(left)
	target := m.width - len(arrow) - len(titleVersion) // column where the version starts
	gap := max(target-(len(arrow)+leftW), 1)
	return rear + styleBold.Render(left) + strings.Repeat(" ", gap) + styleBold.Render(titleVersion)
}

func (m Model) headerLine() string {
	h := rear + padRight("HOSTNAME", m.hostW) + " " + padRight("ADDRESS", m.addrW) + " " + refHeader + "  RESULT"
	return styleBold.Render(h)
}

func (m Model) separatorLine() string {
	n := max(m.width-2*len(arrow), 0)
	return rear + strings.Repeat("-", n)
}

func (m Model) targetLine(idx int, t *monitor.Target) string {
	ar := rear
	switch {
	case !m.opts.Async:
		if m.arrowIdx == idx {
			ar = arrow
		}
	case idx < len(m.inflight) && m.inflight[idx]:
		// async: arrow on every target currently being probed, so the round's
		// parallelism is visible. With -b the arrows blink instead of staying lit.
		if !m.opts.Blink || m.blinkOn {
			ar = arrow
		}
	}

	stats := fmt.Sprintf(" %3d%% %4d %4d %4d  ",
		int(t.LossRate), int(t.RTT), int(t.Avg), t.Snt)

	text := ar + padRight(t.Name, m.hostW) + " " + padRight(t.Addr, m.addrW) + " " + stats
	if t.State != monitor.Up {
		text = styleBold.Render(text)
	}

	var g strings.Builder
	for _, ch := range t.Glyphs(m.resW) {
		if monitor.IsFailGlyph(ch) {
			g.WriteString(styleDown.Render(ch))
		} else {
			g.WriteString(styleUp.Render(ch))
		}
	}
	return text + g.String()
}

func spinner(tick int) string {
	return string(spinnerChars[tick%len(spinnerChars)])
}

func displayWidth(s string) int {
	return runewidth.StringWidth(s)
}

// padRight truncates s to width w (no ellipsis, matching the original slice) and
// pads it with spaces to exactly w display columns.
func padRight(s string, w int) string {
	return runewidth.FillRight(runewidth.Truncate(s, w, ""), w)
}
