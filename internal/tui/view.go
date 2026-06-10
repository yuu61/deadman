package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/yuu61/deadman/internal/monitor"
)

// View renders the entire screen.
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(m.centerTitle()) // line 0: centered program name.
	b.WriteByte('\n')
	b.WriteString(m.titleLine()) // line 1: host info (+spinner) and version.
	b.WriteByte('\n')
	b.WriteString(m.keysLine()) // line 2: scale + key legend.
	b.WriteByte('\n')

	for _, w := range m.warnings {
		b.WriteString(styleDown.Render("! " + w))
		b.WriteByte('\n')
	}

	vp := m.scrollMetrics()

	b.WriteByte('\n') // blank line before the column header(s).

	// Render only the visible window. targetLine takes the absolute row index so the
	// probe arrow (arrowFor reads m.arrowIdx / m.inflight by absolute index) stays
	// correct when scrolled.
	if vp.cols <= 1 {
		b.WriteString(m.headerLine()) // column headers.
		b.WriteByte('\n')

		for i := vp.top; i < vp.top+vp.count && i < len(m.rows); i++ {
			if m.rows[i].Sep {
				b.WriteString(m.separatorLine())
			} else {
				b.WriteString(m.targetLine(i, m.rows[i].Target))
			}

			b.WriteByte('\n')
		}
	} else {
		// Newspaper layout: header and rows laid out as side-by-side columns. The
		// merged block's first line is the (repeated) column header, so it occupies the
		// same screen row headerLine() would, keeping fixedHeaderLines() exact.
		b.WriteString(m.renderColumns(vp))
		b.WriteByte('\n')
	}

	if vp.status {
		b.WriteString(m.scrollStatus(vp))
		b.WriteByte('\n')
	}

	// Trim the trailing newline so the logical line count equals the physical
	// line count, keeping the row-window math (fixedHeaderLines) exact.
	return strings.TrimRight(b.String(), "\n")
}

// keysLine is the scale + key-legend line (line 2), factored into a method to sit
// alongside the other fixed-line builders (centerTitle/titleLine/headerLine).
func (m Model) keysLine() string {
	s := rear + fmt.Sprintf("RTT Scale %dms.", m.scale)

	// The effective-vs-requested column count sits near the front, before the long
	// key legend, so the renderer's width truncation (it clips lines, not wraps)
	// never hides it: a request is clamped precisely on a narrow terminal, where a
	// tail-appended hint would be the first thing cut.
	if m.cols > 1 {
		s += fmt.Sprintf(" cols %d/%d", m.effectiveCols(), m.cols)
	}

	s += fmt.Sprintf(
		" Keys: (q)uit (r)efresh (R)eload (m)in/max (v)ia (↑/↓)scale (p)recision[%s] ([/])cols",
		m.precMode().Label,
	)

	return s
}

// scrollStatus is the one-line position indicator shown below the row window when
// the list is scrolled. It is truncated to the terminal width so it always
// occupies exactly one physical line, which the row-window math assumes.
func (m Model) scrollStatus(vp viewport) string {
	first := vp.top + 1
	last := min(vp.top+vp.count, len(m.rows))
	s := fmt.Sprintf(
		"%s[%d-%d/%d]  j/k scroll  PgUp/PgDn page  g/G top/bottom",
		rear,
		first,
		last,
		len(m.rows),
	)

	return styleBold.Render(runewidth.Truncate(s, m.width, ""))
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
	target := m.width - len(arrow) - len(titleVersion) // column where the version starts.
	gap := max(target-(len(arrow)+leftW), 1)

	return rear + styleBold.Render(left) + strings.Repeat(" ", gap) + styleBold.Render(titleVersion)
}

func (m Model) headerLine() string {
	h := rear + padRight("HOSTNAME", m.hostW) + " " + padRight("ADDRESS", m.addrW) + " "
	if m.columnVisible(colVia) {
		h += padRight("VIA", m.viaW) + " "
	}

	h += m.statsHeader() + "  RESULT"

	return styleBold.Render(h)
}

func (m Model) separatorLine() string {
	n := max(m.width-2*len(arrow), 0)

	return rear + strings.Repeat("-", n)
}

// arrowFor returns the leading arrow ("> ") for the target at idx, or the blank
// rear when it is not being probed. In sync mode only the current target shows
// it; in async mode every in-flight target does (blinking with -b).
func (m Model) arrowFor(idx int) string {
	switch {
	case !m.opts.Async:
		if m.arrowIdx == idx {
			return arrow
		}
	case idx < len(m.inflight) && m.inflight[idx]:
		if !m.opts.Blink || m.blinkOn {
			return arrow
		}
	default:
		// async target not currently in flight: no arrow.
	}

	return rear
}

func (m Model) targetLine(idx int, t *monitor.Target) string {
	ar := m.arrowFor(idx)

	stats := m.statsLine(t)

	text := ar + padRight(t.Name, m.hostW) + " " + padRight(t.Addr, m.addrW) + " "
	if m.columnVisible(colVia) {
		text += padRight(t.Via, m.viaW) + " "
	}

	text += stats
	if t.State != monitor.Up {
		text = styleBold.Render(text)
	}

	var g strings.Builder

	for _, res := range t.Results(m.resW) {
		ch := monitor.Glyph(res, m.scale)
		if monitor.IsFailGlyph(ch) {
			g.WriteString(styleDown.Render(ch))
		} else {
			g.WriteString(styleUp.Render(ch))
		}
	}

	return text + g.String()
}

// renderColumns lays the visible window out as vp.cols side-by-side newspaper
// columns (column-major: column 0 holds the first vp.perCol rows, column 1 the next,
// and so on). Each column carries its own header so it is self-labeled, and every
// cell is padded to the per-column content width so the columns line up.
func (m Model) renderColumns(vp viewport) string {
	w := m.colContentWidth()
	header := padCell(m.headerLine(), w)

	blocks := make([][]string, vp.cols)

	for j := range blocks {
		lines := make([]string, 0, vp.perCol+1)
		lines = append(lines, header)

		start := vp.top + j*vp.perCol
		for k := range vp.perCol {
			lines = append(lines, m.columnCell(start+k, w))
		}

		blocks[j] = lines
	}

	return joinColumns(blocks)
}

// columnCell renders the row at absolute index i to exactly w display columns for
// the newspaper layout, or a blank cell when i is past the last row (the final
// column may be short). targetLine is given the absolute index so arrowFor keeps the
// probe arrow on the right row.
func (m Model) columnCell(i, w int) string {
	switch {
	case i >= len(m.rows):
		return strings.Repeat(" ", w)
	case m.rows[i].Sep:
		return padCell(separatorCell(w), w)
	default:
		return padCell(m.targetLine(i, m.rows[i].Target), w)
	}
}

// joinColumns merges equal-height column blocks side by side, separated by
// colGutter. Every cell is already padded to its column width, so a plain row-by-row
// concatenation aligns.
func joinColumns(blocks [][]string) string {
	if len(blocks) == 0 {
		return ""
	}

	var b strings.Builder

	rows := len(blocks[0])
	for i := range rows {
		for j, col := range blocks {
			if j > 0 {
				b.WriteString(colGutter)
			}

			b.WriteString(col[i])
		}

		if i < rows-1 {
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// padCell fits s to exactly w display columns, measuring width ANSI-aware
// (lipgloss.Width ignores the SGR escapes styleBold/styleUp/styleDown add, and
// ansi.Truncate never cuts mid-escape) so the colored glyphs and bold header stay
// intact — unlike padRight, whose runewidth basis would count the escape bytes.
// Cells are normally <= w (effectiveCols budgets the result bar), but a long-uptime
// SNT/FAIL count can widen the stats past their fixed header budget; the over-width
// branch then trims the cell from the right (cutting the result bar's oldest glyphs,
// the least-important content) so the following column never shifts.
func padCell(s string, w int) string {
	switch sw := lipgloss.Width(s); {
	case sw > w:
		return ansi.Truncate(s, w, "")
	case sw < w:
		return s + strings.Repeat(" ", w-sw)
	default:
		return s
	}
}

// separatorCell is the column-local separator: dashes filling one column's content
// width, mirroring separatorLine's leading rear so it lines up with the data rows.
func separatorCell(w int) string {
	n := max(w-2*len(arrow), 0)

	return rear + strings.Repeat("-", n)
}

func spinner(tick int) string {
	return string(spinnerChars[tick%len(spinnerChars)])
}

func displayWidth(s string) int {
	return runewidth.StringWidth(s)
}

// padRight truncates s to width w (no ellipsis) and pads it with spaces to exactly
// w display columns.
func padRight(s string, w int) string {
	return runewidth.FillRight(runewidth.Truncate(s, w, ""), w)
}
