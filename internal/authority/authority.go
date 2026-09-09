package authority

import (
	"fmt"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/gate"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Request is one proposal put to the authority.
type Request struct {
	Actor  string
	Worker string // the worker type, empty when the coordinator proposes directly
	AsPM   bool   // the coordinating actor acting in its own name
	RunID  string
	From   State
	To     State
	Env    *envelope.Envelope
	Gate   *gate.Result
	Reason string
	Now    time.Time
}

// Facts are everything the authority needs that it cannot read off the
// request. They are gathered separately so that the decision itself is a pure
// function of stated inputs — which is what makes the rules testable without
// a database, and what stops a rule quietly growing a query of its own.
type Facts struct {
	Item ledger.Item
	// OpenBlockingQs are unanswered blocking questions on the item.
	OpenBlockingQs []ledger.Question
	// Approvals are the item's approval requests, newest first.
	Approvals []ledger.Approval
	// ImplementRunID is the run that last drove the item into verification.
	ImplementRunID string
	// DepStates maps each declared dependency to its current state.
	DepStates map[string]State
	// CommitReachable reports whether the commit the envelope names still exists
	// in the repository.
	CommitReachable bool
	// SpendDayMicros and SpendSegmentMicros are recorded spend so far.
	SpendDayMicros     int64
	SpendSegmentMicros int64
	// RunStarted reports whether the proposing run has a start recorded in the
	// ledger. A proposal from a run nobody registered has no prompt pin, no base
	// commit and no attribution behind it.
	RunStarted bool
	// MergeClean reports that the merge queue rebased the work onto the trunk,
	// re-ran the gate on the rebased tree, and found the branch changes only
	// files its own commits touch. Nothing else may set it: it is a statement
	// about a rebase that actually happened.
	MergeClean bool
}

// Decision is the authority's answer.
type Decision struct {
	Admitted bool
	Reason   Reason
	Detail   string
	Edge     *Edge
}

// Refusal renders a decision for a human and for the ledger.
func (d Decision) Refusal() string {
	if d.Admitted {
		return ""
	}
	return fmt.Sprintf("[%s] %s", d.Reason, d.Detail)
}

// Authority decides transitions against a project's declared policy.
type Authority struct{ cfg *config.Config }

// New builds an authority over a config.
func New(cfg *config.Config) *Authority { return &Authority{cfg: cfg} }

// Decide answers one proposal.
//
// The order of the checks matters and is not arbitrary. Identity questions
// come first — does this edge exist, may this proposer propose it, is the
// item actually in the state the proposal claims — because answering them
// later produces refusals that describe the wrong problem. A run working from
// a stale read of the item should be told that, not told its evidence is thin.
func (a *Authority) Decide(req Request, f Facts) Decision {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	edge := a.edgeFor(req)
	if edge == nil {
		return Decision{Reason: ReasonNoSuchEdge, Detail: fmt.Sprintf(
			"there is no %s -> %s edge in the transition table; run `adlc transition table` for the edges that exist", req.From, req.To)}
	}

	// The item's state now, not the state the proposal remembers. Concurrent runs
	// read, work for a while, and propose against a world that has moved.
	if State(f.Item.State) != req.From {
		return Decision{Edge: edge, Reason: ReasonStaleFromState, Detail: fmt.Sprintf(
			"the proposal says %s is in %s, but the ledger says %s — the proposing run worked from a stale read",
			f.Item.ID, req.From, f.Item.State)}
	}

	// A worker's proposal has to come from a run the control plane started.
	//
	// Without this, a proposal can be admitted carrying a run id nothing ever
	// registered — no prompt pin, no base commit, no attribution, and no row
	// for the never-run roll call to count. The audit chain would then have a
	// state change whose author cannot be established, which is the one thing an
	// append-only record cannot fix afterwards.
	if req.Worker != "" && !req.AsPM {
		if strings.TrimSpace(req.RunID) == "" {
			return Decision{Edge: edge, Reason: ReasonRunNotStarted, Detail: fmt.Sprintf(
				"a proposal from worker %q names no run; a state change nobody can attribute is not auditable", req.Worker)}
		}
		if !f.RunStarted {
			return Decision{Edge: edge, Reason: ReasonRunNotStarted, Detail: fmt.Sprintf(
				"run %s has no start recorded in the ledger, so this proposal has no prompt pin, no base commit and no attribution behind it. Record it with `adlc run start` before proposing", req.RunID)}
		}
	}

	prop, dec := a.proposerFor(req, edge)
	if !dec.Admitted {
		return dec
	}
	if !edge.Allows(prop) {
		return Decision{Edge: edge, Reason: ReasonWrongProposer, Detail: fmt.Sprintf(
			"%s -> %s may be proposed by %s; this proposal came from %s",
			req.From, req.To, joinProposers(edge.Proposers), prop)}
	}

	for _, r := range edge.Requires {
		if d := a.check(r, req, f, edge); !d.Admitted {
			d.Edge = edge
			return d
		}
	}
	return Decision{Admitted: true, Edge: edge}
}

// proposerFor resolves the capability a proposal is made under.
func (a *Authority) proposerFor(req Request, edge *Edge) (Proposer, Decision) {
	if req.AsPM {
		return ProposerPM, Decision{Admitted: true}
	}
	if req.Worker == "" {
		return ProposerControl, Decision{Admitted: true}
	}
	if a.cfg.Worker(req.Worker) == nil {
		return "", Decision{Edge: edge, Reason: ReasonUnknownWorker, Detail: fmt.Sprintf(
			"%q is not a declared worker type; an undeclared worker is also one the never-run roll call cannot see", req.Worker)}
	}
	// A worker proposes under whichever of its capabilities this edge admits.
	// Checking the edge first rather than picking a "primary" capability keeps a
	// multi-capability worker from being refused for holding more than one.
	for _, p := range edge.Proposers {
		switch p {
		case ProposerImplement, ProposerTest, ProposerJudge, ProposerValidate,
			ProposerCurate, ProposerArbitrate, ProposerOperate, ProposerImprove:
			if a.cfg.Can(req.Worker, string(p)) {
				return p, Decision{Admitted: true}
			}
		}
	}
	// No capability matched: report the strongest one it holds so the refusal
	// names what it actually is.
	for _, c := range []string{config.CapValidate, config.CapArbitrate, config.CapJudge,
		config.CapTest, config.CapCurate, config.CapImprove, config.CapOperate,
		config.CapImplement, config.CapPlan, config.CapResearch} {
		if a.cfg.Can(req.Worker, c) {
			return Proposer(c), Decision{Admitted: true}
		}
	}
	return Proposer(req.Worker), Decision{Admitted: true}
}

func (a *Authority) check(r Requirement, req Request, f Facts, edge *Edge) Decision {
	switch r {

	case ReqDepsSatisfied:
		var unmet []string
		for _, dep := range f.Item.DependsOn {
			st, known := f.DepStates[dep]
			if !known {
				unmet = append(unmet, dep+" (no such item)")
				continue
			}
			if st != StateDone {
				unmet = append(unmet, fmt.Sprintf("%s is %s", dep, st))
			}
		}
		if len(unmet) > 0 {
			return refuse(ReasonDependenciesUnmet, "this item waits on: "+strings.Join(unmet, ", "))
		}

	case ReqQuestionsAnswered:
		if n := len(f.OpenBlockingQs); n > 0 {
			return refuse(ReasonOpenQuestion, fmt.Sprintf(
				"%d blocking question(s) on this item are unanswered, starting with %s: %s",
				n, f.OpenBlockingQs[0].ID, firstLine(f.OpenBlockingQs[0].Text)))
		}

	case ReqMergeClean:
		if !f.MergeClean {
			return refuse(ReasonMergeConflict, "the merge queue has not established that this branch rebases onto the trunk, re-gates green on the rebased tree, and changes only files its own commits touch")
		}

	case ReqStatedReason:
		if strings.TrimSpace(req.Reason) == "" {
			return refuse(ReasonNoReason, fmt.Sprintf(
				"%s -> %s is a decision, and a decision with no stated reason is indistinguishable from a defect", req.From, req.To))
		}

	case ReqGateGreen:
		if req.Gate == nil {
			return refuse(ReasonGateNotRun, "the control plane did not run this edge's checks, and an unrun check is not a passed one")
		}
		if req.Gate.NoChecksDeclared {
			return refuse(ReasonZeroChecks, fmt.Sprintf(
				"no checks are declared for %s -> %s, so the gate observed nothing; a gate with nothing to run reports green over nothing", req.From, req.To))
		}
		if u := req.Gate.Unknown(); len(u) > 0 && req.Gate.Status != gate.StatusRed {
			return refuse(ReasonGateUnknown, fmt.Sprintf(
				"%d check(s) could not be run here — %s. Not run is not passed",
				len(u), u[0].CheckID+": "+u[0].Why))
		}
		if !req.Gate.Green() {
			var parts []string
			for _, c := range req.Gate.Failed() {
				parts = append(parts, c.CheckID+" ("+c.Why+")")
			}
			return refuse(ReasonGateFailed, "the control plane ran the checks itself and got: "+strings.Join(parts, "; "))
		}

	case ReqCommittedTree:
		if req.Gate == nil {
			return refuse(ReasonGateNotRun, "no gate observation, so nothing knows what tree the evidence describes")
		}
		if req.Gate.Dirty {
			return refuse(ReasonUncommittedTree, fmt.Sprintf(
				"the evidence was produced over a tree with uncommitted changes in the declared source roots, so it describes a state that has no commit and that nobody can check out again: %d path(s) at %s, starting with %s",
				len(req.Gate.DirtyPaths), shortSHA(req.Gate.TreeSHA), strings.Join(firstN(req.Gate.DirtyPaths, 5), ", ")))
		}

	case ReqCommitReachable:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope, so no commit is named")
		}
		if req.Env.HeadSHA == "" {
			return refuse(ReasonMissingEvidence, "the envelope names no commit; a state change with no commit behind it cannot be reviewed later")
		}
		if !f.CommitReachable {
			return refuse(ReasonCommitUnreachable, fmt.Sprintf(
				"commit %s is not reachable in this repository — a run's commit can be lost when its isolated workspace is removed, which leaves the item pointing at code that no longer exists",
				shortSHA(req.Env.HeadSHA)))
		}

	case ReqClaimsMatch:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope to compare the observation against")
		}
		if req.Gate == nil {
			return refuse(ReasonGateNotRun, "no observation to compare the envelope against")
		}
		issues := req.Gate.CompareClaims(a.cfg, req.Env)
		for _, is := range issues {
			if is.Kind == "discrepancy" {
				return refuse(ReasonClaimDiscrepancy, is.Detail)
			}
		}
		if len(issues) > 0 {
			return refuse(ReasonClaimOmission, issues[0].Detail)
		}

	case ReqVerdictPass:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope, so no verdict")
		}
		if req.Env.Verdict != "pass" {
			return refuse(ReasonMissingEvidence, fmt.Sprintf(
				"%s -> %s needs a passing verdict; this envelope says %q", req.From, req.To, req.Env.Verdict))
		}

	case ReqNoBlockerFindings:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope, so no findings")
		}
		if b := req.Env.Blockers(); len(b) > 0 {
			return refuse(ReasonMissingEvidence, fmt.Sprintf(
				"the envelope carries %d blocker finding(s) and still proposes %s; the first is at %s", len(b), req.To, b[0].Location))
		}

	case ReqBlockerFinding:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope, so no findings")
		}
		// Read through GroundsForRejection, not Blockers: the question a
		// rejection has to answer is whether it cited a substantive defect, and
		// "major" is one. Asking for "blocker" exactly discarded six real
		// rejections on a spelling.
		b := req.Env.GroundsForRejection()
		if len(b) == 0 {
			return refuse(ReasonMissingEvidence, fmt.Sprintf(
				"%s needs at least one finding weighted blocker or major; %d finding(s) here are minor or note, and rejecting on taste alone is not a rejection",
				req.To, len(req.Env.Outputs.Findings)))
		}
		for i, x := range b {
			if strings.TrimSpace(x.Location) == "" || strings.TrimSpace(x.Required) == "" {
				return refuse(ReasonMissingEvidence, fmt.Sprintf(
					"%s finding %d cites no location or names no required change; a defect that does not say what would clear it cannot be worked", x.Severity, i))
			}
		}

	case ReqBlockingQuestion:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope, so nothing states what the block is")
		}
		q, ok := req.Env.BlockingQuestion()
		if !ok && strings.TrimSpace(req.Env.EnvironmentFailure) == "" {
			return refuse(ReasonMissingEvidence,
				"blocked needs a blocking question or a declared environment failure; blocked is not an acceptable steady state and has to say what would clear it")
		}
		if ok && strings.TrimSpace(q.Lean) == "" {
			return refuse(ReasonMissingEvidence, fmt.Sprintf(
				"the blocking question %q carries no lean; a question with no recommendation hands back the analysis the run was dispatched to do", firstLine(q.Text)))
		}

	case ReqIndependentVerifier:
		if a.cfg.Can(req.Worker, config.CapImplement) &&
			!a.cfg.Can(req.Worker, config.CapTest) && !a.cfg.Can(req.Worker, config.CapJudge) {
			return refuse(ReasonSelfCertified, fmt.Sprintf(
				"%s implements; verification has to come from a worker that does not", req.Worker))
		}
		if f.ImplementRunID != "" && req.RunID == f.ImplementRunID {
			return refuse(ReasonSelfCertified, fmt.Sprintf(
				"run %s produced this work and is now judging it; authoring and judging your own work is the conflict of interest the separation exists to prevent", req.RunID))
		}

	case ReqAllCriteriaPass:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope, so no per-criterion verdicts")
		}
		if len(f.Item.Criteria) == 0 {
			return refuse(ReasonMissingEvidence,
				"this item declares no acceptance criteria, so there is nothing for a verifier to have verified")
		}
		got := req.Env.Criteria()
		if len(got) < len(f.Item.Criteria) {
			return refuse(ReasonMissingEvidence, fmt.Sprintf(
				"the item declares %d acceptance criteria and the envelope reports %d", len(f.Item.Criteria), len(got)))
		}
		for i, c := range got {
			if c.Status != "pass" {
				return refuse(ReasonMissingEvidence, fmt.Sprintf(
					"criterion %s is %q, not pass", labelFor(c.ID, i), c.Status))
			}
			if c.CommandIndex < 0 || c.CommandIndex >= len(req.Env.Commands) {
				return refuse(ReasonMissingEvidence, fmt.Sprintf(
					"criterion %s cites command index %d, which is not in commands_run; a criterion passed on inspection alone is not passed",
					labelFor(c.ID, i), c.CommandIndex))
			}
			if req.Env.Commands[c.CommandIndex].NotRun {
				return refuse(ReasonMissingEvidence, fmt.Sprintf(
					"criterion %s cites a command the envelope itself says was not run", labelFor(c.ID, i)))
			}
		}

	case ReqSomeCriterionFails:
		if req.Env == nil {
			return refuse(ReasonNoEnvelope, "no envelope, so no per-criterion verdicts")
		}
		failed := req.Env.FailedCriteria()
		if len(failed) == 0 {
			return refuse(ReasonMissingEvidence,
				"sending work back needs at least one criterion reported as failing, with the failing output captured")
		}
		for _, c := range failed {
			if strings.TrimSpace(c.Evidence) == "" {
				return refuse(ReasonMissingEvidence, fmt.Sprintf(
					"criterion %s is reported failing with no captured output", c.ID))
			}
		}

	case ReqRadiusNone, ReqRadiusAutoAppliable, ReqRadiusNeedsApproval:
		return a.checkRadius(r, f)

	case ReqPlanDigest:
		digest := f.Item.PlanDigest
		if req.Env != nil && req.Env.PlanDigest != "" {
			digest = req.Env.PlanDigest
		}
		if strings.TrimSpace(digest) == "" {
			return refuse(ReasonPlanDigestMissing, fmt.Sprintf(
				"a change with blast radius %q needs the digest of the dry run that describes it; without one, an approval approves a sentence and an apply performs something else", f.Item.Radius))
		}

	case ReqApprovalCurrent:
		return a.checkApproval(req, f)

	case ReqArtifactDigest:
		var art string
		if req.Env != nil {
			art = req.Env.Artifact
		}
		if art == "" && req.Gate != nil {
			art = req.Gate.Artifact
		}
		if strings.TrimSpace(art) == "" {
			return refuse(ReasonArtifactMissing,
				"this edge is judged on the assembled artifact, so its evidence has to name the artifact it ran against — an image id, a plan hash, a state fingerprint. A commit says what the source was and nothing about what was built from it")
		}

	case ReqAttemptsUnderMax:
		max := a.cfg.Dispatch.MaxAttempts
		if f.Item.Attempts >= max {
			return refuse(ReasonAttemptLimit, fmt.Sprintf(
				"%s has been reworked %d times against a limit of %d; past the limit it escalates to a human rather than looping", f.Item.ID, f.Item.Attempts, max))
		}
	}
	return Decision{Admitted: true}
}

func (a *Authority) checkRadius(r Requirement, f Facts) Decision {
	rad := config.Radius(f.Item.Radius)
	if !rad.Known() {
		return refuse(ReasonRadiusUndeclared, fmt.Sprintf(
			"%s declares blast radius %q, which is not one of %v; an unrecognised radius fails closed, because the one thing this policy exists to prevent is an unattended apply",
			f.Item.ID, f.Item.Radius, config.Radii()))
	}
	auto := a.cfg.Blast.AutoApplyMax
	switch r {
	case ReqRadiusNone:
		if rad != config.RadiusNone {
			return refuse(ReasonRadiusMismatch, fmt.Sprintf(
				"%s has blast radius %q, so it has an apply phase and cannot go straight to done", f.Item.ID, rad))
		}
	case ReqRadiusAutoAppliable:
		if rad == config.RadiusNone {
			return refuse(ReasonRadiusMismatch, fmt.Sprintf(
				"%s has blast radius none, so there is nothing to apply", f.Item.ID))
		}
		if rad.Rank() > auto.Rank() {
			return refuse(ReasonRadiusMismatch, fmt.Sprintf(
				"%s reaches %q and policy applies at most %q unattended", f.Item.ID, rad, auto))
		}
	case ReqRadiusNeedsApproval:
		if rad.Rank() <= auto.Rank() {
			return refuse(ReasonRadiusMismatch, fmt.Sprintf(
				"%s reaches only %q, which policy applies unattended; waiting for an approval nobody needs to give stalls it", f.Item.ID, rad))
		}
	}
	return Decision{Admitted: true}
}

func (a *Authority) checkApproval(req Request, f Facts) Decision {
	want := f.Item.PlanDigest
	if want == "" {
		return refuse(ReasonPlanDigestMissing, "the item carries no plan digest, so there is nothing an approval could have approved")
	}
	rad := config.Radius(f.Item.Radius)
	needNamed := rad.Rank() >= a.cfg.Blast.NamedApproverMin.Rank() && a.cfg.Blast.NamedApproverMin.Known()
	needTwo := rad.Rank() >= a.cfg.Blast.TwoApprovalsMin.Rank() && a.cfg.Blast.TwoApprovalsMin.Known()

	ttl := time.Duration(a.cfg.Blast.ApprovalTTLMinutes) * time.Minute
	approvers := map[string]bool{}
	var newest *ledger.Approval
	for i := range f.Approvals {
		ap := f.Approvals[i]
		if !ap.Decided() || ap.Verdict != "approve" {
			continue
		}
		if ap.PlanDigest != want {
			continue
		}
		if req.Now.Sub(time.UnixMilli(ap.DecidedMS)) > ttl {
			continue
		}
		if needNamed && strings.TrimSpace(ap.Approver) == "" {
			continue
		}
		if newest == nil {
			newest = &f.Approvals[i]
		}
		if ap.Approver != "" {
			approvers[ap.Approver] = true
		} else {
			approvers["(unnamed)"] = true
		}
	}
	if newest == nil {
		// Distinguish "nobody approved" from "the plan moved after approval". They
		// call for different actions and must not share a message.
		for _, ap := range f.Approvals {
			if ap.Decided() && ap.Verdict == "approve" && ap.PlanDigest != want {
				return refuse(ReasonApprovalStale, fmt.Sprintf(
					"the approval on record approved plan %s and the item's plan is now %s — approving one plan and applying another is exactly what this check exists to catch",
					shortSHA(ap.PlanDigest), shortSHA(want)))
			}
			if ap.Decided() && ap.Verdict == "approve" && req.Now.Sub(time.UnixMilli(ap.DecidedMS)) > ttl {
				return refuse(ReasonApprovalStale, fmt.Sprintf(
					"the approval for plan %s was given %s ago and policy expires one after %s",
					shortSHA(want), req.Now.Sub(time.UnixMilli(ap.DecidedMS)).Round(time.Minute), ttl))
			}
		}
		return refuse(ReasonApprovalMissing, fmt.Sprintf(
			"no current approval for plan %s on an item whose blast radius is %q", shortSHA(want), f.Item.Radius))
	}
	if needTwo && len(approvers) < 2 {
		return refuse(ReasonApprovalMissing, fmt.Sprintf(
			"blast radius %q requires two distinct approvers; %d has approved plan %s", f.Item.Radius, len(approvers), shortSHA(want)))
	}
	return Decision{Admitted: true}
}

func refuse(r Reason, detail string) Decision { return Decision{Reason: r, Detail: detail} }

func joinProposers(ps []Proposer) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return strings.Join(out, " or ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

func firstN(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return append(append([]string{}, v[:n]...), fmt.Sprintf("… and %d more", len(v)-n))
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(none)"
	}
	return s
}

func labelFor(id string, i int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("#%d", i+1)
}

// edgeFor picks the edge this request would actually travel.
//
// Several edges may share a (from, to) pair when one stage holds several
// independent tasks: in verification a tester, a judge and a validator each
// clear their own task and each carries its own requirements. Matching on the
// pair alone returned whichever happened to be declared first, so a judge was
// refused as the wrong proposer for an edge that was never its edge — and its
// own requirements were never reached at all.
//
// The first matching edge is still returned when nothing fits, so the refusal
// says wrong_proposer against a real edge rather than claiming the move does
// not exist.
func (a *Authority) edgeFor(req Request) *Edge {
	var first *Edge
	for _, e := range Table() {
		if e.From != req.From || e.To != req.To {
			continue
		}
		ec := e
		if first == nil {
			first = &ec
		}
		if req.AsPM {
			if ec.Allows(ProposerPM) {
				return &ec
			}
			continue
		}
		if req.Worker == "" {
			if ec.Allows(ProposerControl) {
				return &ec
			}
			continue
		}
		for _, p := range ec.Proposers {
			if a.cfg.Can(req.Worker, string(p)) {
				return &ec
			}
		}
	}
	return first
}
