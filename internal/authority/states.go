// Package authority decides whether a proposed state change is admissible.
//
// Workers propose; this package decides; the ledger records both answers. A
// refused proposal is appended to the record with its reason, because a system
// that only records what it accepted cannot answer the question it will
// actually be asked, which is what went wrong.
//
// Both of those systems assumed every action was undone by reverting a commit:
// a rejection routes back to in_progress, a bad merge is a revert, and the
// worst outcome of a wrong decision is wasted tokens. None of that holds when
// the artifact is a running machine. So the lifecycle splits proposing a
// change from making it, and puts an approval edge between them whose
// threshold is the change's blast radius.
package authority

import "fmt"

// State is a work item's lifecycle position.
type State string

// The lifecycle.
//
// queued and ready are separate because dependency readiness is COMPUTED: an
// operator has to be able to see "not started because its dependency is open"
// distinctly from "not started because nobody picked it".
//
// blocked is deliberately not a substate of the others. A blocked item has no
// running process — a blocked worker terminates and is later re-dispatched
// as a resume run carrying the answer — so it has to be visibly parked
// rather than invisibly waiting.
const (
	StateQueued           State = "queued"
	StateReady            State = "ready"
	StateInProgress       State = "in_progress"
	StateVerifying        State = "verifying"
	StateValidating       State = "validating"
	StateAwaitingApproval State = "awaiting_approval"
	StateApplying         State = "applying"
	StateConfirming       State = "confirming"
	StateDone             State = "done"
	StateBlocked          State = "blocked"
	StateRejected         State = "rejected"
	StateCancelled        State = "cancelled"
	StateSuperseded       State = "superseded"
)

// AllStates lists the lifecycle, in rough order of progress.
func AllStates() []State {
	return []State{
		StateQueued, StateReady, StateInProgress, StateVerifying, StateValidating,
		StateAwaitingApproval, StateApplying, StateConfirming, StateDone,
		StateBlocked, StateRejected, StateCancelled, StateSuperseded,
	}
}

// Known reports whether a state is one this build understands. An unknown
// state is reported as unknown rather than treated as terminal.
func (s State) Known() bool {
	for _, k := range AllStates() {
		if k == s {
			return true
		}
	}
	return false
}

// Terminal reports whether an item in this state is finished for good.
func (s State) Terminal() bool {
	return s == StateDone || s == StateCancelled || s == StateSuperseded
}

// Active reports whether a run may be dispatched against an item in this
// state.
func (s State) Active() bool {
	switch s {
	case StateReady, StateInProgress, StateVerifying, StateValidating, StateApplying, StateConfirming:
		return true
	}
	return false
}

// Proposer is who may propose an edge.
type Proposer string

const (
	// ProposerControl is the control plane computing something. No agent proposes
	// these; they are derived and recomputed.
	ProposerControl Proposer = "control"
	// ProposerPM is the coordinating actor — the only one that may route
	// rework, resume a blocked item, or send a done item back for review.
	ProposerPM Proposer = "pm"
	// The rest are worker capabilities, declared in config.
	ProposerImplement Proposer = "implement"
	ProposerVerify    Proposer = "verify"
	ProposerValidate  Proposer = "validate"
	ProposerOperate   Proposer = "operate"
)

// Requirement is a condition the authority checks before admitting an edge.
type Requirement string

const (
	// ReqGateGreen: the control plane ran the edge's declared checks ITSELF and
	// every one came back green. Not "the envelope says so".
	ReqGateGreen Requirement = "gate_green"
	// ReqCommittedTree: the evidence describes a tree that has a commit. Evidence
	// produced over uncommitted changes describes a state nobody can check out
	// again.
	ReqCommittedTree Requirement = "committed_tree"
	// ReqClaimsMatch: the envelope's account of the checks agrees with what the
	// gate observed.
	ReqClaimsMatch Requirement = "claims_match"
	// ReqCommitReachable: the commit the work names still exists. A run's commit
	// can become unreachable when its isolated workspace is removed, which leaves
	// a finished item pointing at code that is gone.
	ReqCommitReachable Requirement = "commit_reachable"
	// ReqAllCriteriaPass: every acceptance criterion is passed, each citing an
	// executed command. A criterion passed on inspection alone is not passed.
	ReqAllCriteriaPass Requirement = "all_criteria_pass"
	// ReqSomeCriterionFails: at least one criterion failed, with the failing
	// output captured — otherwise sending work back is unevidenced too.
	ReqSomeCriterionFails Requirement = "some_criterion_fails"
	// ReqIndependentVerifier: the run proposing this edge is not the run that did
	// the work, and does not hold the implement capability.
	ReqIndependentVerifier Requirement = "independent_verifier"
	// ReqVerdictPass: the worker's own verdict is pass.
	ReqVerdictPass Requirement = "verdict_pass"
	// ReqNoBlockerFindings: no blocker-severity finding is open on the item.
	ReqNoBlockerFindings Requirement = "no_blocker_findings"
	// ReqBlockerFinding: at least one blocker finding, with a location and the
	// smallest change that would clear it.
	ReqBlockerFinding Requirement = "blocker_finding"
	// ReqBlockingQuestion: a blocking question or a declared environment failure.
	// Blocking is a claim that has to be substantiated.
	ReqBlockingQuestion Requirement = "blocking_question"
	// ReqQuestionsAnswered: no blocking question on the item is still open.
	ReqQuestionsAnswered Requirement = "questions_answered"
	// ReqDepsSatisfied: every named dependency is done.
	ReqDepsSatisfied Requirement = "deps_satisfied"
	// ReqRadiusNone: the item changes nothing outside the source tree, so it has
	// no apply phase.
	ReqRadiusNone Requirement = "radius_none"
	// ReqRadiusAutoAppliable: the radius is at or below what policy allows
	// unattended.
	ReqRadiusAutoAppliable Requirement = "radius_auto_appliable"
	// ReqRadiusNeedsApproval: the radius is above it.
	ReqRadiusNeedsApproval Requirement = "radius_needs_approval"
	// ReqApprovalCurrent: an approval exists, is approved, names THIS plan
	// digest, and has not expired. Approving one plan and applying another is the
	// failure this prevents.
	ReqApprovalCurrent Requirement = "approval_current"
	// ReqPlanDigest: a dry-run digest exists to approve.
	ReqPlanDigest Requirement = "plan_digest"
	// ReqArtifactDigest: the behavioural evidence names what it ran against.
	ReqArtifactDigest Requirement = "artifact_digest"
	// ReqAttemptsUnderMax: rework has not exhausted its budget. Past the limit an
	// item escalates to a human instead of looping.
	ReqAttemptsUnderMax Requirement = "attempts_under_max"
	// ReqStatedReason: the proposer said why. Required on every edge a human
	// drives, because an unexplained override is indistinguishable from a bug.
	ReqStatedReason Requirement = "stated_reason"
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
//
// Read it as the specification it is: each row states who may propose the
// change and what has to be true for it to be admitted. Nothing else in this
// module is allowed to move an item.
func Table() []Edge {
	return []Edge{
		{StateQueued, StateReady, []Proposer{ProposerControl}, []Requirement{ReqDepsSatisfied},
			"dependencies are done; recomputed by the control plane, never proposed by an agent"},
		{StateReady, StateQueued, []Proposer{ProposerControl, ProposerPM}, []Requirement{ReqStatedReason},
			"a dependency regressed, or the coordinator deferred the item"},

		{StateReady, StateInProgress, []Proposer{ProposerPM, ProposerControl}, []Requirement{ReqDepsSatisfied, ReqQuestionsAnswered},
			"dispatch: a lease covering the item's files and resources is held before any work starts"},

		{StateInProgress, StateVerifying, []Proposer{ProposerImplement}, []Requirement{
			ReqGateGreen, ReqCommittedTree, ReqClaimsMatch, ReqCommitReachable},
			"implementation landed and the control plane's own run of the edge's checks was green"},

		{StateVerifying, StateValidating, []Proposer{ProposerVerify}, []Requirement{
			ReqIndependentVerifier, ReqAllCriteriaPass, ReqGateGreen, ReqClaimsMatch, ReqCommittedTree},
			"every acceptance criterion passed, each citing a command that was executed"},
		{StateVerifying, StateInProgress, []Proposer{ProposerVerify}, []Requirement{
			ReqIndependentVerifier, ReqSomeCriterionFails},
			"a criterion failed, with the failing output captured"},

		{StateValidating, StateDone, []Proposer{ProposerValidate}, []Requirement{
			ReqVerdictPass, ReqNoBlockerFindings, ReqRadiusNone, ReqCommitReachable, ReqQuestionsAnswered},
			"adversarial review passed and the item changes nothing outside the source tree"},
		{StateValidating, StateApplying, []Proposer{ProposerValidate}, []Requirement{
			ReqVerdictPass, ReqNoBlockerFindings, ReqRadiusAutoAppliable, ReqPlanDigest, ReqQuestionsAnswered},
			"review passed and the blast radius is within what policy allows unattended"},
		{StateValidating, StateAwaitingApproval, []Proposer{ProposerValidate}, []Requirement{
			ReqVerdictPass, ReqNoBlockerFindings, ReqRadiusNeedsApproval, ReqPlanDigest},
			"review passed but the change reaches further than policy applies unattended"},
		{StateValidating, StateRejected, []Proposer{ProposerValidate}, []Requirement{ReqBlockerFinding},
			"at least one blocker, citing a location and the smallest change that would clear it"},

		{StateAwaitingApproval, StateApplying, []Proposer{ProposerControl, ProposerPM}, []Requirement{
			ReqApprovalCurrent, ReqPlanDigest},
			"a human approved THIS plan digest, and it has not expired or moved since"},
		{StateAwaitingApproval, StateRejected, []Proposer{ProposerControl, ProposerPM}, []Requirement{ReqStatedReason},
			"the approval was declined"},
		{StateAwaitingApproval, StateBlocked, []Proposer{ProposerPM, ProposerControl}, []Requirement{ReqStatedReason},
			"the approval cannot be obtained; parked visibly rather than left pending"},

		{StateApplying, StateConfirming, []Proposer{ProposerOperate, ProposerImplement}, []Requirement{
			ReqGateGreen, ReqClaimsMatch, ReqArtifactDigest},
			"the change was applied and the artifact it produced is identified"},

		{StateConfirming, StateDone, []Proposer{ProposerValidate}, []Requirement{
			ReqVerdictPass, ReqNoBlockerFindings, ReqGateGreen, ReqClaimsMatch, ReqArtifactDigest, ReqQuestionsAnswered},
			"the behavioural checks ran against the applied artifact and passed — not a plan, the thing itself"},
		{StateConfirming, StateRejected, []Proposer{ProposerValidate, ProposerOperate}, []Requirement{ReqBlockerFinding},
			"the applied artifact does not do what the item said it would"},

		{StateRejected, StateInProgress, []Proposer{ProposerPM}, []Requirement{ReqAttemptsUnderMax, ReqStatedReason},
			"rework, routed by the coordinator; past the attempt limit it escalates instead"},
		{StateRejected, StateBlocked, []Proposer{ProposerPM}, []Requirement{ReqStatedReason},
			"the attempt limit is reached; a human decides what happens next"},
		{StateRejected, StateSuperseded, []Proposer{ProposerPM}, []Requirement{ReqStatedReason},
			"replaced by other items, named in the reason"},
		{StateRejected, StateCancelled, []Proposer{ProposerPM}, []Requirement{ReqStatedReason},
			"withdrawn"},

		{StateInProgress, StateBlocked, []Proposer{ProposerImplement, ProposerVerify, ProposerValidate, ProposerOperate, ProposerPM}, []Requirement{ReqBlockingQuestion},
			"a blocking question or a declared environment failure"},
		{StateVerifying, StateBlocked, []Proposer{ProposerVerify, ProposerPM}, []Requirement{ReqBlockingQuestion}, "same"},
		{StateValidating, StateBlocked, []Proposer{ProposerValidate, ProposerPM}, []Requirement{ReqBlockingQuestion}, "same"},
		{StateApplying, StateBlocked, []Proposer{ProposerOperate, ProposerImplement, ProposerPM}, []Requirement{ReqBlockingQuestion}, "same"},
		{StateConfirming, StateBlocked, []Proposer{ProposerValidate, ProposerOperate, ProposerPM}, []Requirement{ReqBlockingQuestion}, "same"},
		{StateReady, StateBlocked, []Proposer{ProposerPM, ProposerControl}, []Requirement{ReqStatedReason},
			"the environment is red before work started"},

		{StateBlocked, StateReady, []Proposer{ProposerPM}, []Requirement{ReqQuestionsAnswered, ReqStatedReason}, "resume before work started"},
		{StateBlocked, StateInProgress, []Proposer{ProposerPM}, []Requirement{ReqQuestionsAnswered, ReqStatedReason}, "resume at implementation"},
		{StateBlocked, StateVerifying, []Proposer{ProposerPM}, []Requirement{ReqQuestionsAnswered, ReqStatedReason}, "resume at verification"},
		{StateBlocked, StateValidating, []Proposer{ProposerPM}, []Requirement{ReqQuestionsAnswered, ReqStatedReason}, "resume at review"},
		{StateBlocked, StateAwaitingApproval, []Proposer{ProposerPM}, []Requirement{ReqQuestionsAnswered, ReqStatedReason}, "resume at approval"},
		{StateBlocked, StateCancelled, []Proposer{ProposerPM}, []Requirement{ReqStatedReason}, "a human closes it out"},

		{StateQueued, StateCancelled, []Proposer{ProposerPM}, []Requirement{ReqStatedReason}, "withdrawn before it started"},
		{StateQueued, StateSuperseded, []Proposer{ProposerPM}, []Requirement{ReqStatedReason}, "replaced before it started"},

		// A done item re-enters review by a coordinator's decision, never by a
		// reviewer's own hand. Re-entry needs an owner and a reason, and the verdict
		// then arrives on the edge out of validating where the evidence rules
		// already bite. Without this edge, a validation that finds a finished item
		// was never really finished has nowhere to put the finding — which is
		// exactly what happened when four items marked done by a backfill turned out
		// to carry seven blockers between them.
		{StateDone, StateValidating, []Proposer{ProposerPM}, []Requirement{ReqStatedReason},
			"a finished item is reopened for review, by decision, with a reason"},
	}
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

// Requires reports whether an edge carries a requirement.
func (e *Edge) requires(r Requirement) bool {
	for _, x := range e.Requires {
		if x == r {
			return true
		}
	}
	return false
}

func (e *Edge) String() string { return fmt.Sprintf("%s -> %s", e.From, e.To) }

// Reason is why a proposal was refused. Reasons are a closed vocabulary
// because they are read by agents as much as by people: a refusal an agent
// cannot classify is a refusal it will work around rather than fix.
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
)
