package monitoring

import (
	"reflect"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestSeparatorBlank(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lines   []config.Line
		enabled bool
		want    []Line
	}{
		{
			name: "skip leading separator",
			lines: []config.Line{
				config.Separator{Label: "First"},
				config.Target{Name: "host", Addr: "192.0.2.1"},
				config.Separator{Label: "Second"},
			},
			enabled: true,
			want:    []Line{Separator{Label: "First"}, Monitored{}, Blank{}, Separator{Label: "Second"}},
		},
		{
			name: "first separator after a target",
			lines: []config.Line{
				config.Target{Name: "host", Addr: "192.0.2.1"}, config.Separator{},
			},
			enabled: true,
			want:    []Line{Monitored{}, Blank{}, Separator{}},
		},
		{
			name:    "consecutive separators",
			lines:   []config.Line{config.Separator{}, config.Separator{}},
			enabled: true,
			want:    []Line{Separator{}, Blank{}, Separator{}},
		},
		{
			name: "preserve existing blanks",
			lines: []config.Line{
				config.Target{Name: "host", Addr: "192.0.2.1"},
				config.Blank{},
				config.Blank{},
				config.Separator{},
			},
			enabled: true,
			want:    []Line{Monitored{}, Blank{}, Blank{}, Separator{}},
		},
		{
			name:  "disabled",
			lines: []config.Line{config.Separator{}, config.Separator{}},
			want:  []Line{Separator{}, Separator{}},
		},
		{name: "empty", enabled: true, want: []Line{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configOf(tc.lines...)
			cfg.Display.SeparatorBlank = tc.enabled
			src := &fakeSource{cfg: cfg}
			svc := NewService(
				Ports{NewPinger: fakePingers(nil), LoadConfig: src.load, Host: capable},
			)

			session, loaded := openSession(t, svc, false)
			if len(loaded.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %v", loaded.Warnings)
			}

			got := session.Table()
			for index, line := range got {
				if _, monitored := line.(Monitored); monitored {
					got[index] = Monitored{}
				}
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("table = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestReloadSeparatorBlankPreservesHistory(t *testing.T) {
	spec := config.Target{Name: "host", Addr: "192.0.2.1"}
	src := source(config.Separator{}, spec, config.Separator{}, spec)
	svc := NewService(Ports{NewPinger: fakePingers(nil), LoadConfig: src.load, Host: capable})

	session, _ := openSession(t, svc, false)
	for index := range 2 {
		session.Update(probeResult{
			index:      index,
			generation: session.gen.id,
			result:     probe.SuccessResult(float64(index + 1)),
		}, time.Now())
	}

	before := monitoredRows(session.Table())

	for _, enabled := range []bool{true, false} {
		src.cfg.Display.SeparatorBlank = enabled

		out := reload(session)
		if out.Reload == nil || !out.Reload.Applied {
			t.Fatalf("reload failed: %+v", out.Reload)
		}

		position := 3
		if enabled {
			position = 4
		}

		if len(session.Table()) != position+1 || session.gen.rows[1].position != position ||
			len(out.Inflight) != position+1 {
			t.Fatalf("separator_blank=%v: incorrect table positions: %+v", enabled, session.Table())
		}

		if got := monitoredRows(session.Table()); !reflect.DeepEqual(got, before) {
			t.Errorf("separator_blank=%v changed target identity, statistics or history", enabled)
		}
	}
}
