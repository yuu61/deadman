package ping

import "context"

// failedPinger always reports Failed. The TUI builds one (via AlwaysFail) for a target
// whose config could not be constructed — e.g. a missing required relay attribute or an
// option-like operand — so that one target shows a permanent failure glyph while
// monitoring of the others continues, instead of aborting the whole program.
type failedPinger struct{}

func (failedPinger) Send(context.Context) Result {
	return Result{Code: Failed, TTL: -1}
}

// AlwaysFail returns a Pinger that always reports Failed.
func AlwaysFail() Pinger { return failedPinger{} }
