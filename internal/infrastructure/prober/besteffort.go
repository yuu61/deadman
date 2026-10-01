package prober

import (
	"context"
	"time"
)

// discard drops the error of a best-effort call on purpose: one whose failure leaves
// nothing to retry or report. Naming the drop keeps every one greppable, while errcheck
// still flags a `_ =` or a blank result written by accident.
func discard(error) {}

// interruptOnDone moves a connection's deadline to now when ctx ends, so a read or write
// parked past the old deadline returns at once. set is the connection's SetDeadline,
// SetReadDeadline or SetWriteDeadline; the returned stop cancels the interruption once
// the caller is done with the connection.
//
// set's error is dropped: it fails only on a connection that is already closed, where the
// parked call has returned with its own error anyway.
func interruptOnDone(ctx context.Context, set func(time.Time) error) func() bool {
	return context.AfterFunc(ctx, func() { discard(set(time.Now())) })
}
