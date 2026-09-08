package authority

import (
	"fmt"

	"github.com/CyborgShadow/adlc/internal/config"
)

// Advance is what the control plane will do next with an item.
type Advance struct {
	To State
	// Why is the plain-language reason, recorded on the transition and shown in
	// the history. It is written for a person reading the record later.
	Why string
	// Inferred is false when there is nothing to do — the run reported
	// something that does not move the item.
	Inferred bool
	// Stall explains why nothing moves, when nothing does.
	Stall string
}

// NextState computes the transition the control plane will attempt.
//
// This is the whole of "the tooling advances the status". It is a pure
// function of the state the item is in, the capability the run held, the
// verdict it reported, and the item's blast radius — and it is the ONLY
// thing that decides where an item goes next.
//
// An agent does not propose a transition and is not asked to. It is given a
// prompt, it does a bounded job, and it reports a verdict: pass, fail, reject
// or blocked. Everything else is arithmetic the control plane does. That
// division matters for two reasons beyond tidiness.
//
// It removes a whole class of failure. An agent that has to name its own next
// state can name the wrong one, name one that does not exist, or name one it
// is not allowed to take — and every one of those is a refused run, which
// costs as much as a wrong one.
//
// And it makes the pipeline reproducible. Given the same recorded inputs this
// returns the same answer forever, so `adlc run replay` can re-derive a past
// decision and prove it would be made the same way today. A decision that
// depends on what an agent happened to write cannot be re-derived at all.
func NextState(from State, capability, verdict string, radius config.Radius, policy config.BlastPolicy) Advance {
	// Blocked is available from any live state and outranks everything: a run
	// that says it is stuck on a question has not done the work, whatever else it
	// reported.
	if verdict == "blocked" && from.Active() {
		return Advance{To: StateBlocked, Inferred: true,
			Why: "the run stopped on a blocking question or an environment failure"}
	}

	switch from {
	case StateReady, StateInProgress:
		if capability != config.CapImplement {
			return stall(from, capability, "only a worker that implements can move work out of "+string(from))
		}
		switch verdict {
		case "pass":
			return Advance{To: StateVerifying, Inferred: true,
				Why: "the implementation landed and the control plane's own run of the checks was green"}
		case "fail":
			return stall(from, capability, "the run reported it did not finish; the item stays where it is and is re-dispatched")
		}

	case StateVerifying:
		if capability != config.CapVerify {
			return stall(from, capability, "verification has to come from a worker that does not implement")
		}
		switch verdict {
		case "pass":
			return Advance{To: StateValidating, Inferred: true,
				Why: "every acceptance criterion passed, each citing a command that was executed"}
		case "fail", "reject":
			return Advance{To: StateInProgress, Inferred: true,
				Why: "a criterion failed, so the work goes back with the failing output attached"}
		}

	case StateValidating:
		if capability != config.CapValidate {
			return stall(from, capability, "only an adversarial reviewer decides what happens after verification")
		}
		switch verdict {
		case "reject", "fail":
			return Advance{To: StateRejected, Inferred: true,
				Why: "review found at least one blocker"}
		case "pass":
			return afterReview(radius, policy)
		}

	case StateAwaitingApproval:
		// Nothing an agent reports moves this. It moves when a person decides, and
		// the control plane picks it up from the approval record.
		return stall(from, capability, "this item is waiting for a person to approve a specific plan; no agent can clear it")

	case StateApplying:
		if capability != config.CapOperate {
			return stall(from, capability, "only an operator performs an apply")
		}
		if verdict == "pass" {
			return Advance{To: StateConfirming, Inferred: true,
				Why: "the change was applied and the artifact it produced is identified"}
		}
		return Advance{To: StateRejected, Inferred: true, Why: "the apply did not succeed"}

	case StateConfirming:
		if capability != config.CapValidate {
			return stall(from, capability, "confirmation is a review of the applied artifact, not of the plan")
		}
		if verdict == "pass" {
			return Advance{To: StateDone, Inferred: true,
				Why: "the behavioural checks ran against the applied artifact and passed"}
		}
		return Advance{To: StateRejected, Inferred: true,
			Why: "the applied artifact does not do what the item said it would"}
	}
	return stall(from, capability, fmt.Sprintf("nothing advances an item out of %s on a %q verdict", from, verdict))
}

// afterReview is the fork that exists because an approved change and an
// applied change are different events. Everything above this line is ordinary
// software delivery; this is the part that stops before touching something
// real.
func afterReview(radius config.Radius, policy config.BlastPolicy) Advance {
	if !radius.Known() {
		return Advance{Stall: fmt.Sprintf(
			"blast radius %q is not one this build knows, and an unrecognised radius fails closed rather than applying", radius)}
	}
	switch {
	case radius == config.RadiusNone:
		return Advance{To: StateDone, Inferred: true,
			Why: "review passed and the item changes nothing outside the source tree"}
	case radius.Rank() <= policy.AutoApplyMax.Rank():
		return Advance{To: StateApplying, Inferred: true,
			Why: fmt.Sprintf("review passed and a %s change is within what policy applies unattended", radius)}
	default:
		return Advance{To: StateAwaitingApproval, Inferred: true,
			Why: fmt.Sprintf("review passed, but a %s change reaches further than policy applies unattended — it waits for a named person to approve this exact plan", radius)}
	}
}

func stall(from State, capability, why string) Advance {
	return Advance{Stall: fmt.Sprintf("%s (item is in %s, run held %q)", why, from, capability)}
}

// Stage is a human-facing pipeline position. The lifecycle has thirteen states
// because the machine needs that many; a person tracking a feature wants six.
type Stage struct {
	Key   string
	Label string
	Order int
}

// Stages is the pipeline as a person reads it, in order.
func Stages() []Stage {
	return []Stage{
		{"idea", "Idea", 0},
		{"building", "Building", 1},
		{"testing", "Testing", 2},
		{"judging", "Judging", 3},
		{"approval", "Approval", 4},
		{"applying", "Applying", 5},
		{"done", "Done", 6},
		{"stopped", "Stopped", 7},
	}
}

// StageOf maps a lifecycle state onto the pipeline a person reads.
func StageOf(s State) Stage {
	all := Stages()
	at := func(k string) Stage {
		for _, x := range all {
			if x.Key == k {
				return x
			}
		}
		return all[0]
	}
	switch s {
	case StateQueued, StateReady:
		return at("idea")
	case StateInProgress:
		return at("building")
	case StateVerifying:
		return at("testing")
	case StateValidating, StateConfirming:
		return at("judging")
	case StateAwaitingApproval:
		return at("approval")
	case StateApplying:
		return at("applying")
	case StateDone:
		return at("done")
	}
	return at("stopped")
}
