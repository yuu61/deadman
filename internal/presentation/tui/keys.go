package tui

import tea "github.com/charmbracelet/bubbletea"

// handleKey processes a keypress: the control keys (quit, refresh 'r', reload 'R')
// are handled here; the display-only keys are delegated to handleViewKey.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.Close()

		return m, tea.Quit
	case "r":
		// Refresh resets the counters to 0, so the stat columns may shrink back; the
		// per-probe growStatWidths only widens, so refreshActiveRows recomputes the layout.
		m.session.ResetStatistics()

		return m.refreshActiveRows(), nil
	case "R":
		return m, taskCommand(m.session.Reload())
	}

	return m.handleViewKey(msg), nil
}

// handleViewKey handles the display-only keys: the MIN/MAX ('m') and structural
// HOSTNAME/ADDRESS/VIA ('h'/'a'/'v') column toggles, the RTT-bar scale (up/down) and
// its log factor ('l'), the stat precision ('p'), the RESULT-bar glyph set ('b'), and
// the newspaper-column count ('['/']'). An unknown key leaves the model unchanged.
//
// The layout-changing keys clampScroll after recalcWidths: a stat-column toggle
// shifts minColumnWidth, which can change effectiveCols and thus the vertical
// capacity, so the scroll position must be re-pinned just like a column-count change.
func (m Model) handleViewKey(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "m":
		// Toggle the MIN/MAX pair: show both unless both are already shown. The
		// header width changes, so the result-bar column (and the multi-column fit)
		// are recomputed.
		show := !m.visible[colMin] || !m.visible[colMax]
		m.visible[colMin] = show
		m.visible[colMax] = show

		return m.recalcWidths().clampScroll()
	case "h", "a", "v":
		// Toggle a structural string column: HOSTNAME ('h'), ADDRESS ('a') or VIA
		// ('v'). Each one's width feeds the result-bar column and the multi-column
		// fit, so recompute the layout and re-pin the scroll, like the MIN/MAX toggle.
		m.viewPrefs = m.toggleStructuralCol(msg.String())

		return m.recalcWidths().clampScroll()
	case "up", "down", "l":
		// RTT-bar scale (up/down step the ladder) and its log factor ('l' cycles
		// 0->e->e²->0). Glyphs are bucketed at render time, so the bar re-buckets
		// with no width change.
		m.viewPrefs = m.adjustRTTScale(msg.String())

		return m
	case "p":
		// Cycle the stat precision (ms -> ms.1 -> ms.2 -> ms.3); the column width
		// changes, so recompute the result-bar layout.
		m.precIdx = (m.precIdx + 1) % len(precisionModes)

		return m.recalcWidths().clampScroll()
	case "b":
		// Cycle the RESULT-bar glyph set (block -> ascii -> digit). Every glyph is one
		// cell and is chosen at render time, so no width changes.
		m.bar = m.bar.Next()

		return m
	case "[", "]":
		// Adjust the newspaper-column count (']' more, '[' fewer; effectiveCols clamps
		// it to what the width fits). The count changes both the per-column width and
		// the vertical capacity, so recompute the widths and re-pin the scroll.
		return m.adjustCols(msg.String()).recalcWidths().clampScroll()
	case "k", "j", "pgup", "pgdown", "g", "G", "home", "end":
		// Scroll the row window. When the list fits the screen these are no-ops
		// (maxTop is 0). The arrows stay bound to the RTT-bar scale.
		return m.scroll(msg.String())
	default:
		// Any other key is unbound; leave the model unchanged.
	}

	return m
}

// adjustCols bumps the requested newspaper-column count: ']' adds one, anything else
// ('[') removes one with a floor of a single column. effectiveCols later clamps the
// request to what the terminal width can actually hold.
func (m Model) adjustCols(key string) Model {
	if key == "]" {
		// Cap one beyond what currently fits so repeated ']' on a narrow terminal
		// can't run the request away (needing as many '[' presses to undo). Hiding
		// stat columns lowers minColumnWidth and lifts the cap, so columns can still
		// grow incrementally.
		m.cols = min(m.cols+1, m.effectiveCols()+1)
	} else {
		m.cols = max(m.cols-1, 1)
	}

	return m
}

// scroll moves the row window in response to a navigation key and clamps the new
// top into range. With a list that fits the screen maxTop is 0, so every key is
// a no-op.
func (m Model) scroll(key string) Model {
	vp := m.scrollMetrics()

	switch key {
	case "k":
		m.scrollTop--
	case "j":
		m.scrollTop++
	case "pgup":
		m.scrollTop -= vp.count
	case "pgdown":
		m.scrollTop += vp.count
	case "g", "home":
		m.scrollTop = 0
	case "G", "end":
		m.scrollTop = vp.maxTop
	default:
		// Unreachable: handleViewKey routes only the keys handled above.
	}

	m.scrollTop = max(min(m.scrollTop, vp.maxTop), 0)

	return m
}
