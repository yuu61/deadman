package logfile

import (
	"errors"
	"testing"
)

// A failure lasts until the row that failed writes again, however long it lasted.
func TestLogHealthClearsWhenFailedRowsWriteAgain(t *testing.T) {
	h := newLogHealth()
	full := errors.New("disk full")

	for range 3 {
		h.record("a", full)
		h.record("b", full)
	}

	h.record("a", nil)

	if !errors.Is(h.current(), full) {
		t.Fatalf("current = %v while b still fails, want %v", h.current(), full)
	}

	h.record("b", nil)

	if h.current() != nil {
		t.Fatalf("current = %v after every row wrote again, want nil", h.current())
	}
}

// One row's failing file keeps the failure steady while the other rows write fine.
func TestLogHealthHoldsOneRowsFailure(t *testing.T) {
	h := newLogHealth()
	denied := errors.New("permission denied")

	for range 5 {
		h.record("a", nil)
		h.record("b", denied)
		h.record("c", nil)

		if !errors.Is(h.current(), denied) {
			t.Fatalf("current = %v, want %v", h.current(), denied)
		}
	}
}

// A row that failed and then stopped being written (a reload removed it) is forgotten
// once the other rows have gone around without it.
func TestLogHealthForgetsRowsNoLongerWritten(t *testing.T) {
	h := newLogHealth()
	full := errors.New("disk full")

	h.record("gone", full)
	h.record("kept", full)

	for range forgetAfterPasses + 1 {
		h.record("kept", nil)
	}

	if h.current() != nil {
		t.Fatalf("current = %v after the failed row stopped, want nil", h.current())
	}
}
