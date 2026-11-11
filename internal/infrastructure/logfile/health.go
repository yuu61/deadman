package logfile

// forgetAfterPasses is how many passes over the rows a failed row's log may go unwritten
// before its failure is forgotten. Each row is written once per round, so a row still
// monitored has written again within one pass; one that has not after two is no longer
// monitored (a reload removed it) and can never recover.
const forgetAfterPasses = 2

// logHealth tracks whose log writes are failing now, so the writer reports a failure while
// it lasts rather than for the rest of the run. A row is failing from a failed write until
// its next write succeeds. It counts passes over the rows from the writes themselves: a row
// written a second time starts a new pass.
type logHealth struct {
	failing map[string]int // row ID → the pass of its failed write.
	seen    map[string]struct{}
	pass    int
	last    error // the most recent failure, reported while any row is failing.
}

func newLogHealth() logHealth {
	return logHealth{failing: make(map[string]int), seen: make(map[string]struct{})}
}

// record notes the outcome of writing row id's line.
func (h *logHealth) record(id string, err error) {
	if _, again := h.seen[id]; again {
		h.pass++
		clear(h.seen)
	}

	h.seen[id] = struct{}{}

	if err != nil {
		h.failing[id] = h.pass
		h.last = err
	} else {
		delete(h.failing, id)
	}

	for row, pass := range h.failing {
		if h.pass-pass >= forgetAfterPasses {
			delete(h.failing, row)
		}
	}
}

// current is the storage failure the writer is in: the most recent failure while any row's
// latest write failed, nil once every such row has written again.
func (h *logHealth) current() error {
	if len(h.failing) == 0 {
		return nil
	}

	return h.last
}
