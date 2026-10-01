// Package monitoring holds deadman's use cases, between the TUI and the domain:
// opening a monitoring session over the configured targets, rereading the config into
// it (carrying each target's statistics across the reload), explaining a config's
// pitfalls as operator-facing startup warnings, and recording each probe result.
//
// The infrastructure the use cases need is reached through the ports declared here
// (Ports), which cmd/deadman wires to the real adapters and tests to fakes. This
// package does no I/O of its own.
package monitoring

import (
	"context"
	"fmt"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Service runs deadman's monitoring use cases over its Ports.
type Service struct {
	ports Ports
}

// NewService returns a Service running on p.
func NewService(p Ports) *Service {
	return &Service{ports: p}
}

// entry is a row as the session owns it: the monitored entity, the plan it was built
// from, and the adapter that probes it. It never crosses the application boundary;
// Table hands out detached copies.
type entry struct {
	target   *monitor.Target
	plan     probe.Plan
	pinger   probe.Pinger
	position int // position in the table, including separators and rejected lines.
}

// Open reads the config through the ConfigSource and returns the session that owns and
// monitors its targets, with what the frontend needs from the read. err is the read
// error; no session exists then. Closing the session (or canceling ctx) stops its
// monitoring and releases every adapter it holds, including those of a pending reload.
// async selects parallel probing (every target at once each round) over sequential.
func (s *Service) Open(ctx context.Context, async bool) (*Session, Loaded, error) {
	cfg, err := s.ports.LoadConfig(ctx)
	if err != nil {
		return nil, Loaded{}, err
	}

	rows, lines, loaded := s.build(cfg)
	ports := sessionPorts{reread: s.reread, log: s.ports.Log, wait: s.ports.Wait}
	session := newSession(ctx, ports, rows, lines, async)

	return session, loaded, nil
}

// reread reads the config again and builds fresh rows from it, owned by ctx until a
// session takes them. It never reads the live targets, so it may run off the state
// owner while monitoring continues. rows is nil when the config cannot be read; loaded
// then carries only a ReloadFailed diagnostic.
func (s *Service) reread(ctx context.Context) (*preparedRows, []Line, Loaded) {
	cfg, err := s.ports.LoadConfig(ctx)
	if err != nil {
		return nil, nil, Loaded{Warnings: []Diagnostic{ReloadFailed{Reason: err.Error()}}}
	}

	if ctx.Err() != nil {
		return nil, nil, Loaded{}
	}

	rows, lines, loaded := s.build(cfg)

	return prepareRows(ctx, rows), lines, loaded
}

// build turns a config read into fresh monitored rows, table layout and metadata. The
// warnings explain how lines were written and how targets can be probed on this host.
//
// A target that cannot be built is NOT fatal: it becomes a rejected line that always
// shows a failure, so one bad target does not stop the monitoring of all the others.
// Statistics carried over from live targets are the session's job, so building never
// reads the targets being monitored.
func (s *Service) build(cfg config.Config) ([]entry, []Line, Loaded) {
	rows, lines, loaded := s.buildRows(cfg.Lines)
	warns := append(noteWarnings(cfg.Notes), probeWarnings(probeTargets(rows), s.ports.Host)...)
	loaded.Warnings = append(warns, loaded.Warnings...)
	loaded.Display = cfg.Display

	return rows, lines, loaded
}

// buildRows separates executable targets, rejected rows and visual separators.
func (s *Service) buildRows(lines []config.Line) ([]entry, []Line, Loaded) {
	rows := make([]entry, 0, len(lines))
	table := make([]Line, 0, len(lines))
	loaded := Loaded{}

	var ids monitor.Numbering

	for _, line := range lines {
		switch l := line.(type) {
		case config.Separator:
			table = append(table, Separator{})
		case config.Malformed:
			loaded.reject(
				&table,
				l.Name,
				l.Addr,
				fmt.Errorf("invalid configuration: %s", l.Problem),
			)
		case config.Target:
			row, err := s.buildRow(l, &ids)
			if err != nil {
				loaded.reject(&table, l.Name, l.Addr, err)

				continue
			}

			row.position = len(table)
			table = append(table, Monitored{})
			rows = append(rows, row)
		default:
			// Unreachable: config.Line is a closed set, each kind cased above.
		}
	}

	return rows, table, loaded
}

// reject records a target line that could not be built: its line, and its diagnostic.
func (l *Loaded) reject(lines *[]Line, name, addr string, err error) {
	l.Warnings = append(l.Warnings, TargetBuildFailed{Name: name, Addr: addr, Reason: err.Error()})
	*lines = append(*lines, Rejected{Name: name, Addr: addr, Reason: err.Error()})
}

// buildRow compiles one target line, numbers its row among the table's rows (ids) and
// builds the adapter that probes it. A line that cannot be compiled or built returns only
// its construction error.
func (s *Service) buildRow(t config.Target, ids *monitor.Numbering) (entry, error) {
	plan, err := probe.Compile(t.ProbeSpec())
	if err != nil {
		return entry{}, err
	}

	target := ids.Next(plan, t.Name, t.Addr)

	p, err := s.ports.NewPinger(plan, target.Snapshot().ID)
	if err != nil {
		return entry{}, err
	}

	return entry{target: target, plan: plan, pinger: p}, nil
}

// carry lets the live targets continue in the fresh rows (monitor.Carry decides which);
// the fresh rows keep their fresh adapters. Both sets contain executable targets only.
// Only the state owner may call this; preparation never reads or mutates live targets.
func carry(rows, live []entry) {
	monitor.Carry(targetsOf(rows), targetsOf(live))
}

// targetsOf lists the entities owned by the executable rows.
func targetsOf(rows []entry) []*monitor.Target {
	targets := make([]*monitor.Target, len(rows))
	for i, r := range rows {
		targets[i] = r.target
	}

	return targets
}
