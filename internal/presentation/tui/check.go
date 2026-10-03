package tui

import (
	"fmt"
	"strings"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// CheckSetting validates one explicitly written display value against the same
// registries and bounds used by the TUI. It does not query the terminal.
func CheckSetting(setting config.Setting) error {
	switch value := setting.(type) {
	case config.Scale:
		if !resultbar.ValidScale(float64(value)) {
			return fmt.Errorf(
				"scale must be between %g and %g ms",
				resultbar.MinScale,
				resultbar.MaxScale,
			)
		}
	case config.Split:
		if value <= 0 {
			return fmt.Errorf("split must be a positive integer, got %d", value)
		}
	case config.Precision:
		return checkPrecision(string(value))
	case config.Glyph:
		return CheckGlyph(string(value))
	case config.Column:
		if _, ok := buildVisible(nil)[value.Key]; !ok {
			return fmt.Errorf("unknown or non-configurable column %q", value.Key)
		}
	default:
		panic("unhandled display setting")
	}

	return nil
}

func checkPrecision(value string) error {
	if !strings.EqualFold(precisionModes[precisionIndex(value)].Label, value) {
		return fmt.Errorf("unknown precision %q", value)
	}

	return nil
}
