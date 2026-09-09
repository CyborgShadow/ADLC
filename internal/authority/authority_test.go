package authority

import (
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/gate"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

var now = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func cfg(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.FromChecks("t", []string{"."}, []config.Check{{
		ID: "test", Command: []string{"x"}, Verdict: config.VerdictExitZero,
		RequiredFor: []string{"in_progress->ready_for_testing", "testing->ready_for_review",
			"judging->ready_for_validation", "applying->confirming", "confirming->ready_to_merge"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	c.Blast.AutoApplyMax = config.RadiusHost
	c.Blast.NamedApproverMin = config.RadiusHost
	c.Blast.TwoApprovalsMin = config.RadiusRegion
	c.Blast.ApprovalTTLMinutes = 60
	c.Dispatch.MaxAttempts = 3
	return c
}

func greenGate() *gate.Result {
	return &gate.Result{
		Edge: "x", Status: gate.StatusGreen, TreeSHA: "abc123abc123", Dirty: false,
		Checks: []gate.Observation{{CheckID: "test", Rule: config.VerdictExitZero, Ran: true, Verdict: gate.StatusGreen}},
	}
}

func env(t *testing.T, mutate func(m map[string]any)) *envelope.Envelope {
	t.Helper()
	m := map[string]any{
		"envelope_version": envelope.Version,
		"run_id":           "p-1",
		"worker_type":      "performer",
		"work_item_id":     "S1-001",
		"verdict":          "pass",
		"head_sha":         "abc123abc123",
		"commands_run":     []map[string]any{{"check_id": "test", "cmd": "x", "exit_code": 0}},
		"outputs":          map[string]any{},
	}
	if mutate != nil {
		mutate(m)
	}
	return parseMap(t, m)
}

func item(state string) ledger.Item {
	return ledger.Item{
		ID: "S1-001", SegmentID: "S1", Title: "thing", State: state,
		Radius: string(config.RadiusNone), Criteria: []string{"it works"},
	}
}

func TestAnEdgeThatDoesNotExistIsRefusedAsSuch(t *testing.T) {
	a := New(cfg(t))
	d := a.Decide(Request{RunID: "r-1", Worker: "performer", From: StateQueued, To: StateDone, Now: now},
		Facts{RunStarted: true, Item: item("queued")})
	if d.Admitted || d.Reason != ReasonNoSuchEdge {
		t.Fatalf("want no_such_edge, got admitted=%v reason=%s", d.Admitted, d.Reason)
	}
}

// TestAStaleReadIsNamedAsOne matters because the refusal has to describe the
// right problem. A run that worked from an out-of-date view of the item should
// be told that, not told its evidence is thin.
func TestAStaleReadIsNamedAsOne(t *testing.T) {
	a := New(cfg(t))
	d := a.Decide(Request{
		Worker: "performer", From: StateInProgress, To: StateVerifying,
		Env: env(t, nil), Gate: greenGate(), Now: now,
	}, Facts{RunStarted: true, Item: item("verifying"), CommitReachable: true})
	if d.Reason != ReasonStaleFromState {
		t.Fatalf("want stale_from_state, got %s: %s", d.Reason, d.Detail)
	}
	if !strings.Contains(d.Detail, "stale read") {
		t.Errorf("the refusal should say what went wrong: %q", d.Detail)
	}

	// Each substring is a thing the reader has to be able to act on, and each is
	// asserted by name because "the detail is non-empty" passes vacuously the day
	// the message is gutted back to naming the run as the cause.
	for _, want := range []string{
		"S1-001",         // which item moved
		"verifying",      // the state the ledger actually holds
		"adlc item show", // the command that shows it
		"envelope",       // and that none of the run's output was
		"questions",      // discarded when the transition was refused —
		"lessons",        // it was all recorded before this decision
		"recorded",       // was reached
	} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("the refusal should carry %q so the reader knows what to do next: %q", want, d.Detail)
		}
	}
}

// TestAFreshReadIsNotRefusedAsStale is the clean case for the guard above: a
// proposal whose From matches the ledger must get past this check entirely.
// Without it the stale-read test still passes on the day the comparison is
// inverted and every proposal is refused as stale.
func TestAFreshReadIsNotRefusedAsStale(t *testing.T) {
	a := New(cfg(t))
	d := a.Decide(Request{
		RunID: "r-1", Worker: "performer", From: StateInProgress, To: StateVerifying,
		Env: env(t, nil), Gate: greenGate(), Now: now,
	}, Facts{RunStarted: true, Item: item("in_progress"), CommitReachable: true})
	if d.Reason == ReasonStaleFromState {
		t.Fatalf("the item is in the state the proposal claims; got [%s] %s", d.Reason, d.Detail)
	}
}

// TestOnlyTheJudgeMayClearAVerificationTask pins who the stage belongs to at
// the edge rather than at the routing.
//
// The performer is the original case: a builder closing its own review is the
// conflict of interest the separation exists to prevent. The tester and the
// validator are here because their tasks left the stage, and an edge that
// merely stops being dispatched to them still admits them if something else
// proposes it. Refused by name is the difference between a role that cannot
// clear a task and one that nothing currently asks to.
func TestOnlyTheJudgeMayClearAVerificationTask(t *testing.T) {
	a := New(cfg(t))
	f := func() Facts {
		return Facts{RunStarted: true, Item: item("verifying"), CommitReachable: true}
	}
	for _, worker := range []string{"performer", "tester", "validator"} {
		e := env(t, func(m map[string]any) { m["worker_type"] = worker })
		d := a.Decide(Request{RunID: "r-1", Worker: worker, From: StateVerifying, To: StateVerifying,
			Env: e, Gate: greenGate(), Now: now}, f())
		if d.Admitted || d.Reason != ReasonWrongProposer {
			t.Errorf("%s cleared a verification task; got admitted=%v %s", worker, d.Admitted, d.Reason)
		}
	}
	// Clean case: the judge may, or nothing ever leaves verification and the
	// refusals above would pass on a stage that had stopped working entirely.
	d := a.Decide(Request{RunID: "j-1", Worker: "judge", From: StateVerifying, To: StateVerifying,
		Env: judgeEnv(t, "pass"), Gate: greenGate(), Now: now}, f())
	if !d.Admitted {
		t.Fatalf("the judge should be able to clear the stage's only task, got [%s] %s", d.Reason, d.Detail)
	}
}

// judgeEnv is a judge's report on an item with one acceptance criterion: the
// criterion held, and it cites a command the envelope says was run.
//
// The verdict is a parameter because the two answers differ in exactly one
// place. A judge now rules on whether the work serves the brief, which is a
// question no command answers — so "every criterion held" and "this is not
// what was asked for" is a coherent envelope, and it is the shape the fleet's
// actual failure arrives in.
func judgeEnv(t *testing.T, verdict string) *envelope.Envelope {
	t.Helper()
	return env(t, func(m map[string]any) {
		m["worker_type"] = "judge"
		m["verdict"] = verdict
		m["outputs"] = map[string]any{"criteria": []map[string]any{
			{"id": "AC-1", "status": "pass", "command_index": 0, "evidence": "ran it"},
		}}
	})
}

// TestAVerifierCannotBeTheRunThatDidTheWork pins the conflict of interest the
// author/judge split exists to prevent.
func TestAJudgeCannotBeTheRunThatDidTheWork(t *testing.T) {
	a := New(cfg(t))
	e := env(t, func(m map[string]any) {
		m["worker_type"] = "judge"
		m["outputs"] = map[string]any{"criteria": []map[string]any{
			{"id": "AC-1", "status": "pass", "command_index": 0, "evidence": "ran it"},
		}}
	})
	f := Facts{RunStarted: true, Item: item("verifying"), CommitReachable: true, ImplementRunID: "same-run"}

	d := a.Decide(Request{Worker: "judge", RunID: "same-run", From: StateVerifying, To: StateVerifying,
		Env: e, Gate: greenGate(), Now: now}, f)
	if d.Admitted || d.Reason != ReasonSelfCertified {
		t.Fatalf("a run judging its own work must be refused, got admitted=%v %s", d.Admitted, d.Reason)
	}
	// Clean case: a different run passes the same evidence.
	d2 := a.Decide(Request{Worker: "judge", RunID: "other-run", From: StateVerifying, To: StateVerifying,
		Env: e, Gate: greenGate(), Now: now}, f)
	if !d2.Admitted {
		t.Fatalf("an independent judge should be admitted, got [%s] %s", d2.Reason, d2.Detail)
	}
}

func TestACriterionPassedOnInspectionAloneIsRefused(t *testing.T) {
	a := New(cfg(t))
	e := env(t, func(m map[string]any) {
		m["worker_type"] = "judge"
		m["outputs"] = map[string]any{"criteria": []map[string]any{
			{"id": "AC-1", "status": "pass", "command_index": 7, "evidence": "looks right"},
		}}
	})
	d := a.Decide(Request{Worker: "judge", RunID: "v-1", From: StateVerifying, To: StateVerifying,
		Env: e, Gate: greenGate(), Now: now}, Facts{RunStarted: true, Item: item("verifying"), CommitReachable: true})
	if d.Admitted {
		t.Fatal("a criterion citing a command that is not in commands_run must not pass")
	}
	if !strings.Contains(d.Detail, "inspection alone") {
		t.Errorf("the refusal should explain the rule: %q", d.Detail)
	}
}

func TestAnUncommittedTreeIsRefused(t *testing.T) {
	a := New(cfg(t))
	g := greenGate()
	g.Dirty = true
	g.DirtyPaths = []string{"internal/thing.go"}
	d := a.Decide(Request{RunID: "r-1", Worker: "performer", From: StateInProgress, To: StateVerifying,
		Env: env(t, nil), Gate: g, Now: now}, Facts{RunStarted: true, Item: item("in_progress"), CommitReachable: true})
	if d.Reason != ReasonUncommittedTree {
		t.Fatalf("want uncommitted_tree, got %s", d.Reason)
	}
}

func TestAGateThatCouldNotRunIsNotAPass(t *testing.T) {
	a := New(cfg(t))
	g := &gate.Result{Status: gate.StatusUnknown, Checks: []gate.Observation{{
		CheckID: "test", Verdict: gate.StatusUnknown, Why: "tool absent — check not run, NOT passed",
	}}}
	d := a.Decide(Request{RunID: "r-1", Worker: "performer", From: StateInProgress, To: StateVerifying,
		Env: env(t, nil), Gate: g, Now: now}, Facts{RunStarted: true, Item: item("in_progress"), CommitReachable: true})
	if d.Admitted || d.Reason != ReasonGateUnknown {
		t.Fatalf("UNKNOWN must not satisfy a gate; got admitted=%v %s", d.Admitted, d.Reason)
	}
}

func TestAnUnreachableCommitIsRefused(t *testing.T) {
	a := New(cfg(t))
	d := a.Decide(Request{RunID: "r-1", Worker: "performer", From: StateInProgress, To: StateVerifying,
		Env: env(t, nil), Gate: greenGate(), Now: now},
		Facts{RunStarted: true, Item: item("in_progress"), CommitReachable: false})
	if d.Reason != ReasonCommitUnreachable {
		t.Fatalf("want commit_unreachable, got %s", d.Reason)
	}
}

func hostItem(state, digest string) ledger.Item {
	it := item(state)
	it.Radius = string(config.RadiusFleet)
	it.Resources = []string{"fleet:prod"}
	it.PlanDigest = digest
	return it
}

func TestAChangeAboveTheThresholdStopsForApproval(t *testing.T) {
	a := New(cfg(t))
	e := env(t, func(m map[string]any) {
		m["worker_type"] = "arbiter"
		m["plan_digest"] = "plan-aaa"
	})
	// fleet is above auto_apply_max=host, so applying directly is refused...
	d := a.Decide(Request{RunID: "r-1", Worker: "arbiter", From: StateArbitrating, To: StateApplying,
		Env: e, Gate: greenGate(), Now: now}, Facts{RunStarted: true, Item: hostItem("arbitrating", "plan-aaa"), CommitReachable: true})
	if d.Admitted || d.Reason != ReasonRadiusMismatch {
		t.Fatalf("a fleet-wide change must not apply unattended; got admitted=%v %s", d.Admitted, d.Reason)
	}
	// ...and awaiting_approval is the edge that is open.
	d2 := a.Decide(Request{RunID: "r-1", Worker: "arbiter", From: StateArbitrating, To: StateAwaitingApproval,
		Env: e, Gate: greenGate(), Now: now}, Facts{RunStarted: true, Item: hostItem("arbitrating", "plan-aaa"), CommitReachable: true})
	if !d2.Admitted {
		t.Fatalf("it should be able to stop for approval, got [%s] %s", d2.Reason, d2.Detail)
	}
}

func TestApprovingOnePlanDoesNotApproveAnother(t *testing.T) {
	a := New(cfg(t))
	approved := ledger.Approval{
		ID: "AP-1", ItemID: "S1-001", Radius: "fleet", PlanDigest: "plan-aaa",
		DecidedMS: now.Add(-10 * time.Minute).UnixMilli(), Verdict: "approve", Approver: "brandon",
	}
	// Clean case: the plan is what was approved.
	ok := a.Decide(Request{From: StateAwaitingApproval, To: StateApplying, Now: now},
		Facts{RunStarted: true, Item: hostItem("awaiting_approval", "plan-aaa"), Approvals: []ledger.Approval{approved}})
	if !ok.Admitted {
		t.Fatalf("a current approval should admit the apply, got [%s] %s", ok.Reason, ok.Detail)
	}
	// Firing case: the plan moved after the approval was given.
	stale := a.Decide(Request{From: StateAwaitingApproval, To: StateApplying, Now: now},
		Facts{RunStarted: true, Item: hostItem("awaiting_approval", "plan-bbb"), Approvals: []ledger.Approval{approved}})
	if stale.Admitted || stale.Reason != ReasonApprovalStale {
		t.Fatalf("approving one plan and applying another must be refused; got admitted=%v %s", stale.Admitted, stale.Reason)
	}
}

func TestAnExpiredApprovalIsStale(t *testing.T) {
	a := New(cfg(t))
	old := ledger.Approval{
		ID: "AP-1", ItemID: "S1-001", PlanDigest: "plan-aaa", Verdict: "approve", Approver: "brandon",
		DecidedMS: now.Add(-5 * time.Hour).UnixMilli(),
	}
	d := a.Decide(Request{From: StateAwaitingApproval, To: StateApplying, Now: now},
		Facts{RunStarted: true, Item: hostItem("awaiting_approval", "plan-aaa"), Approvals: []ledger.Approval{old}})
	if d.Admitted || d.Reason != ReasonApprovalStale {
		t.Fatalf("an approval past its TTL must not apply; got admitted=%v %s", d.Admitted, d.Reason)
	}
}

func TestNoApprovalAtAllIsNamedDifferentlyFromAStaleOne(t *testing.T) {
	a := New(cfg(t))
	d := a.Decide(Request{From: StateAwaitingApproval, To: StateApplying, Now: now},
		Facts{RunStarted: true, Item: hostItem("awaiting_approval", "plan-aaa")})
	if d.Reason != ReasonApprovalMissing {
		t.Fatalf("want approval_missing, got %s — 'nobody approved' and 'the plan moved' call for different actions", d.Reason)
	}
}

func TestARegionWideChangeNeedsTwoApprovers(t *testing.T) {
	a := New(cfg(t))
	it := hostItem("awaiting_approval", "plan-aaa")
	it.Radius = string(config.RadiusRegion)
	one := ledger.Approval{ID: "AP-1", PlanDigest: "plan-aaa", Verdict: "approve", Approver: "brandon",
		DecidedMS: now.Add(-time.Minute).UnixMilli()}
	d := a.Decide(Request{From: StateAwaitingApproval, To: StateApplying, Now: now},
		Facts{RunStarted: true, Item: it, Approvals: []ledger.Approval{one}})
	if d.Admitted {
		t.Fatal("a region-wide change should need two approvers")
	}
	two := ledger.Approval{ID: "AP-2", PlanDigest: "plan-aaa", Verdict: "approve", Approver: "someone-else",
		DecidedMS: now.Add(-time.Minute).UnixMilli()}
	d2 := a.Decide(Request{From: StateAwaitingApproval, To: StateApplying, Now: now},
		Facts{RunStarted: true, Item: it, Approvals: []ledger.Approval{one, two}})
	if !d2.Admitted {
		t.Fatalf("two distinct approvers should be enough, got [%s] %s", d2.Reason, d2.Detail)
	}
}

func TestAnUnknownBlastRadiusFailsClosed(t *testing.T) {
	a := New(cfg(t))
	it := item("arbitrating")
	it.Radius = "planetary"
	e := env(t, func(m map[string]any) { m["worker_type"] = "arbiter" })
	d := a.Decide(Request{RunID: "r-1", Worker: "arbiter", From: StateArbitrating, To: StateReadyToMerge,
		Env: e, Gate: greenGate(), Now: now}, Facts{RunStarted: true, Item: it, CommitReachable: true})
	if d.Admitted || d.Reason != ReasonRadiusUndeclared {
		t.Fatalf("an unrecognised radius must fail closed; got admitted=%v %s", d.Admitted, d.Reason)
	}
}

func TestConfirmingRequiresTheArtifactItJudged(t *testing.T) {
	a := New(cfg(t))
	it := hostItem("confirming", "plan-aaa")
	e := env(t, func(m map[string]any) { m["worker_type"] = "validator" }) // no artifact
	d := a.Decide(Request{RunID: "r-1", Worker: "validator", From: StateConfirming, To: StateReadyToMerge,
		Env: e, Gate: greenGate(), Now: now}, Facts{RunStarted: true, Item: it, CommitReachable: true})
	if d.Admitted || d.Reason != ReasonArtifactMissing {
		t.Fatalf("behavioural evidence must name what it ran against; got admitted=%v %s", d.Admitted, d.Reason)
	}
	e2 := env(t, func(m map[string]any) {
		m["worker_type"] = "validator"
		m["artifact"] = "sha256:deadbeef"
	})
	g := greenGate()
	g.Artifact = "sha256:deadbeef"
	d2 := a.Decide(Request{RunID: "r-1", Worker: "validator", From: StateConfirming, To: StateReadyToMerge,
		Env: e2, Gate: g, Now: now}, Facts{RunStarted: true, Item: it, CommitReachable: true})
	if !d2.Admitted {
		t.Fatalf("with an artifact digest it should be admitted, got [%s] %s", d2.Reason, d2.Detail)
	}
}

func TestReworkStopsAtTheAttemptLimit(t *testing.T) {
	a := New(cfg(t))
	it := item("rejected")
	it.Attempts = 3
	d := a.Decide(Request{AsPM: true, From: StateRejected, To: StateInProgress, Reason: "try again", Now: now},
		Facts{RunStarted: true, Item: it})
	if d.Admitted || d.Reason != ReasonAttemptLimit {
		t.Fatalf("past the limit an item escalates rather than looping; got admitted=%v %s", d.Admitted, d.Reason)
	}
	it.Attempts = 1
	if d2 := a.Decide(Request{AsPM: true, From: StateRejected, To: StateInProgress, Reason: "try again", Now: now},
		Facts{RunStarted: true, Item: it}); !d2.Admitted {
		t.Fatalf("within the limit rework should be allowed, got [%s] %s", d2.Reason, d2.Detail)
	}
}

func TestAnEdgeAHumanDrivesNeedsAStatedReason(t *testing.T) {
	a := New(cfg(t))
	it := item("done")
	d := a.Decide(Request{AsPM: true, From: StateDone, To: StateVerifying, Now: now}, Facts{RunStarted: true, Item: it})
	if d.Admitted || d.Reason != ReasonNoReason {
		t.Fatalf("reopening a finished item needs an owner and a reason; got admitted=%v %s", d.Admitted, d.Reason)
	}
	d2 := a.Decide(Request{AsPM: true, From: StateDone, To: StateVerifying, Now: now,
		Reason: "the retro found three blockers a backfill had skipped"}, Facts{RunStarted: true, Item: it})
	if !d2.Admitted {
		t.Fatalf("with a reason it should be admitted, got [%s] %s", d2.Reason, d2.Detail)
	}
}

func TestABlockingQuestionMustCarryALean(t *testing.T) {
	a := New(cfg(t))
	e := env(t, func(m map[string]any) {
		m["verdict"] = "blocked"
		m["questions"] = []map[string]any{{"text": "which way?", "blocking": true}}
	})
	d := a.Decide(Request{RunID: "r-1", Worker: "performer", From: StateInProgress, To: StateBlocked,
		Env: e, Gate: greenGate(), Now: now}, Facts{RunStarted: true, Item: item("in_progress")})
	if d.Admitted {
		t.Fatal("a question with no recommendation hands back the analysis the run was dispatched to do")
	}
	e2 := env(t, func(m map[string]any) {
		m["verdict"] = "blocked"
		m["questions"] = []map[string]any{{"text": "which way?", "blocking": true, "lean": "A, because it is reversible"}}
	})
	if d2 := a.Decide(Request{RunID: "r-1", Worker: "performer", From: StateInProgress, To: StateBlocked,
		Env: e2, Gate: greenGate(), Now: now}, Facts{RunStarted: true, Item: item("in_progress")}); !d2.Admitted {
		t.Fatalf("a question with a lean should be admitted, got [%s] %s", d2.Reason, d2.Detail)
	}
}

// TestAnOpenBlockingQuestionHoldsTheItem pins that an item does not leave
// verification while somebody has been asked something and has not answered.
//
// It is asserted on verifying -> reviewed, the control plane's own edge, which
// is where ReqQuestionsAnswered sits. That is deliberate rather than
// incidental: the guard used to hang off the validator's task edge, so it fired
// only if a validator happened to be the last of three to report — a question
// raised after that run finished held nothing at all. The stage now settles at
// one place, and the question is checked at that place, so an item cannot walk
// past an open blocking question by any ordering of the runs inside it.
func TestAnOpenBlockingQuestionHoldsTheItem(t *testing.T) {
	a := New(cfg(t))
	open := Facts{RunStarted: true, Item: item("verifying"), CommitReachable: true,
		OpenBlockingQs: []ledger.Question{{ID: "Q-1", Blocking: true, Text: "which way?"}}}
	// Worker is empty: the move out of the stage is the control plane's, computed
	// from the record, and is never proposed by whichever run finished last.
	req := Request{From: StateVerifying, To: StateReviewed, Now: now,
		Env: judgeEnv(t, "pass"), Gate: greenGate(),
		Reason: "every task in the stage passed"}
	if d := a.Decide(req, open); d.Admitted || d.Reason != ReasonOpenQuestion {
		t.Fatalf("an item cannot finish over an unanswered blocking question; got admitted=%v %s", d.Admitted, d.Reason)
	}
	// Clean case: answered, and the same move is admitted. Without it this test
	// passes on the day nothing may leave verification for any reason.
	answered := open
	answered.OpenBlockingQs = nil
	if d := a.Decide(req, answered); !d.Admitted {
		t.Fatalf("a stage with nothing outstanding must still complete, got [%s] %s", d.Reason, d.Detail)
	}
}

func TestEveryEdgeInTheTableNamesAProposer(t *testing.T) {
	for _, e := range Table() {
		if len(e.Proposers) == 0 {
			t.Errorf("%s has no proposer, so nothing can ever take it", e)
		}
		if !e.From.Known() || !e.To.Known() {
			t.Errorf("%s references a state this build does not know", e)
		}
	}
}

func parseMap(t *testing.T, m map[string]any) *envelope.Envelope {
	t.Helper()
	b := marshal(t, m)
	e, err := envelope.Parse(b)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	return e
}

// TestAProposalFromAnUnregisteredRunIsRefused closes a gap found by running
// this system against itself: a worker proposal carrying a run id that nothing
// ever started was admitted, so the record held a state change with no prompt
// pin, no base commit and no attribution — and the never-run roll call had
// no row to count it against.
func TestAProposalFromAnUnregisteredRunIsRefused(t *testing.T) {
	a := New(cfg(t))
	f := Facts{Item: item("in_progress"), CommitReachable: true} // RunStarted false
	d := a.Decide(Request{Worker: "performer", RunID: "p-ghost", From: StateInProgress, To: StateVerifying,
		Env: env(t, nil), Gate: greenGate(), Now: now}, f)
	if d.Admitted || d.Reason != ReasonRunNotStarted {
		t.Fatalf("want run_not_started, got admitted=%v %s", d.Admitted, d.Reason)
	}

	// A worker proposal with no run id at all is the same defect, named.
	d2 := a.Decide(Request{RunID: "r-1", Worker: "performer", From: StateInProgress, To: StateVerifying,
		Env: env(t, nil), Gate: greenGate(), Now: now}, f)
	if d2.Reason != ReasonRunNotStarted {
		t.Fatalf("an unattributable proposal must be refused, got %s", d2.Reason)
	}

	// Clean case: the coordinator acting in its own name is not a dispatched run
	// and needs no run row.
	it := item("rejected")
	if d3 := a.Decide(Request{AsPM: true, From: StateRejected, To: StateInProgress,
		Reason: "rework", Now: now}, Facts{Item: it}); !d3.Admitted {
		t.Fatalf("the coordinator should not need a run row, got [%s] %s", d3.Reason, d3.Detail)
	}
}
