package tui

import (
	"slices"

	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// viewPrefs are the view preferences the keys adjust. Of these a reload reapplies only
// the configured column visibility (see showReload).
type viewPrefs struct {
	visible map[string]bool // per-column visibility (config defaults + 'm'/'h'/'a'/'v' toggles).

	scale   float64       // RTT-bar ms-per-step (the window floor in log mode); adjusted live with up/down.
	logIdx  int           // index into logFactors (0 = linear); cycled with 'l'. The selected LnBase drives Glyph.
	precIdx int           // index into precisionModes for the stat columns; cycled with 'p'.
	bar     resultbar.Bar // RESULT-bar glyph set; cycled with 'b'.

	cols int // requested newspaper-column count ('['/']'); effectiveCols clamps it to the terminal width.
}

// columnVisible reports whether the column is shown. A nil map (a Model not built
// through New) is treated as all-shown.
func (p viewPrefs) columnVisible(key string) bool {
	if p.visible == nil {
		return true
	}

	return p.visible[key]
}

// precMode returns the active precision mode for the time-stat columns. A precIdx
// out of range (e.g. a Model not built through New) falls back to ms.
func (p viewPrefs) precMode() precisionMode {
	if p.precIdx < 0 || p.precIdx >= len(precisionModes) {
		return precisionModes[0]
	}

	return precisionModes[p.precIdx]
}

// logFactor returns the active RTT-scale log factor, clamping an out-of-
// range logIdx to the linear entry so a Model not built through New can never panic the
// render (mirrors precMode). Callers read both its Label (legend) and LnBase (mode/base).
func (p viewPrefs) logFactor() logFactor {
	if p.logIdx < 0 || p.logIdx >= len(logFactors) {
		return logFactors[0]
	}

	return logFactors[p.logIdx]
}

// adjustRTTScale handles the RTT-bar scale and log-factor keys: up/down step the
// scale ladder and 'l' cycles the log factor (0 linear -> e -> e²). None of these
// changes a column width, so the caller needs no width recalc.
func (p viewPrefs) adjustRTTScale(key string) viewPrefs {
	switch key {
	case "up":
		p.scale = scaleUp(p.scale)
	case "down":
		p.scale = scaleDown(p.scale)
	case "l":
		p.logIdx = (p.logIdx + 1) % len(logFactors)
	default:
		// Unreachable: handleViewKey routes only up/down/l here.
	}

	return p
}

// toggleStructuralCol flips the visibility of the structural string column bound to
// the key: 'h' -> HOSTNAME, 'a' -> ADDRESS, 'v' -> VIA. RESULT has no key and is
// never hidden. The caller recomputes the layout afterwards, since each column's
// width feeds the result-bar column and the multi-column fit.
func (p viewPrefs) toggleStructuralCol(key string) viewPrefs {
	switch key {
	case "h":
		p.visible[colHost] = !p.visible[colHost]
	case "a":
		p.visible[colAddr] = !p.visible[colAddr]
	case "v":
		p.visible[colVia] = !p.visible[colVia]
	default:
		// Unreachable: handleViewKey routes only "h"/"a"/"v" here.
	}

	return p
}

// logFactor is one entry in the 'l'-key cycle: a legend label paired with LnBase, the
// log base carried as a value rather than implied by the slice index. Mirrors
// precisionMode (label + behavior in one row).
type logFactor struct {
	Label string
	// LnBase is the ln of the per-step bucket base, i.e. the divisor in
	// ln(rtt/scale)/LnBase handed to resultbar.Glyph: 0 = linear (no log), 1 = base e,
	// 2 = base e². Because it is data, not the index, a new factor is just a row — base
	// 10 would be {"x10", math.Log(10)} — with no monitor change. Labels stay ASCII
	// (narrow): "×"/"²" are East-Asian-ambiguous and render 2 cells on CJK terminals,
	// desyncing the keys-line width from the renderer's measure.
	LnBase float64
}

// logFactors is the single source of the 'l'-key cycle order, the log base each entry
// selects, and the floor-mode legend labels for the log factors: linear (no log), then
// base e and base e². Index 0's "lin" label is a cycle-slot placeholder, not rendered:
// keysLine labels linear mode with its own "RTT Scale" wording (vs "RTT floor … xe"),
// since linear shows ms-per-step while a log factor shows the window floor. So the table
// is the single source of the log-factor labels (index > 0); the linear row is self-named.
var logFactors = []logFactor{
	{"lin", 0},
	{"xe", 1},
	{"xe2", 2},
}

// scaleSteps is the ladder the up/down keys move the RTT-bar scale through (ms); it
// extends below 1ms so sub-millisecond LAN RTTs can be resolved.
var scaleSteps = []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 50, 100}

// scaleUp returns the next coarser (larger-ms) rung above cur. An off-ladder cur
// (e.g. from -s 7) snaps up to the nearest rung, so the live scale stays a free-form
// value while the keys move through sensible presets. At or above the top rung cur is
// preserved: Up means coarser, so it must never decrease the scale (a free-form
// -s 1000 stays 1000 rather than snapping down to the ladder top).
func scaleUp(cur float64) float64 {
	for _, s := range scaleSteps {
		if s > cur {
			return s
		}
	}

	return cur
}

// scaleDown returns the next finer (smaller-ms) rung below cur. At or below the bottom
// rung cur is preserved: Down means finer, so it must never increase the scale — the
// mirror of scaleUp's top guard, so a free-form -s 0.005 stays 0.005 rather than being
// snapped up to the ladder bottom.
func scaleDown(cur float64) float64 {
	for _, s := range slices.Backward(scaleSteps) {
		if s < cur {
			return s
		}
	}

	return cur
}
