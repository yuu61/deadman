package monitoring

import (
	"reflect"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestColumnBreakBoundariesPreserveIdentityAndHistory(t *testing.T) {
	spec := config.Target{Name: "host", Addr: "192.0.2.1"}
	src := source(spec, spec)
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
	src.cfg.Lines = []config.Line{
		config.ColumnBreak{Label: "LAN"},
		config.Separator{Label: "Routers"},
		spec,
		config.ColumnBreak{Label: "WAN"},
		config.Separator{Label: "Uplinks"},
		spec,
	}
	src.cfg.Display.SeparatorBlank = true

	out := reload(session)
	if out.Reload == nil || !out.Reload.Applied {
		t.Fatalf("reload failed: %+v", out.Reload)
	}

	if got := monitoredRows(session.Table()); !reflect.DeepEqual(got, before) {
		t.Error("split boundaries changed target identity, statistics or history")
	}

	if len(session.Table()) != 6 || session.gen.rows[0].position != 2 ||
		session.gen.rows[1].position != 5 {
		t.Fatalf("incorrect positions or a blank added at split start: %+v", session.Table())
	}

	for index, label := range map[int]string{0: "LAN", 3: "WAN"} {
		if session.Table()[index] != (ColumnBreak{Label: label}) {
			t.Errorf("missing boundary at %d: %+v", index, session.Table())
		}
	}

	src.cfg.Lines = []config.Line{spec, spec}

	out = reload(session)
	if out.Reload == nil || !out.Reload.Applied ||
		!reflect.DeepEqual(monitoredRows(session.Table()), before) {
		t.Error("removing split boundaries lost target history")
	}
}
