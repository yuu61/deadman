package resultbar

// Scale bounds. A scale outside [MinScale, MaxScale] is unusable, not merely degenerate:
// below MinScale every real RTT overflows the top band (the bar is uniformly "█") and the
// shortest-decimal footer label balloons toward hundreds of characters (FormatFloat 'f'
// of 1e-300 is ~300 digits), shoving the key legend off-screen; above MaxScale every bar
// collapses to the floor. MinScale (0.1 µs) is far finer and MaxScale (1000 s) far coarser
// than any real network bar, so the usable window stays generous while nonsense is rejected.
// Exported so the rejection warning formats the bounds from these constants instead of
// carrying a prose copy that drifts when the window changes.
const (
	MinScale = 1e-4
	MaxScale = 1e6
)

// ValidScale reports whether v is a usable RTT-bar scale: within [MinScale, MaxScale],
// which also excludes NaN, ±Inf, zero and negatives. It is the single predicate for the
// CLI -s flag, the "scale" directive and the TUI's startup normalization, so the
// accept/reject boundary cannot drift between them.
func ValidScale(v float64) bool {
	return v >= MinScale && v <= MaxScale
}

// DefaultScale is the RTT-bar scale used when neither the CLI -s flag nor a "scale"
// directive supplies a valid value.
const DefaultScale = 1.0

// ScaleOrDefault returns v when it is a usable scale and DefaultScale otherwise, so the
// "invalid → default" normalization lives in one place.
func ScaleOrDefault(v float64) float64 {
	if ValidScale(v) {
		return v
	}

	return DefaultScale
}
