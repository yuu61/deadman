package monitoring

import (
	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/monitor"
)

// What a session reports to its frontend besides its table: what a config read loaded
// (Service.Open, and a ReloadOutcome), and what applying an event did (Session.Update).

// Loaded contains display directives and warnings from a config read. The session owns
// the table lines and supplies their detached snapshots through Table.
type Loaded struct {
	Display  config.Display
	Warnings []Diagnostic
}

// Transition describes the effects of applying an event on the state owner. It reports
// what happened to monitoring; how to show it (a spinner step, an arrow) is the
// frontend's call. Inflight comes whole when a round starts or a reload is applied; after
// that, a Recorded row is no longer in flight.
type Transition struct {
	Tasks          []Task         // work to run off the owner; feed each result back to Update.
	Recorded       *Recorded      // the row whose statistics changed, if one did.
	Probing        *int           // the row about to be probed, if one is.
	Inflight       []bool         // per table line, whether it is being probed; nil = unchanged.
	Reload         *ReloadOutcome // the latest reload request finished, if it did.
	RoundStarted   bool           // a new pass over the targets began.
	RoundCompleted bool           // every probe of the current pass has reported.
	// LogDropped is how many result lines the log has dropped over the session, reported
	// when the Recorded result was one of them; 0 when it was logged (or not logging).
	LogDropped int
	LogError   error // a storage failure reported by a background log writer.
}

// Recorded is a row a probe result just changed: its position in Table, and the detached
// reading the result left it at. A frontend holding the row's snapshot follows it by
// monitor.Snapshot.Advance.
type Recorded struct {
	Index   int // position in Session.Table(), including separators and rejected lines.
	Reading monitor.Reading
}

// ReloadOutcome is the result of the latest reload request. A superseded request, or
// one finishing after Close, produces none: its adapters are released instead.
type ReloadOutcome struct {
	// Loaded holds the reread config's display directives and warnings. A failed read
	// carries only its ReloadFailed warning, and the current rows stay monitored.
	Loaded

	// Applied reports that the reread rows replaced the monitored ones: every live
	// target with an unchanged identity kept its statistics, and the next generation's
	// first round is among the Transition's Tasks. Read Table again.
	Applied bool
}
