package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/monitor"
)

// However many warnings a config raises, they take at most half of the rows the other
// header lines leave, and the last line shown says how many more there are, so the
// targets stay on the screen.
func TestHeaderWarningsLeaveRoomForTheTargets(t *testing.T) {
	m := sizedModel(t, 3, testOptions{Scale: 10}, 160, 24)

	for i := range 30 {
		m.configWarns = append(m.configWarns, fmt.Sprintf("warning %d", i))
	}

	m = m.composeWarnings().recalcWidths().clampScroll()

	shown := m.headerWarnings()
	if limit := (24 - nonWarnLines) / 2; len(shown) != limit {
		t.Fatalf("header shows %d warning lines, want %d", len(shown), limit)
	}

	if last := shown[len(shown)-1]; last != overflowLine(30-(len(shown)-1)) {
		t.Fatalf("last header warning = %q, want the count of the rest", last)
	}

	if vp := m.scrollMetrics(); vp.count != 3 {
		t.Fatalf("viewport %+v shows %d rows, want all 3", vp, vp.count)
	}

	view := m.View()
	assertNoLineExceedsWidth(t, view, 160)

	if strings.Count(view, "\n")+1 > 24 {
		t.Fatalf("view is taller than the terminal:\n%s", view)
	}
}

// Build failures share one header line however many lines of the config fail, and each
// rejected row shows its own reason in its RESULT column.
func TestRejectedRowsShowTheirReasonInline(t *testing.T) {
	lines := make([]config.Line, 0, 40)
	for i := range 40 {
		lines = append(lines, config.Malformed{
			Name: fmt.Sprintf("bad%d", i), Addr: "192.0.2.1", Problem: fmt.Sprintf("problem %d", i),
		})
	}

	m := newModel(t, lines, testOptions{Scale: 10})
	if len(m.warnings) != 1 || !strings.HasPrefix(m.warnings[0], "40 targets could not be built") {
		t.Fatalf("header warnings = %q, want one summary line", m.warnings)
	}

	_, view := drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 60})
	for _, want := range []string{"problem 0", "problem 39"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks the rejected row's reason %q\n%s", want, view)
		}
	}

	assertNoLineExceedsWidth(t, view, 160)
}

// droppingLog is a ResultLog whose store cannot keep up: it drops every line.
type droppingLog struct{}

func (droppingLog) Log(monitor.Reading, time.Time) bool { return false }

// A result line the log drops raises a header warning with the running count.
func TestDroppedLogLinesAreShown(t *testing.T) {
	src := sourceOf([]config.Line{config.Target{Name: "h", Addr: "192.0.2.1"}}, testOptions{})
	r := newReplies()
	svc := monitoring.NewService(monitoring.Ports{
		NewPinger:  r.pinger,
		LoadConfig: src.load,
		Host:       stubHost{},
		Log:        droppingLog{},
		Wait:       noWait,
	})
	scripts.Store(svc, r)

	m := openModel(t, svc, testOptions{})

	m, view := drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30}, success(1), success(2))
	if m.logDropped != 2 || !strings.Contains(view, logDroppedLine(2)) {
		t.Fatalf("logDropped=%d, view:\n%s", m.logDropped, view)
	}

	if len(m.warnings) != 1 {
		t.Fatalf("warnings = %q, want the one log line", m.warnings)
	}
}

type failedLog struct{}

func (failedLog) Log(monitor.Reading, time.Time) bool { return true }
func (failedLog) Err() error                          { return errors.New("disk full") }

func TestLogStorageFailureIsShownAndSurvivesReload(t *testing.T) {
	src := sourceOf([]config.Line{config.Target{Name: "h", Addr: "192.0.2.1"}}, testOptions{})
	replies := newReplies()
	svc := monitoring.NewService(monitoring.Ports{
		NewPinger:  replies.pinger,
		LoadConfig: src.load,
		Host:       stubHost{},
		Log:        failedLog{},
		Wait:       noWait,
	})
	scripts.Store(svc, replies)
	model := openModel(t, svc, testOptions{})

	model, view := drive(t, model, tea.WindowSizeMsg{Width: 160, Height: 30}, success(1))
	if !strings.Contains(view, "log write failed: disk full") {
		t.Fatalf("storage failure invisible: %s", view)
	}

	model.configWarns = nil

	model = model.composeWarnings()
	if len(model.warnings) != 1 || !strings.Contains(model.warnings[0], "disk full") {
		t.Fatal("config refresh discarded storage warning")
	}
}
