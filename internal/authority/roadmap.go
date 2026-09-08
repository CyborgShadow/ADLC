package authority

import (
	"fmt"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// SegmentState is where a deliverable sits on the roadmap.
//
// Planning is its own pipeline because deciding what to build and building it
// are different jobs with different failure modes. A fleet that starts building
// the moment an idea is written down builds exactly what was written — including
// the parts that do not add up to the thing that was wanted, which nobody finds
// out until the deliverable is finished and wrong.
//
// So an idea is signed off before it is researched, researched before it is
// planned, and the plan is validated against the original intent before any of
// it becomes work.
type SegmentState string

const (
	// SegTheory is an idea. Nothing commits to it.
	SegTheory SegmentState = "theory"
	// SegRoadmap means it has been accepted onto the roadmap.
	SegRoadmap SegmentState = "roadmap"
	// SegSignedOff means a person agreed the intent is worth pursuing. This is
	// the only planning gate a machine never passes on its own.
	SegSignedOff SegmentState = "signed_off"
	// SegResearching means a researcher is turning the intent into an approach.
	SegResearching SegmentState = "researching"
	// SegResearched means the approach is written down.
	SegResearched SegmentState = "researched"
	// SegPlanning means a planner is decomposing the approach into work items.
	SegPlanning SegmentState = "planning"
	// SegPlanned means work items exist and nobody has checked that they add up.
	SegPlanned SegmentState = "planned"
	// SegValidating means a validator is checking the plan against the intent.
	SegValidating SegmentState = "validating"
	// SegReady means the plan was accepted. Only now may the work be dispatched.
	SegReady SegmentState = "ready"
	// SegBuilding means at least one item has moved.
	SegBuilding SegmentState = "building"
	// SegDelivered means every item reached a terminal state and one is done.
	SegDelivered SegmentState = "delivered"
	// SegPaused is an operator's decision, and only an operator's.
	SegPaused SegmentState = "paused"
)

// SegmentStages is the roadmap in reading order.
func SegmentStages() []SegmentState {
	return []SegmentState{
		SegTheory, SegRoadmap, SegSignedOff, SegResearching, SegResearched,
		SegPlanning, SegPlanned, SegValidating, SegReady, SegBuilding, SegDelivered,
	}
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

// OpenForWork reports whether the items in a deliverable may be dispatched.
//
// This is the handoff gate and the reason the planning pipeline earns its
// place: a plan nobody has checked against the intent does not become work. An
// item held here is skipped with a stated reason rather than silently ignored,
// because "not started because the plan is unreviewed" and "not started because
// nobody picked it" are different problems with different fixes.
func (s SegmentState) OpenForWork() bool {
	return s == SegReady || s == SegBuilding
}

// NeedsPerson reports whether the deliverable is waiting on a human decision.
func (s SegmentState) NeedsPerson() bool { return s == SegRoadmap }

// SegmentCapabilityFor says which capability moves a deliverable out of a
// state. An empty capability means it waits on a person, or on its own items.
func SegmentCapabilityFor(s SegmentState) (capability string, priority int) {
	switch s {
	case SegSignedOff, SegResearching:
		return config.CapResearch, 20
	case SegResearched, SegPlanning:
		return config.CapPlan, 21
	case SegPlanned, SegValidating:
		return config.CapValidate, 19
	}
	return "", 99
}

// SegmentPickedUp is the state a deliverable enters when a lane takes it.
func SegmentPickedUp(s SegmentState) (SegmentState, bool) {
	switch s {
	case SegSignedOff:
		return SegResearching, true
	case SegResearched:
		return SegPlanning, true
	case SegPlanned:
		return SegValidating, true
	}
	return s, false
}

// SegmentAdvance is a computed roadmap move.
type SegmentAdvance struct {
	To       SegmentState
	Why      string
	Inferred bool
}

// NextSegmentState computes where a deliverable goes next after a run.
//
// Like NextState for items, this is a pure function and the only thing that
// moves a deliverable on the strength of a run. Two edges are not here at all:
// theory to roadmap, and roadmap to signed off. Both are decisions a person
// makes, and no verdict from any agent produces them.
func NextSegmentState(from SegmentState, capability, verdict string, itemsCreated int) SegmentAdvance {
	pass := verdict == "pass"
	switch from {
	case SegSignedOff, SegResearching:
		if capability != config.CapResearch {
			return SegmentAdvance{}
		}
		if pass {
			return segOK(SegResearched, "the intent has been turned into a written approach")
		}
	case SegResearched, SegPlanning:
		if capability != config.CapPlan {
			return SegmentAdvance{}
		}
		if itemsCreated > 0 {
			return segOK(SegPlanned, fmt.Sprintf(
				"the approach was decomposed into %d work item(s), which nobody has checked against the intent yet", itemsCreated))
		}
		if pass {
			return SegmentAdvance{}
		}
	case SegPlanned, SegValidating:
		if capability != config.CapValidate {
			return SegmentAdvance{}
		}
		if pass {
			return segOK(SegReady, "the plan was checked against the intent and accepted; work may now be dispatched")
		}
		return segOK(SegResearched, "the plan does not add up to the intent and goes back for decomposition")
	}
	return SegmentAdvance{}
}

// SegmentFromItems recomputes the states that depend only on the items, so the
// roadmap is a projection of what has happened rather than a board somebody
// remembers to update.
func SegmentFromItems(from SegmentState, items []ledger.Item) SegmentAdvance {
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
	case SegReady:
		if moved > 0 {
			return segOK(SegBuilding, "work has started")
		}
	case SegBuilding:
		if total > 0 && terminal == total && done > 0 {
			return segOK(SegDelivered, fmt.Sprintf(
				"every one of the %d item(s) reached a terminal state, %d of them done", total, done))
		}
	}
	return SegmentAdvance{}
}

func segOK(to SegmentState, why string) SegmentAdvance {
	return SegmentAdvance{To: to, Why: why, Inferred: true}
}

// SegmentProgress is the rollup a person reads on the roadmap and progress
// pages. Every field is counted from the items, never stored.
type SegmentProgress struct {
	Total    int
	Done     int
	Active   int
	Waiting  int
	Blocked  int
	Approval int
	Rejected int
	Queued   int
	ByStage  map[string]int
}

// Percent is completion as a whole number, based on items DONE rather than
// items touched. A bar that fills as work is started reports that the fleet is
// busy, which is not the question anyone is asking.
func (p SegmentProgress) Percent() int {
	if p.Total == 0 {
		return 0
	}
	return p.Done * 100 / p.Total
}

// Progress counts a deliverable's items by stage.
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
			p.Approval++
		case st == StateQueued:
			p.Queued++
		case st.Waiting():
			p.Waiting++
		case st.Active():
			p.Active++
		}
	}
	return p
}

// Label is the stage in a person's words. The state name is what the record
// keeps; a surface that shows "signed_off" is showing its own internals.
func (s SegmentState) Label() string {
	switch s {
	case SegTheory:
		return "An idea"
	case SegRoadmap:
		return "On the roadmap"
	case SegSignedOff:
		return "Signed off"
	case SegResearching:
		return "Being researched"
	case SegResearched:
		return "Researched"
	case SegPlanning:
		return "Being planned"
	case SegPlanned:
		return "Planned"
	case SegValidating:
		return "Plan being checked"
	case SegReady:
		return "Ready to build"
	case SegBuilding:
		return "Being built"
	case SegDelivered:
		return "Delivered"
	case SegPaused:
		return "Paused"
	}
	return string(s)
}
