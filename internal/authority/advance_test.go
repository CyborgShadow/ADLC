package authority

import (
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

func policy() config.BlastPolicy {
	return config.BlastPolicy{AutoApplyMax: config.RadiusHost, NamedApproverMin: config.RadiusHost}
}

// TestTheToolDecidesWhereWorkGoes walks the whole pipeline. An agent reports a
// verdict and nothing else; every one of these answers is arithmetic on
// recorded state.
func TestTheToolDecidesWhereWorkGoes(t *testing.T) {
	cases := []struct {
		name       string
		from       State
		capability string
		verdict    string
		radius     config.Radius
		want       State
	}{
		{"a builder picks up ready work", StateReady, config.CapImplement, "pass", config.RadiusNone, StateReadyForTesting},
		{"work and tests land", StateInProgress, config.CapImplement, "pass", config.RadiusNone, StateReadyForTesting},
		{"tests pass", StateTesting, config.CapTest, "pass", config.RadiusNone, StateReadyForReview},
		{"a test fails", StateTesting, config.CapTest, "fail", config.RadiusNone, StateInProgress},
		{"criteria judged green", StateJudging, config.CapJudge, "pass", config.RadiusNone, StateReadyForValidation},
		{"a criterion fails", StateJudging, config.CapJudge, "fail", config.RadiusNone, StateInProgress},
		{"review passes", StateValidating, config.CapValidate, "pass", config.RadiusNone, StateReviewed},
		{"review rejects", StateValidating, config.CapValidate, "reject", config.RadiusNone, StateRejected},
		{"hygiene pass", StateJanitoring, config.CapCurate, "pass", config.RadiusNone, StateReadyForArbitration},
		{"the janitor finds something wrong", StateJanitoring, config.CapCurate, "reject", config.RadiusNone, StateInProgress},
		{"source-only work clears to merge", StateArbitrating, config.CapArbitrate, "pass", config.RadiusNone, StateReadyToMerge},
		{"a small change applies", StateArbitrating, config.CapArbitrate, "pass", config.RadiusHost, StateApplying},
		{"a big change waits for a person", StateArbitrating, config.CapArbitrate, "pass", config.RadiusFleet, StateAwaitingApproval},
		{"arbitration rejects", StateArbitrating, config.CapArbitrate, "reject", config.RadiusNone, StateRejected},
		{"apply succeeds", StateApplying, config.CapOperate, "pass", config.RadiusHost, StateConfirming},
		{"the applied artifact confirms", StateConfirming, config.CapValidate, "pass", config.RadiusHost, StateReadyToMerge},
		{"the applied artifact does not", StateConfirming, config.CapValidate, "reject", config.RadiusHost, StateRejected},
		{"lessons recorded", StateMerged, config.CapImprove, "pass", config.RadiusNone, StateDone},
		{"blocked from anywhere live", StateJudging, config.CapJudge, "blocked", config.RadiusNone, StateBlocked},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			adv := NextState(c.from, c.capability, c.verdict, c.radius, policy())
			if !adv.Inferred {
				t.Fatalf("nothing inferred: %s", adv.Stall)
			}
			if adv.To != c.want {
				t.Fatalf("%s + %s + %q -> %s, want %s", c.from, c.capability, c.verdict, adv.To, c.want)
			}
			if adv.Why == "" {
				t.Error("every move carries a reason, because the reason is what the record shows a person")
			}
		})
	}
}

// TestTheSameInputsAlwaysGiveTheSameAnswer is what makes a past decision
// re-derivable. Without it, replay could not tell a changed rule from a
// changed record.
func TestTheSameInputsAlwaysGiveTheSameAnswer(t *testing.T) {
	first := NextState(StateArbitrating, config.CapArbitrate, "pass", config.RadiusFleet, policy())
	for i := 0; i < 50; i++ {
		if again := NextState(StateArbitrating, config.CapArbitrate, "pass", config.RadiusFleet, policy()); again != first {
			t.Fatalf("the same inputs produced a different answer on attempt %d", i)
		}
	}
}

// TestEveryStageRefusesTheWrongRole is the separation of duties, checked at
// every handoff rather than only at the one everybody remembers.
func TestEveryStageRefusesTheWrongRole(t *testing.T) {
	wrong := []struct {
		from       State
		capability string
	}{
		{StateTesting, config.CapImplement},   // the builder cannot pass its own tests
		{StateJudging, config.CapImplement},   // nor judge its own work
		{StateJudging, config.CapTest},        // the tester does not judge
		{StateValidating, config.CapJudge},    // the judge does not validate
		{StateJanitoring, config.CapValidate}, // the validator does not curate
		{StateArbitrating, config.CapCurate},  // the janitor does not arbitrate
		{StateApplying, config.CapImplement},  // a builder does not apply
		{StateConfirming, config.CapOperate},  // nobody confirms their own apply
		{StateMerged, config.CapValidate},     // improving is its own role
	}
	for _, w := range wrong {
		adv := NextState(w.from, w.capability, "pass", config.RadiusNone, policy())
		if adv.Inferred {
			t.Errorf("%s moved an item out of %s; that stage belongs to another role", w.capability, w.from)
		}
		if adv.Stall == "" {
			t.Errorf("a stall at %s has to say why, or an operator sees an item that never moves", w.from)
		}
	}
}

func TestNothingAnAgentReportsClearsAnApprovalOrAMerge(t *testing.T) {
	for _, from := range []State{StateAwaitingApproval, StateReadyToMerge, StateMerging} {
		for _, cap := range []string{config.CapImplement, config.CapValidate, config.CapOperate, config.CapArbitrate} {
			if adv := NextState(from, cap, "pass", config.RadiusFleet, policy()); adv.Inferred {
				t.Errorf("%s moved an item out of %s; that is a person's or the control plane's", cap, from)
			}
		}
	}
}

func TestAnUnknownRadiusStopsRatherThanApplying(t *testing.T) {
	adv := NextState(StateArbitrating, config.CapArbitrate, "pass", config.Radius("planetary"), policy())
	if adv.Inferred {
		t.Fatalf("an unrecognised radius must fail closed, got %s", adv.To)
	}
}

// TestPickedUpPairsEveryWaitingStateWithAWorkingOne pins the queue-depth split:
// "four items are waiting for review" is a different problem from "four items
// are in review", and the board has to be able to say which.
func TestPickedUpPairsEveryWaitingStateWithAWorkingOne(t *testing.T) {
	for _, s := range AllStates() {
		if !s.Waiting() {
			continue
		}
		to, ok := PickedUp(s)
		if !ok || to == s {
			t.Errorf("%s is a waiting state with nowhere to be picked up to", s)
		}
		if to.Waiting() {
			t.Errorf("%s picks up into %s, which is still a waiting state", s, to)
		}
	}
	// StateMerged is not "waiting" but is still picked up, by the improver.
	if to, ok := PickedUp(StateMerged); !ok || to != StateImproving {
		t.Errorf("merged work should be picked up for the lessons pass, got %s", to)
	}
}

// TestEveryDispatchableStateHasAnEdgeOut is the check that would have caught an
// item stuck behind a step nobody performs.
func TestEveryDispatchableStateHasAnEdgeOut(t *testing.T) {
	for _, s := range AllStates() {
		capability, _ := CapabilityFor(s)
		if capability == "" {
			continue
		}
		found := false
		for _, e := range Table() {
			if e.From == s && e.To != StateBlocked {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s can be dispatched against but has no edge out, so work would pile up there forever", s)
		}
	}
}

func TestEveryLifecycleStateMapsOntoAStageAPersonReads(t *testing.T) {
	for _, s := range AllStates() {
		if StageOf(s).Label == "" {
			t.Errorf("%s maps to no readable stage", s)
		}
	}
}

func TestEveryEdgeInTheTableIsWellFormed(t *testing.T) {
	for _, e := range Table() {
		if len(e.Proposers) == 0 {
			t.Errorf("%s has no proposer, so nothing can ever take it", e)
		}
		if !e.From.Known() || !e.To.Known() {
			t.Errorf("%s references a state this build does not know", e)
		}
		if e.Doc == "" {
			t.Errorf("%s has no explanation, and the table is the specification", e)
		}
	}
}

// --- the planning pipeline ------------------------------------------------

func items(states ...string) []ledger.Item {
	var out []ledger.Item
	for i, s := range states {
		out = append(out, ledger.Item{ID: string(rune('a' + i)), State: s})
	}
	return out
}

// TestNothingIsBuiltUntilThePlanIsCheckedAgainstTheIntent is the handoff gate
// the planning pipeline exists for.
func TestNothingIsBuiltUntilThePlanIsCheckedAgainstTheIntent(t *testing.T) {
	if adv := NextSegmentState(SegSignedOff, config.CapResearch, "pass", 0); adv.To != SegResearched {
		t.Fatalf("a signed-off intent should become a written approach, got %s", adv.To)
	}
	if adv := NextSegmentState(SegResearched, config.CapPlan, "pass", 3); adv.To != SegPlanned {
		t.Fatalf("an approach with items should be planned, got %s", adv.To)
	}
	if SegPlanned.OpenForWork() {
		t.Fatal("a plan nobody has checked must not be dispatchable — this gate is the whole reason planning is a pipeline")
	}
	if adv := NextSegmentState(SegPlanned, config.CapValidate, "pass", 0); adv.To != SegReady {
		t.Fatalf("an accepted plan should open the work, got %s", adv.To)
	}
	if !SegReady.OpenForWork() {
		t.Fatal("an accepted plan must be dispatchable")
	}
	if adv := NextSegmentState(SegPlanned, config.CapValidate, "reject", 0); adv.To != SegResearched {
		t.Fatalf("a rejected plan goes back for decomposition, got %s", adv.To)
	}
}

// TestNoAgentSignsOffAnIntent pins the one planning gate a machine never
// passes on its own.
func TestNoAgentSignsOffAnIntent(t *testing.T) {
	for _, from := range []SegmentState{SegTheory, SegRoadmap} {
		for _, cap := range []string{config.CapResearch, config.CapPlan, config.CapValidate, config.CapArbitrate} {
			if adv := NextSegmentState(from, cap, "pass", 5); adv.Inferred {
				t.Errorf("%s moved a deliverable out of %s; committing to an idea is a person's decision", cap, from)
			}
		}
	}
	if !SegRoadmap.NeedsPerson() {
		t.Error("a roadmap entry awaiting sign-off should be flagged as needing a person")
	}
}

func TestAPlannerThatProducedNothingDoesNotAdvanceTheDeliverable(t *testing.T) {
	if adv := NextSegmentState(SegResearched, config.CapPlan, "pass", 0); adv.Inferred {
		t.Fatal("a planning run that created no items has not planned anything")
	}
}

func TestADeliverableIsDeliveredOnlyWhenEveryItemIsFinished(t *testing.T) {
	if adv := SegmentFromItems(SegBuilding, items("done", "in_progress")); adv.Inferred {
		t.Fatalf("work is still in flight; nothing should move, got %s", adv.To)
	}
	if adv := SegmentFromItems(SegBuilding, items("done", "done")); adv.To != SegDelivered {
		t.Fatalf("want delivered, got %s", adv.To)
	}
	if adv := SegmentFromItems(SegBuilding, items("cancelled", "cancelled")); adv.Inferred {
		t.Fatal("a deliverable whose every item was cancelled has not been delivered")
	}
	if adv := SegmentFromItems(SegReady, items("in_progress")); adv.To != SegBuilding {
		t.Fatalf("once work has moved the deliverable is building, got %s", adv.To)
	}
}

func TestProgressCountsFinishedWorkNotBusyWork(t *testing.T) {
	p := Progress(items("done", "in_progress", "testing", "ready_for_review", "queued"))
	if p.Total != 5 || p.Done != 1 {
		t.Fatalf("unexpected counts: %+v", p)
	}
	if p.Percent() != 20 {
		t.Fatalf("want 20%%, got %d%% — the bar measures items finished, not items touched", p.Percent())
	}
	if p.Waiting != 1 {
		t.Errorf("ready_for_review is queue depth, got %d waiting", p.Waiting)
	}
	if p.ByStage["review"] != 1 || p.ByStage["testing"] != 1 || p.ByStage["planning"] != 1 {
		t.Fatalf("stage rollup wrong: %+v", p.ByStage)
	}
}
