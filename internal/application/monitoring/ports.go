package monitoring

import (
	"context"
	"time"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// PingerFactory builds the Pinger that probes by a compiled plan for the row whose ID is
// row (see monitor.Snapshot.ID): stable across reloads and restarts and distinct among
// duplicate rows, for an adapter that keeps state on a remote host to key it by. Compile
// has already rejected every mistake decidable from the plan alone, so an error is one
// only the execution technique knows (e.g. an option-like operand in a subprocess argv,
// or a method this platform cannot send); the service keeps the target as a rejected
// line and says why in a warning.
type PingerFactory func(plan probe.Plan, row string) (probe.Pinger, error)

// ConfigSource reads the monitored config. The composition root binds it to the config
// file, so the use cases never see where the config lives. Service.Open reads the
// initial config through it and Session.Reload rereads it; its error is the reason the
// program could not start, or the reason a reload failed.
type ConfigSource func(context.Context) (config.Config, error)

// HostCapabilities reports what this host can probe with. The startup warnings ask it,
// so that a probe that cannot work here is explained up front instead of failing as X
// with no packet sent.
type HostCapabilities interface {
	// Platform identifies this host's OS, so presentation can offer remedies that apply.
	Platform() probe.OS
	// DirectICMPAvailable reports whether a direct-ICMP socket opens: the raw socket or
	// the unprivileged datagram one.
	DirectICMPAvailable() bool
	// RawICMPAvailable reports whether the raw-socket path (root or CAP_NET_RAW) opens,
	// which a forced next-hop needs specifically.
	RawICMPAvailable() bool
	// RPFilterStrict reports whether strict reverse-path filtering is on, which can drop
	// the replies to forced IPv4 next-hop probes.
	RPFilterStrict() bool
}

// ResultLog records one probe result, arrived at now, by the reading it left its target
// at: its statistics say what the probe found (the state, and the RTT of a reply). The
// reading's ID is the row's stable identity (the same across reloads and restarts, and
// distinct among duplicate rows), for the adapter to key its log by. What a log line
// shows is the adapter's choice, so a change of log format does not reach this port.
// Session.Update calls it on the state owner (the frontend's update goroutine) with a
// detached reading, so Log must not block but may keep the reading. Rather than block, it
// may drop the line when its store cannot keep up; it then reports false, and the session
// tells the frontend how many lines the log lacks.
type ResultLog interface {
	Log(reading monitor.Reading, now time.Time) bool
}

// Wait blocks until d has passed or ctx has ended, reporting whether d passed. The session
// spaces its probes and rounds by it, so a test can run them without waiting in real time.
type Wait func(ctx context.Context, d time.Duration) bool

// Ports are the infrastructure the use cases run on. Log may be nil to record nothing, and
// Wait nil to wait in real time.
//
// Every literal of Ports names every port, so the assembly cannot leave a new one unwired.
//
//exhaustruct:enforce
type Ports struct {
	NewPinger  PingerFactory
	LoadConfig ConfigSource
	Host       HostCapabilities
	Log        ResultLog //exhaustruct:optional
	Wait       Wait      //exhaustruct:optional
}
