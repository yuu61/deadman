package tui

import (
	"math"
	"testing"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

func TestCheckSetting(t *testing.T) {
	for _, setting := range []config.Setting{
		config.Scale(resultbar.MinScale), config.Scale(resultbar.MaxScale),
		config.Split(1), config.Split(100), config.Glyph("auto"), config.Glyph("ASCII"),
		config.Precision("ms"), config.Precision("MS.3"),
		config.Column{Key: "HOSTNAME"},
		config.Column{Key: "MIN", Visible: true},
	} {
		err := CheckSetting(setting)
		if err != nil {
			t.Errorf("CheckSetting(%+v) = %v", setting, err)
		}
	}

	for _, setting := range []config.Setting{
		config.Scale(0), config.Scale(-1), config.Scale(math.NaN()), config.Scale(math.Inf(1)),
		config.Scale(resultbar.MinScale / 2), config.Scale(resultbar.MaxScale * 2),
		config.Split(0), config.Split(-1), config.Glyph(""), config.Glyph("typo"),
		config.Precision(""), config.Precision("ms.4"), config.Precision("bogus"),
		config.Column{Key: "RESULT"},
		config.Column{Key: "UNKNOWN"},
	} {
		err := CheckSetting(setting)
		if err == nil {
			t.Errorf("CheckSetting(%+v) accepted invalid value", setting)
		}
	}
}
