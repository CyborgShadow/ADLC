// Package authority decides whether a proposed state change is admissible.
//
// Workers propose; this package decides; the ledger records both answers. A
// refused proposal is appended too, because a system that records only what it
// accepted cannot answer the question it will actually be asked.
package authority

import "fmt"

// State is a work item's position in the delivery lifecycle.
type State string

// The lifecycle.
//
// Each stage that needs a person or an agent has two states: one where the item
// waits, and one where it is being worked. That split is what lets an operator
// see queue depth — "four items are waiting for review" is a different problem
// from "four items are in review" and calls for a different response.
//
// A "ready for X" state is entered by the control plane when the previous stage
// completes, and left when a lane picks the item up.
const (
	StateQueued State = "queued" // dependencies unmet
	StateReady  State = "ready"  // ready for work

	StateInProgress      State = "in_progress"       // work and tests being built
	StateReadyForTesting State = "ready_for_testing" // implementation landed
	StateTesting         State = "testing"           // tests being executed

	StateReadyForReview     State = "ready_for_review"     // tests green
	StateJudging            State = "judging"              // judged against acceptance criteria
	StateReadyForValidation State = "ready_for_validation" // judged, awaiting adversarial review
	StateValidating         State = "validating"           // adversarial review

	StateReviewed            State = "reviewed"              // review complete
	StateJanitoring          State = "janitoring"            // hygiene pass over what landed
	StateReadyForArbitration State = "ready_for_arbitration" // awaiting whole-system arbitration
	StateArbitrating         State = "arbitrating"           // judged against the system, not the item

	StateAwaitingApproval State = "awaiting_approval" // blast radius above the threshold
	StateApplying         State = "applying"          // applied to real resources
	StateConfirming       State = "confirming"        // the applied artifact is confirmed

	StateReadyToMerge State = "ready_to_merge" // cleared to land
	StateMerging      State = "merging"        // in the merge queue
	StateMerged       State = "merged"         // landed on the trunk

	StateImproving State = "improving" // lessons and self-improvements
	StateDone      State = "done"

	StateBlocked    State = "blocked"
	StateRejected   State = "rejected"
	StateCancelled  State = "cancelled"
	StateSuperseded State = "superseded"
)

// AllStates lists the lifecycle in order of progress.
func AllStates() []State {
	return []State{
		StateQueued, StateReady,
		StateInProgress, StateReadyForTesting, StateTesting,
		StateReadyForReview, StateJudging, StateReadyForValidation, StateValidating,
		StateReviewed, StateJanitoring, StateReadyForArbitration, StateArbitrating,
		StateAwaitingApproval, StateApplying, StateConfirming,
		StateReadyToMerge, StateMerging, StateMerged,
		StateImproving, StateDone,
		StateBlocked, StateRejected, StateCancelled, StateSuperseded,
	}
}

// Known reports whether a state is one this build understands.
func (s State) Known() bool {
	for _, k := range AllStates() {
		if k == s {
			return true
		}
	}
	return false
}

// Terminal reports whether an item is finished for good.
func (s State) Terminal() bool {
	return s == StateDone || s == StateCancelled || s == StateSuperseded
}

// Active reports whether the item is somewhere a run can move it.
func (s State) Active() bool {
	switch s {
	case StateQueued, StateDone, StateBlocked, StateRejected, StateCancelled,
		StateSuperseded, StateAwaitingApproval:
		return false
	}
	return true
}

// Waiting reports whether the item is parked awaiting a lane rather than being
// worked on right now. It is what the dashboard counts as queue depth.
func (s State) Waiting() bool {
	switch s {
	case StateReady, StateReadyForTesting, StateReadyForReview,
		StateReadyForValidation, StateReadyForArbitration, StateReadyToMerge:
		return true
	}
	return false
}

// Proposer is who may propose an edge.
type Proposer string

const (
	// ProposerControl is the control plane computing something. No agent
	// proposes these; they are derived and recomputed.
	ProposerControl Proposer = "control"
	// ProposerPM is the coordinating actor — the only one that may route
	// rework, resume a blocked item, or reopen finished work.
	ProposerPM Proposer = "pm"
	// The rest are worker capabilities, declared in config.
	ProposerImplement Proposer = "implement"
	ProposerTest      Proposer = "test"
	ProposerJudge     Proposer = "judge"
	ProposerValidate  Proposer = "validate"
	ProposerCurate    Proposer = "curate"
	ProposerArbitrate Proposer = "arbitrate"
	ProposerOperate   Proposer = "operate"
	ProposerImprove   Proposer = "improve"
)

// Requirement is a condition checked before an edge is admitted.
type Requirement string

const (
	// ReqGateGreen: the control plane ran the edge's declared checks ITSELF
	// and every one came back green.
	ReqGateGreen Requirement = "gate_green"
	// ReqCommittedTree: the evidence describes a tree that has a commit.
	ReqCommittedTree Requirement = "committed_tree"
	// ReqClaimsMatch: the envelope's account agrees with what the gate saw.
	ReqClaimsMatch Requirement = "claims_match"
	// ReqCommitReachable: the commit the work names still exists.
	ReqCommitReachable Requirement = "commit_reachable"
	// ReqAllCriteriaPass: every acceptance criterion passed, each citing an
	// executed command.
	ReqAllCriteriaPass Requirement = "all_criteria_pass"
	// ReqSomeCriterionFails: at least one criterion failed, with output.
	ReqSomeCriterionFails Requirement = "some_criterion_fails"
	// ReqIndependentVerifier: the proposing run is not the run that did the
	// work and does not hold the implement capability.
	ReqIndependentVerifier Requirement = "independent_verifier"
	// ReqVerdictPass: the worker's own verdict is pass.
	ReqVerdictPass Requirement = "verdict_pass"
	// ReqNoBlockerFindings: no blocker-severity finding is open.
	ReqNoBlockerFindings Requirement = "no_blocker_findings"
	// ReqBlockerFinding: at least one blocker, with a location and the
	// smallest change that would clear it.
	ReqBlockerFinding Requirement = "blocker_finding"
	// ReqBlockingQuestion: a blocking question or a declared environment
	// failure.
	ReqBlockingQuestion Requirement = "blocking_question"
	// ReqQuestionsAnswered: no blocking question on the item is still open.
	ReqQuestionsAnswered Requirement = "questions_answered"
	// ReqDepsSatisfied: every named dependency is done.
	ReqDepsSatisfied Requirement = "deps_satisfied"
	// ReqRadiusNone: the item changes nothing outside the source tree.
	ReqRadiusNone Requirement = "radius_none"
	// ReqRadiusAutoAppliable: the radius is within what policy applies
	// unattended.
	ReqRadiusAutoAppliable Requirement = "radius_auto_appliable"
	// ReqRadiusNeedsApproval: the radius is above it.
	ReqRadiusNeedsApproval Requirement = "radius_needs_approval"
	// ReqApprovalCurrent: an approval exists, is approved, names THIS plan
	// digest, and has not expired.
	ReqApprovalCurrent Requirement = "approval_current"
	// ReqPlanDigest: a dry-run digest exists to approve.
	ReqPlanDigest Requirement = "plan_digest"
	// ReqArtifactDigest: behavioural evidence names what it ran against.
	ReqArtifactDigest Requirement = "artifact_digest"
	// ReqAttemptsUnderMax: rework has not exhausted its budget.
	ReqAttemptsUnderMax Requirement = "attempts_under_max"
	// ReqStatedReason: the proposer said why.
	ReqStatedReason Requirement = "stated_reason"
	// ReqMergeClean: the merge queue rebased the work, re-ran the gate on the
	// rebased tree, and found the branch changes only files its own commits
	// touch.
	ReqMergeClean Requirement = "merge_clean"
)

// Edge is one admissible transition.
type Edge struct {
	From      State
	To        State
	Proposers []Proposer
	Requires  []Requirement
	Doc       string
}

// Table is the complete transition authority. Everything not on it is refused.
func Table() []Edge {
	e := func(from, to State, doc string, props []Proposer, reqs ...Requirement) Edge {
		return Edge{From: from, To: to, Proposers: props, Requires: reqs, Doc: doc}
	}
	control := []Proposer{ProposerControl}
	pm := []Proposer{ProposerPM}
	pmOrControl := []Proposer{ProposerControl, ProposerPM}

	out := []Edge{
		e(StateQueued, StateReady, "dependencies are done; computed, never proposed",
			control, ReqDepsSatisfied),
		e(StateReady, StateQueued, "a dependency regressed, or the coordinator deferred it",
			pmOrControl, ReqStatedReason),

		// --- build
		e(StateReady, StateInProgress, "a builder picked it up",
			pmOrControl, ReqDepsSatisfied, ReqQuestionsAnswered),
		e(StateInProgress, StateReadyForTesting, "work and tests landed, and the control plane's own run of the checks was green",
			[]Proposer{ProposerImplement}, ReqGateGreen, ReqCommittedTree, ReqClaimsMatch, ReqCommitReachable),

		// --- test
		e(StateReadyForTesting, StateTesting, "a tester picked it up", pmOrControl),
		e(StateTesting, StateReadyForReview, "the tests were executed and passed",
			[]Proposer{ProposerTest}, ReqIndependentVerifier, ReqGateGreen, ReqClaimsMatch, ReqCommittedTree),
		e(StateTesting, StateInProgress, "a test failed, with the failing output captured",
			[]Proposer{ProposerTest}, ReqIndependentVerifier, ReqSomeCriterionFails),

		// --- judge
		e(StateReadyForReview, StateJudging, "a judge picked it up", pmOrControl),
		e(StateJudging, StateReadyForValidation, "every acceptance criterion passed, each citing an executed command",
			[]Proposer{ProposerJudge}, ReqIndependentVerifier, ReqAllCriteriaPass, ReqGateGreen, ReqClaimsMatch),
		e(StateJudging, StateInProgress, "a criterion failed",
			[]Proposer{ProposerJudge}, ReqIndependentVerifier, ReqSomeCriterionFails),

		// --- validate
		e(StateReadyForValidation, StateValidating, "a validator picked it up", pmOrControl),
		e(StateValidating, StateReviewed, "adversarial review passed",
			[]Proposer{ProposerValidate}, ReqVerdictPass, ReqNoBlockerFindings, ReqCommitReachable, ReqQuestionsAnswered),
		e(StateValidating, StateRejected, "at least one blocker, citing a location and the smallest change that would clear it",
			[]Proposer{ProposerValidate}, ReqBlockerFinding),

		// --- janitor and arbiter watch the system rather than the item
		e(StateReviewed, StateJanitoring, "a janitor picked it up", pmOrControl),
		e(StateJanitoring, StateReadyForArbitration, "hygiene pass complete",
			[]Proposer{ProposerCurate}, ReqVerdictPass, ReqCommittedTree),
		e(StateJanitoring, StateInProgress, "the janitor found something that is wrong rather than untidy",
			[]Proposer{ProposerCurate}, ReqBlockerFinding),

		e(StateReadyForArbitration, StateArbitrating, "an arbiter picked it up", pmOrControl),
		e(StateArbitrating, StateReadyToMerge, "the change is coherent with the rest of the system",
			[]Proposer{ProposerArbitrate}, ReqVerdictPass, ReqNoBlockerFindings, ReqRadiusNone),
		e(StateArbitrating, StateAwaitingApproval, "coherent, but it reaches further than policy applies unattended",
			[]Proposer{ProposerArbitrate}, ReqVerdictPass, ReqNoBlockerFindings, ReqRadiusNeedsApproval, ReqPlanDigest),
		e(StateArbitrating, StateApplying, "coherent, and within what policy applies unattended",
			[]Proposer{ProposerArbitrate}, ReqVerdictPass, ReqNoBlockerFindings, ReqRadiusAutoAppliable, ReqPlanDigest),
		e(StateArbitrating, StateRejected, "the change conflicts with the system as a whole",
			[]Proposer{ProposerArbitrate}, ReqBlockerFinding),

		// --- the approval gate in front of anything irreversible
		e(StateAwaitingApproval, StateApplying, "a person approved THIS plan digest, and it has not expired or moved",
			pmOrControl, ReqApprovalCurrent, ReqPlanDigest),
		e(StateAwaitingApproval, StateRejected, "the approval was declined", pmOrControl, ReqStatedReason),
		e(StateAwaitingApproval, StateBlocked, "the approval cannot be obtained; parked visibly", pmOrControl, ReqStatedReason),

		e(StateApplying, StateConfirming, "the change was applied and the artifact it produced is identified",
			[]Proposer{ProposerOperate}, ReqGateGreen, ReqClaimsMatch, ReqArtifactDigest),
		e(StateConfirming, StateReadyToMerge, "the behavioural checks ran against the applied artifact and passed",
			[]Proposer{ProposerValidate}, ReqVerdictPass, ReqNoBlockerFindings, ReqGateGreen, ReqArtifactDigest),
		e(StateConfirming, StateRejected, "the applied artifact does not do what the item said it would",
			[]Proposer{ProposerValidate, ProposerOperate}, ReqBlockerFinding),

		// --- merge
		e(StateReadyToMerge, StateMerging, "the merge queue took it", control),
		e(StateMerging, StateMerged, "rebased onto the trunk, re-gated on the rebased tree, fast-forwarded",
			control, ReqMergeClean),
		e(StateMerging, StateReadyToMerge, "the merge could not complete; it goes back in the queue",
			control, ReqStatedReason),
		e(StateMerging, StateInProgress, "the rebased tree failed the gate, so the work goes back",
			control, ReqStatedReason),

		// --- learn
		e(StateMerged, StateImproving, "an improver picked it up", pmOrControl),
		e(StateImproving, StateDone, "lessons recorded and any self-improvements raised as their own items",
			[]Proposer{ProposerImprove}, ReqVerdictPass),
		e(StateMerged, StateDone, "nothing to learn from this one",
			pm, ReqStatedReason),

		// --- rework and escape hatches
		e(StateRejected, StateInProgress, "rework, routed by the coordinator",
			pm, ReqAttemptsUnderMax, ReqStatedReason),
		e(StateRejected, StateBlocked, "the attempt limit is reached; a person decides", pm, ReqStatedReason),
		e(StateRejected, StateSuperseded, "replaced by other items, named in the reason", pm, ReqStatedReason),
		e(StateRejected, StateCancelled, "withdrawn", pm, ReqStatedReason),
		e(StateQueued, StateCancelled, "withdrawn before it started", pm, ReqStatedReason),
		e(StateQueued, StateSuperseded, "replaced before it started", pm, ReqStatedReason),
		e(StateDone, StateReadyForValidation, "a finished item is reopened for review, by decision, with a reason",
			pm, ReqStatedReason),
	}

	// Anything live can block on a question, and the coordinator can resume it
	// wherever it stopped. Generated rather than written out, so a state added
	// above cannot quietly lose its escape hatch.
	for _, s := range AllStates() {
		if !s.Active() && s != StateReady {
			continue
		}
		out = append(out, e(s, StateBlocked,
			"a blocking question or a declared environment failure",
			[]Proposer{ProposerPM, ProposerControl, ProposerImplement, ProposerTest,
				ProposerJudge, ProposerValidate, ProposerCurate, ProposerArbitrate, ProposerOperate, ProposerImprove},
			ReqBlockingQuestion))
		out = append(out, e(StateBlocked, s, "resume once the question is answered",
			pm, ReqQuestionsAnswered, ReqStatedReason))
	}
	out = append(out, e(StateBlocked, StateCancelled, "a person closes it out", pm, ReqStatedReason))
	return out
}

// Find returns the edge for a transition, if one exists.
func Find(from, to State) *Edge {
	for _, e := range Table() {
		if e.From == from && e.To == to {
			ec := e
			return &ec
		}
	}
	return nil
}

// Allows reports whether a proposer may propose this edge.
func (e *Edge) Allows(p Proposer) bool {
	for _, x := range e.Proposers {
		if x == p {
			return true
		}
	}
	return false
}

func (e *Edge) String() string { return fmt.Sprintf("%s -> %s", e.From, e.To) }

// Reason is why a proposal was refused. A closed vocabulary, because refusals
// are read by agents as much as by people: a refusal an agent cannot classify
// is one it will work around rather than fix.
type Reason string

const (
	ReasonNoSuchEdge        Reason = "no_such_edge"
	ReasonWrongProposer     Reason = "wrong_proposer"
	ReasonStaleFromState    Reason = "stale_from_state"
	ReasonUnknownWorker     Reason = "unknown_worker"
	ReasonRunNotStarted     Reason = "run_not_started"
	ReasonNoEnvelope        Reason = "no_envelope"
	ReasonMalformedEnvelope Reason = "malformed_envelope"
	ReasonGateNotRun        Reason = "gate_not_run"
	ReasonZeroChecks        Reason = "zero_checks_declared"
	ReasonGateFailed        Reason = "gate_failed"
	ReasonGateUnknown       Reason = "gate_unknown"
	ReasonClaimDiscrepancy  Reason = "claim_discrepancy"
	ReasonClaimOmission     Reason = "claim_omission"
	ReasonUncommittedTree   Reason = "uncommitted_tree"
	ReasonCommitUnreachable Reason = "commit_unreachable"
	ReasonMissingEvidence   Reason = "missing_evidence"
	ReasonSelfCertified     Reason = "verification_self_certified"
	ReasonDependenciesUnmet Reason = "dependencies_unmet"
	ReasonOpenQuestion      Reason = "unanswered_blocking_question"
	ReasonRadiusUndeclared  Reason = "blast_radius_undeclared"
	ReasonRadiusMismatch    Reason = "blast_radius_mismatch"
	ReasonPlanDigestMissing Reason = "plan_digest_missing"
	ReasonApprovalMissing   Reason = "approval_missing"
	ReasonApprovalStale     Reason = "approval_stale"
	ReasonArtifactMissing   Reason = "artifact_digest_missing"
	ReasonAttemptLimit      Reason = "attempt_limit_reached"
	ReasonNoReason          Reason = "no_stated_reason"
	ReasonBudgetExhausted   Reason = "budget_exhausted"
	ReasonMergeConflict     Reason = "merge_conflict"
	ReasonStaleBaseClobber  Reason = "stale_base_clobber"
	ReasonMergeGateFailed   Reason = "merge_gate_failed"
	ReasonTrunkMoved        Reason = "trunk_moved"
)
