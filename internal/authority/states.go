// Package authority decides whether a proposed state change is admissible.
//
// Workers propose; this package decides; the ledger records both answers. A
// refused proposal is appended too, because a system that records only what it
// accepted cannot answer the question it will actually be asked.
package authority

import (
	"fmt"

	"github.com/CyborgShadow/ADLC/internal/config"
)

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

	StateInProgress State = "in_progress" // work and tests being built
	// The six states below are LEGACY. Verification used to be six states in
	// series; it is one stage now, and nothing enters these. They are kept so a
	// ledger written before the change still parses, and Refresh moves any item
	// found in one into StateVerifying rather than leaving it stranded.
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
		StateInProgress, StateVerifying,
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
		e(StateInProgress, StateVerifying, "work and tests landed, and the control plane's own run of the checks was green",
			[]Proposer{ProposerImplement}, ReqGateGreen, ReqCommittedTree, ReqClaimsMatch, ReqCommitReachable),

		// --- verification: one stage, three tasks, run together
		//
		// A pass here clears one task and moves nothing. The edge OUT is the
		// control plane's, because "every task has cleared" is arithmetic on the
		// record and letting the last run to finish propose it would make the
		// item's state depend on a race between three runs that all passed.
		// A verification task that PASSES is still a proposal the authority
		// decides — it simply does not move the item.
		//
		// These self-edges exist because the guards live on edges. When a pass
		// stopped proposing a transition, ReqIndependentVerifier and
		// ReqAllCriteriaPass stopped running with it, and a judge could have
		// cleared its own work by reporting a pass with no cited command. The
		// stage collapsing into one state must not quietly collapse the checks
		// on the way through it.
		e(StateVerifying, StateVerifying, "the tests were executed and passed; this task is cleared and the item waits on the rest of the stage",
			[]Proposer{ProposerTest}, ReqIndependentVerifier, ReqGateGreen, ReqClaimsMatch, ReqCommittedTree),
		e(StateVerifying, StateVerifying, "every acceptance criterion passed, each citing an executed command; this task is cleared",
			[]Proposer{ProposerJudge}, ReqIndependentVerifier, ReqAllCriteriaPass, ReqGateGreen, ReqClaimsMatch),
		e(StateVerifying, StateVerifying, "adversarial review found no blocker; this task is cleared",
			[]Proposer{ProposerValidate}, ReqIndependentVerifier, ReqVerdictPass, ReqNoBlockerFindings, ReqCommitReachable, ReqQuestionsAnswered),

		e(StateVerifying, StateReviewed, "every task in the stage passed: the tests were executed, every acceptance criterion held, and adversarial review found no blocker",
			[]Proposer{ProposerControl}, ReqCommitReachable, ReqQuestionsAnswered),
		e(StateVerifying, StateInProgress, "a verification task failed, and the work goes back with what it observed",
			[]Proposer{ProposerTest, ProposerJudge}, ReqIndependentVerifier, ReqSomeCriterionFails),
		e(StateVerifying, StateRejected, "at least one blocker, citing a location and the smallest change that would clear it",
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
		e(StateDone, StateVerifying, "a finished item is reopened for review, by decision, with a reason",
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
// Find returns the edge for a proposed move.
//
// Several edges may share a (from, to) pair when one stage holds several
// independent tasks: a tester, a judge and a validator each clear their own
// task in verification, and each carries its own requirements. Matching on the
// pair alone returned whichever was declared first, so every guard except that
// one stopped running — a judge was refused as the wrong proposer for an edge
// that was never its edge, and its own requirements were never reached.
//
// So the proposer is part of the lookup when one is offered. Find with an empty
// proposer still returns the first match, which is what the table printer and
// the reachability guard want.
func Find(from, to State) *Edge { return FindFor(from, to, "") }

// FindFor returns the edge this proposer would travel, or the first edge for
// the pair when no proposer is named.
func FindFor(from, to State, p Proposer) *Edge {
	var first *Edge
	for _, e := range Table() {
		if e.From != from || e.To != to {
			continue
		}
		ec := e
		if first == nil {
			first = &ec
		}
		if p == "" {
			return first
		}
		if ec.Allows(p) {
			return &ec
		}
	}
	// No edge this proposer may travel. Returning the first still lets the
	// caller report wrong_proposer against a real edge rather than claiming the
	// move does not exist at all.
	return first
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

// StateVerifying is the one stage whose tasks run together.
//
// Testing, judging against the acceptance criteria and adversarial review all
// examine the same commit and never depended on each other. As three states in
// series they cost three cold starts and three waits to answer three
// independent questions, and an item spent forty minutes in a stage whose work
// takes fifteen.
//
// The state stays singular, which is what keeps NextState a pure function of
// it. What is plural is the set of verdicts the stage requires: a pass clears
// one task and moves nothing, and the item leaves when every task has cleared —
// computed by the control plane from the record, never proposed by whichever
// run happened to finish last.
const StateVerifying State = "verifying"

// VerificationCapabilities are the tasks that must all pass before an item
// leaves verification.
//
// Declared here rather than in the config because it is the shape of the
// lifecycle rather than a policy knob: a project that wants a different set
// wants a different lifecycle, and should say so by editing this and the table
// together.
func VerificationCapabilities() []string {
	return []string{config.CapTest, config.CapJudge, config.CapValidate}
}

// IsVerification reports whether a capability clears a verification task.
func IsVerification(capability string) bool {
	for _, c := range VerificationCapabilities() {
		if c == capability {
			return true
		}
	}
	return false
}

// VerificationComplete reports whether every task in the stage has cleared.
func VerificationComplete(passed map[string]bool) bool {
	for _, c := range VerificationCapabilities() {
		if !passed[c] {
			return false
		}
	}
	return true
}

// VerificationOutstanding names the tasks that have not cleared yet, in a
// stable order, so a dispatcher offers them deterministically.
func VerificationOutstanding(passed map[string]bool) []string {
	var out []string
	for _, c := range VerificationCapabilities() {
		if !passed[c] {
			out = append(out, c)
		}
	}
	return out
}
