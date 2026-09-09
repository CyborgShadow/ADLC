package authority

import (
	"fmt"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// Advance is what the control plane will do next with an item.
type Advance struct {
	To State
	// Why is the plain-language reason, recorded on the transition and shown
	// in the history. It is written for a person reading the record later.
	Why string
	// Inferred is false when the run reported something that moves nothing.
	Inferred bool
	// Stall explains why nothing moves, when nothing does.
	Stall string
}

// NextState computes the transition the control plane will attempt.
//
// This is the whole of "the tooling advances the status". It is a pure function
// of the state the item is in, the capability the run held, the verdict it
// reported, and the item's blast radius — and it is the ONLY thing that decides
// where an item goes next.
//
// An agent does not propose a transition and is not asked to. It is given a
// prompt, does a bounded job, and reports pass, fail, reject or blocked.
// Everything else is arithmetic. That removes a class of failure — an agent
// naming a state that does not exist, or one it may not take — and it is what
// makes a past decision re-derivable: a decision that depends on what an agent
// happened to write cannot be replayed at all.
func NextState(from State, capability, verdict string, radius config.Radius, policy config.BlastPolicy) Advance {
	// Blocked is available from anywhere live and outranks everything: a run
	// that says it is stuck has not done the work, whatever else it reported.
	if verdict == "blocked" && (from.Active() || from == StateReady) {
		return Advance{To: StateBlocked, Inferred: true,
			Why: "the run stopped on a blocking question or an environment failure"}
	}

	pass := verdict == "pass"
	setback := verdict == "fail" || verdict == "reject"

	switch from {
	case StateReady, StateInProgress:
		if capability != config.CapImplement {
			return stall(from, capability, "only a builder moves work out of "+string(from))
		}
		if pass {
			return ok(StateVerifying,
				"work and tests landed, and the control plane's own run of the checks was green")
		}
		return stall(from, capability, "the run did not finish; the item stays where it is and is re-dispatched")

	case StateReadyForTesting, StateTesting, StateVerifying:
		// One stage, three independent questions, asked at once.
		//
		// Testing, judging against the acceptance criteria and adversarial
		// review all examine the same commit. Running them in series cost three
		// cold starts and three waits to learn three things that never depended
		// on each other. The stage is still singular — this is one state, and
		// NextState is still a pure function of it — but a pass here does not
		// move the item. It clears one of the tasks. The item leaves when every
		// one of them has cleared, and that is computed by Refresh from the
		// record rather than proposed by whichever run happened to finish last.
		if !IsVerification(capability) {
			// Named as one role rather than three, because this sentence is what
			// a refused agent reads to work out what happened to it. A tester
			// told that verification is "the tester's, the judge's and the
			// validator's" concludes it was sent to the wrong item and goes
			// looking for a dispatch bug, when what is true is that its task no
			// longer exists.
			return stall(from, capability,
				"verification is the judge's alone; the gate and the acceptance criteria now answer what the tester and the validator were asked")
		}
		if pass {
			// A self-edge: decided like any other proposal, so every guard on it
			// still runs, and then the item does not move. It leaves the stage
			// when Refresh sees that every task has cleared.
			return ok(StateVerifying,
				"this task passed; the item leaves verification when every task in the stage has")
		}
		if setback {
			// A validator's REJECT is the one setback in this stage that moves
			// the item, and it moves it along the edge declared for exactly
			// that: verifying -> rejected, proposer validate, ReqBlockerFinding.
			//
			// Nothing ever routed to that edge. Every setback took the self-edge
			// below, and the validator's form of the self-edge requires
			// ReqVerdictPass — so the only role in the fleet whose job is to
			// stop a change was structurally incapable of stopping one. Its
			// rejections came back refused as malformed proposals: six of them,
			// each a completed adversarial review, spent and discarded. One of
			// those runs diagnosed this defect in the envelope that was thrown
			// away for reporting it.
			//
			// Only "reject" travels it, and only from the role the edge names as
			// its proposer. A "fail" stays on the self-edge for the reason given
			// just below; routing any other capability here would swap this
			// refusal for a wrong-proposer one and change nothing.
			//
			// That role is the judge now rather than the validator. When
			// verification held three tasks the validator was the adversarial
			// one and owned the rejection; the stage holds one task, the judge's,
			// and a stage whose only occupant cannot stop a change is a stage
			// that cannot stop anything.
			if verdict == "reject" && capability == config.CapJudge {
				return ok(StateRejected,
					"the judge rejected the change, citing at least one blocker")
			}
			// A self-edge, exactly like the pass above, and for the same
			// reason: this run cleared or failed ONE of three independent
			// questions asked of one commit. Ejecting the item on the first
			// failure refused its two in-flight siblings as stale — two runs
			// discarded — and handed the builder one complaint, so it learned
			// the other two a whole round later. The item leaves the stage
			// backward when every task has REPORTED, which Refresh computes
			// from the record.
			return ok(StateVerifying,
				"this task failed; the item leaves verification when every task in the stage has reported")
		}

	case StateReviewed, StateJanitoring:
		if capability != config.CapCurate {
			return stall(from, capability, "the hygiene pass belongs to the janitor")
		}
		if pass {
			return ok(StateReadyForArbitration, "the hygiene pass is complete")
		}
		if setback {
			return ok(StateInProgress, "the janitor found something wrong rather than untidy")
		}

	case StateReadyForArbitration, StateArbitrating:
		if capability != config.CapArbitrate {
			return stall(from, capability, "arbitration judges the change against the system, and is the arbiter's")
		}
		if setback {
			return ok(StateRejected, "the change conflicts with the system as a whole")
		}
		if pass {
			return afterArbitration(radius, policy)
		}

	case StateAwaitingApproval:
		// Nothing an agent reports moves this. It moves when a person decides,
		// and the control plane picks that up from the approval record.
		return stall(from, capability,
			"this item is waiting for a person to approve a specific plan; no agent can clear it")

	case StateApplying:
		if capability != config.CapOperate {
			return stall(from, capability, "only an operator performs an apply")
		}
		if pass {
			return ok(StateConfirming, "the change was applied and the artifact it produced is identified")
		}
		return ok(StateRejected, "the apply did not succeed")

	case StateConfirming:
		if capability != config.CapValidate {
			return stall(from, capability, "confirmation is a review of the applied artifact")
		}
		if pass {
			return ok(StateReadyToMerge,
				"the behavioural checks ran against the applied artifact and passed")
		}
		return ok(StateRejected, "the applied artifact does not do what the item said it would")

	case StateReadyToMerge, StateMerging:
		return stall(from, capability, "merging is the control plane's own step; no agent performs it")

	case StateMerged, StateImproving:
		if capability != config.CapImprove {
			return stall(from, capability, "the lessons pass belongs to the improver")
		}
		if pass {
			return ok(StateDone, "lessons recorded, and any self-improvements raised as their own items")
		}
		return stall(from, capability, "the improver did not finish; the item stays merged and is re-dispatched")
	}
	return stall(from, capability, fmt.Sprintf("nothing advances an item out of %s on a %q verdict", from, verdict))
}

// afterArbitration is the fork that exists because an approved change and an
// applied change are different events. Everything before it is ordinary
// software delivery; this is where the pipeline stops before touching
// something real.
func afterArbitration(radius config.Radius, policy config.BlastPolicy) Advance {
	if !radius.Known() {
		return Advance{Stall: fmt.Sprintf(
			"blast radius %q is not one this build knows, and an unrecognised radius fails closed", radius)}
	}
	switch {
	case radius == config.RadiusNone:
		return ok(StateReadyToMerge, "arbitration passed and the item changes nothing outside the source tree")
	case radius.Rank() <= policy.AutoApplyMax.Rank():
		return ok(StateApplying, fmt.Sprintf(
			"arbitration passed and a %s change is within what policy applies unattended", radius))
	default:
		return ok(StateAwaitingApproval, fmt.Sprintf(
			"arbitration passed, but a %s change reaches further than policy applies unattended — it waits for a named person to approve this exact plan", radius))
	}
}

func ok(to State, why string) Advance { return Advance{To: to, Why: why, Inferred: true} }

func stall(from State, capability, why string) Advance {
	return Advance{Stall: fmt.Sprintf("%s (item is in %s, run held %q)", why, from, capability)}
}

// CapabilityFor says which capability moves an item out of a state, and how
// urgently it should be picked up. A lower priority is dispatched first, so
// work nearest the finish line is drained before anything new is started.
//
// An empty capability means nothing can be dispatched against the state — it is
// terminal, blocked, waiting on a person, or the control plane's own step.
func CapabilityFor(s State) (capability string, priority int) {
	switch s {
	case StateMerged, StateImproving:
		return config.CapImprove, 0
	case StateConfirming:
		return config.CapValidate, 1
	case StateApplying:
		return config.CapOperate, 2
	case StateReadyForArbitration, StateArbitrating:
		return config.CapArbitrate, 3
	case StateReviewed, StateJanitoring:
		return config.CapCurate, 4
	case StateVerifying:
		// The stage's one task. This returned CapTest from when it held three
		// and this function reported the first outstanding one — which quietly
		// made `test` look like a capability the lifecycle still dispatches,
		// long after nothing did. `config check` then reported the tester as a
		// role with work and no lane to drain it, every run, over work that does
		// not exist; and a warning that fires on a deliberate choice is one
		// people learn to scroll past.
		return config.CapJudge, 5
	case StateInProgress:
		return config.CapImplement, 8
	case StateReady:
		return config.CapImplement, 9
	}
	return "", 99
}

// PickedUp is the state an item enters when a lane takes it out of a waiting
// state. Dispatching IS that transition: the moment a run holds the lease and
// has a workspace, the item is being worked on, and saying so immediately means
// the board shows the state the fleet is actually in.
func PickedUp(s State) (State, bool) {
	switch s {
	case StateReady:
		return StateInProgress, true
	case StateReadyForTesting:
		return StateTesting, true
	case StateReadyForReview:
		return StateJudging, true
	case StateReadyForValidation:
		return StateValidating, true
	case StateReviewed:
		return StateJanitoring, true
	case StateReadyForArbitration:
		return StateArbitrating, true
	case StateMerged:
		return StateImproving, true
	case StateReadyToMerge:
		// Picked up by the control plane rather than by a lane, but it is still
		// queue depth an operator needs to see.
		return StateMerging, true
	}
	return s, false
}

// Stage is a phase of the pipeline as a person reads it. The lifecycle has
// twenty-five states because the machine needs that many; somebody tracking a
// feature wants ten.
type Stage struct {
	Key   string
	Label string
	Order int
}

// Stages is the pipeline in reading order.
func Stages() []Stage {
	return []Stage{
		{"planning", "Planning", 0},
		{"in_progress", "In progress", 1},
		{"testing", "Testing", 2},
		{"review", "Review", 3},
		{"reviewed", "Reviewed", 4},
		{"approval", "Approval", 5},
		{"merge", "Merge", 6},
		{"improve", "Improve", 7},
		{"done", "Done", 8},
		{"stopped", "Stopped", 9},
	}
}

// StageOf maps a lifecycle state onto the pipeline a person reads.
func StageOf(s State) Stage {
	key := "stopped"
	switch s {
	case StateQueued, StateReady:
		key = "planning"
	case StateInProgress:
		key = "in_progress"
	case StateVerifying, StateReadyForTesting, StateTesting:
		key = "testing"
	case StateReadyForReview, StateJudging, StateReadyForValidation, StateValidating:
		key = "review"
	case StateReviewed, StateJanitoring, StateReadyForArbitration, StateArbitrating:
		key = "reviewed"
	case StateAwaitingApproval, StateApplying, StateConfirming:
		key = "approval"
	case StateReadyToMerge, StateMerging, StateMerged:
		key = "merge"
	case StateImproving:
		key = "improve"
	case StateDone:
		key = "done"
	}
	for _, st := range Stages() {
		if st.Key == key {
			return st
		}
	}
	return Stages()[0]
}

// DispatchesCapability reports whether any lifecycle state asks for this
// capability — either because an item in some state needs it, or because a
// segment on the roadmap does.
//
// It exists so that "no lane drains this" can tell the two cases apart. A
// capability the lifecycle never asks for strands nothing, however few lanes
// hold it: no item will ever reach a state that wants it. A capability some
// state DOES dispatch, with no lane behind it, stops a fleet dead. Reporting
// both as work sitting made the first fire on every run of `config check` for a
// role deliberately retired from the lifecycle, and an alarm that goes off on a
// choice somebody made on purpose is one that gets ignored when it is real.
func DispatchesCapability(capability string) bool {
	for _, s := range AllStates() {
		if c, _ := CapabilityFor(s); c == capability {
			return true
		}
	}
	for _, s := range SegmentStages() {
		if c, _ := SegmentCapabilityFor(s); c == capability {
			return true
		}
	}
	return IsVerification(capability)
}
