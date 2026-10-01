package monitor

import (
	"slices"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func consumed(id string, rtts ...float64) *Target {
	t := NewTarget(id, "name", "192.0.2.1")
	for _, rtt := range rtts {
		t.Consume(probe.SuccessResult(rtt))
	}

	return t
}

// A reload continues each live target whose ID is unchanged, and a changed ID starts
// fresh. The fresh target keeps its own name and address.
func TestCarryMatchesByID(t *testing.T) {
	kept, moved := consumed("kept#1", 5), consumed("moved#1", 5)
	freshKept := NewTarget("kept#1", "renamed", "198.51.100.1")
	freshMoved := NewTarget("moved-elsewhere#1", "moved", "192.0.2.2")

	fresh := []*Target{freshMoved, freshKept}
	Carry(fresh, []*Target{kept, moved})

	if fresh[0] != freshMoved || fresh[1] != freshKept {
		t.Fatal("Carry replaced its input targets")
	}

	if got := freshKept.Snapshot(); got.Snt != 1 || got.Name != "renamed" ||
		got.Addr != "198.51.100.1" {
		t.Errorf("continued target = %+v, want the live history under the fresh name", got)
	}

	if freshMoved.Snapshot().Snt != 0 {
		t.Error("a target whose ID changed continued a live one")
	}

	freshKept.Consume(probe.SuccessResult(9))

	if kept.Snapshot().Snt != 1 {
		t.Error("the continued target shares state with the live one")
	}
}

// Duplicate rows carry distinct IDs, so each keeps its own history across a reload.
// Should an ID repeat anyway, the live target still continues only once.
func TestCarryContinuesEachLiveTargetOnce(t *testing.T) {
	first, second := consumed("p#1", 10), consumed("p#2", 20, 30)
	fresh := []*Target{
		NewTarget("p#1", "p", "192.0.2.1"),
		NewTarget("p#2", "p", "192.0.2.1"),
		NewTarget("p#3", "p", "192.0.2.1"),
		NewTarget("p#1", "p", "192.0.2.1"),
	}

	Carry(fresh, []*Target{first, second})

	got := make([]int, len(fresh))
	for i, t := range fresh {
		got[i] = t.Snapshot().Snt
	}

	if got[0] != 1 || got[1] != 2 || got[2] != 0 || got[3] != 0 {
		t.Fatalf("send counts after carry = %v, want [1 2 0 0]", got)
	}
}

// direct is the compiled direct probe of addr.
func direct(t *testing.T, addr string) probe.Plan {
	t.Helper()

	plan, err := probe.Compile(probe.Spec{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}

	return plan
}

// A row's ID is its path and its occurrence among that path's rows. It names the row's
// log file, so its spelling is part of the contract.
func TestNumberingNumbersEachPathByOccurrence(t *testing.T) {
	a, b := direct(t, "192.0.2.1"), direct(t, "192.0.2.2")

	var n Numbering

	ids := []string{
		n.Next(a, "a", "192.0.2.1").Snapshot().ID,
		n.Next(b, "b", "192.0.2.2").Snapshot().ID,
		n.Next(a, "a again", "192.0.2.1").Snapshot().ID,
	}

	want := []string{
		`"direct":"192.0.2.1":"":"1":#1`,
		`"direct":"192.0.2.2":"":"1":#1`,
		`"direct":"192.0.2.1":"":"1":#2`,
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("IDs = %q, want %q", ids, want)
	}
}

// A fresh table numbers its rows the same way whatever they show and however they spell
// the address, so a reload or a restart finds each row under its old ID.
func TestNumberingIgnoresNamesAndSpellings(t *testing.T) {
	var before, after Numbering

	old := before.Next(direct(t, "192.0.2.1"), "old", "192.0.2.1")
	fresh := after.Next(direct(t, "::ffff:192.0.2.1"), "renamed", "::ffff:192.0.2.1")

	if old.Snapshot().ID != fresh.Snapshot().ID {
		t.Fatalf("IDs differ: %q, %q", old.Snapshot().ID, fresh.Snapshot().ID)
	}
}
