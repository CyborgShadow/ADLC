package authority

import (
	"fmt"

	"github.com/CyborgShadow/adlc/internal/ledger"
)

// SegmentState is where a deliverable sits on the roadmap.
//
// This layer exists because "decompose a goal into work" and "do the work" are
// different jobs with different failure modes, and running them together hides
// the more expensive one. A fleet that starts building the moment a brief is
// decomposed will build exactly what the decomposition said — including the
// parts that do not add up to the thing that was asked for. Nobody finds out
// until the deliverable is finished and wrong.
//
// So a breakdown is reviewed before any of it is built, by somebody who did
// not write it, against the brief rather than against the items.
type SegmentState string

const (
	// SegDrafted is a deliverable somebody has described and nobody has broken
	// down yet.
	SegDrafted SegmentState = "drafted"
	// SegResearching means a generator is decomposing the brief.
	SegResearching SegmentState = "researching"
	// SegPlanned means work items exist and nobody has checked that they add up
	// to the brief.
	SegPlanned SegmentState = "planned"
	// SegApproved means a reviewer confirmed the breakdown would deliver the
	// brief. Only now may the work itself be dispatched.
	SegApproved SegmentState = "approved"
	// SegBuilding means at least one item has moved.
	SegBuilding SegmentState = "building"
	// SegDelivered means every item reached a terminal state and at least one of
	// them is done.
	SegDelivered SegmentState = "delivered"
	// SegPaused is an operator's decision, and only an operator's.
	SegPaused SegmentState = "paused"
)

// SegmentStages is the roadmap in reading order.
func SegmentStages() []SegmentState {
	return []SegmentState{SegDrafted, SegResearching, SegPlanned, SegApproved, SegBuilding, SegDelivered}
}

// OpenForWork reports whether the items in a segment may be dispatched.
//
// This is the handoff gate, and it is the whole reason the roadmap layer earns
// its place: a breakdown nobody has reviewed does not become work. An item in
// a segment that has not been approved is skipped with a stated reason rather
// than silently ignored, because "not started because the plan is unreviewed"
// and "not started because nobody picked it" are different problems with
// different fixes.
func (s SegmentState) OpenForWork() bool {
	return s == SegApproved || s == SegBuilding
}

// Known reports whether this build understands the state.
func (s SegmentState) Known() bool {
	for _, x := range append(SegmentStages(), SegPaused) {
		if x == s {
			return true
		}
	}
	return false
}

// SegmentAdvance is a computed roadmap move.
type SegmentAdvance struct {
	To       SegmentState
	Why      string
	Inferred bool
}

// NextSegmentState computes where a deliverable goes next.
//
// Like NextState for items, this is a pure function and the only thing that
// moves a segment. It is called after every event that could change the
// answer, so the roadmap is a projection of what has actually happened rather
// than a board somebody remembers to update.
func NextSegmentState(from SegmentState, items []ledger.Item, planReview string) SegmentAdvance {
	var total, terminal, done, moved int
	for _, it := range items {
		total++
		st := State(it.State)
		if st.Terminal() {
			terminal++
		}
		if st == StateDone {
			done++
		}
		if st != StateQueued && st != StateReady {
			moved++
		}
	}

	switch from {
	case SegDrafted, SegResearching:
		if total > 0 {
			return SegmentAdvance{To: SegPlanned, Inferred: true, Why: fmt.Sprintf(
				"the brief was decomposed into %d work item(s), which nobody has checked against it yet", total)}
		}
	case SegPlanned:
		switch planReview {
		case "pass":
			return SegmentAdvance{To: SegApproved, Inferred: true,
				Why: "a reviewer confirmed the breakdown would deliver the brief; the work may now be dispatched"}
		case "reject":
			return SegmentAdvance{To: SegResearching, Inferred: true,
				Why: "the breakdown does not add up to the brief and goes back for decomposition"}
		}
	case SegApproved:
		if moved > 0 {
			return SegmentAdvance{To: SegBuilding, Inferred: true, Why: "work has started"}
		}
	case SegBuilding:
		if total > 0 && terminal == total && done > 0 {
			return SegmentAdvance{To: SegDelivered, Inferred: true, Why: fmt.Sprintf(
				"every one of the %d item(s) reached a terminal state, %d of them done", total, done)}
		}
	}
	return SegmentAdvance{}
}

// SegmentProgress is the rollup a person reads on the roadmap and progress
// pages. Every field is counted from the items, never stored.
type SegmentProgress struct {
	Total    int
	Done     int
	Active   int
	Blocked  int
	Waiting  int
	Rejected int
	Queued   int
	ByStage  map[string]int
}

// Percent is completion as a whole number, and it is deliberately based on
// items DONE rather than on items touched. A bar that fills as work is started
// tells an operator the fleet is busy, which they can already see; the
// question they are actually asking is how much of this is finished.
func (p SegmentProgress) Percent() int {
	if p.Total == 0 {
		return 0
	}
	return p.Done * 100 / p.Total
}

// Progress counts a segment's items by stage.
func Progress(items []ledger.Item) SegmentProgress {
	p := SegmentProgress{ByStage: map[string]int{}}
	for _, it := range items {
		st := State(it.State)
		p.Total++
		p.ByStage[StageOf(st).Key]++
		switch {
		case st == StateDone:
			p.Done++
		case st == StateBlocked:
			p.Blocked++
		case st == StateRejected:
			p.Rejected++
		case st == StateAwaitingApproval:
			p.Waiting++
		case st == StateQueued || st == StateReady:
			p.Queued++
		case st.Active():
			p.Active++
		}
	}
	return p
}
