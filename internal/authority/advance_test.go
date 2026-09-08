package authority

import (
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

func policy() config.BlastPolicy {
	return config.BlastPolicy{AutoApplyMax: config.RadiusHost, NamedApproverMin: config.RadiusHost}
}

// TestTheToolDecidesWhereWorkGoes is the whole of "the tooling advances the
// status". An agent reports a verdict and nothing else; every one of these
// answers is arithmetic on recorded state.
func TestTheToolDecidesWhereWorkGoes(t *testing.T) {
	cases := []struct {
		name       string
		from       State
		capability string
		verdict    string
		radius     config.Radius
		want       State
	}{
		{"implementation lands", StateInProgress, config.CapImplement, "pass", config.RadiusNone, StateVerifying},
		{"first pick up", StateReady, config.CapImplement, "pass", config.RadiusNone, StateVerifying},
		{"verification passes", StateVerifying, config.CapVerify, "pass", config.RadiusNone, StateValidating},
		{"verification fails", StateVerifying, config.CapVerify, "fail", config.RadiusNone, StateInProgress},
		{"review rejects", StateValidating, config.CapValidate, "reject", config.RadiusNone, StateRejected},
		{"source-only work finishes", StateValidating, config.CapValidate, "pass", config.RadiusNone, StateDone},
		{"a small change applies", StateValidating, config.CapValidate, "pass", config.RadiusHost, StateApplying},
		{"a big change waits", StateValidating, config.CapValidate, "pass", config.RadiusFleet, StateAwaitingApproval},
		{"apply succeeds", StateApplying, config.CapOperate, "pass", config.RadiusHost, StateConfirming},
		{"confirmation passes", StateConfirming, config.CapValidate, "pass", config.RadiusHost, StateDone},
		{"confirmation fails", StateConfirming, config.CapValidate, "reject", config.RadiusHost, StateRejected},
		{"blocked from anywhere live", StateVerifying, config.CapVerify, "blocked", config.RadiusNone, StateBlocked},
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
				t.Error("every move carries a reason, because the reason is what the history shows a person")
			}
		})
	}
}

// TestTheSameInputsAlwaysGiveTheSameAnswer is what makes a past decision
// re-derivable. If this were not true, `adlc run replay` could not tell a
// changed rule from a changed record.
func TestTheSameInputsAlwaysGiveTheSameAnswer(t *testing.T) {
	first := NextState(StateValidating, config.CapValidate, "pass", config.RadiusFleet, policy())
	for i := 0; i < 50; i++ {
		again := NextState(StateValidating, config.CapValidate, "pass", config.RadiusFleet, policy())
		if again != first {
			t.Fatalf("the same inputs produced a different answer on attempt %d", i)
		}
	}
}

func TestAWorkerCannotMoveWorkItWasNotDispatchedFor(t *testing.T) {
	// A builder reporting a pass from verification moves nothing: the state it
	// would take belongs to a role that did not do this work.
	adv := NextState(StateVerifying, config.CapImplement, "pass", config.RadiusNone, policy())
	if adv.Inferred {
		t.Fatalf("a builder must not be able to clear verification, got %s", adv.To)
	}
	if adv.Stall == "" {
		t.Error("a stall has to say why, or an operator sees an item that simply never moves")
	}
}

func TestAnUnknownRadiusStopsRatherThanApplying(t *testing.T) {
	adv := NextState(StateValidating, config.CapValidate, "pass", config.Radius("planetary"), policy())
	if adv.Inferred {
		t.Fatalf("an unrecognised radius must fail closed, got %s", adv.To)
	}
}

func TestNothingAnAgentReportsClearsAnApproval(t *testing.T) {
	for _, cap := range []string{config.CapImplement, config.CapVerify, config.CapValidate, config.CapOperate} {
		adv := NextState(StateAwaitingApproval, cap, "pass", config.RadiusFleet, policy())
		if adv.Inferred {
			t.Fatalf("%s moved an item out of awaiting_approval; only a person may", cap)
		}
	}
}

// --- the roadmap layer ----------------------------------------------------

func items(states ...string) []ledger.Item {
	var out []ledger.Item
	for i, s := range states {
		out = append(out, ledger.Item{ID: string(rune('a' + i)), State: s})
	}
	return out
}

func TestADeliverableIsNotBuiltUntilItsBreakdownIsReviewed(t *testing.T) {
	// Decomposed, but nobody has checked the breakdown against the brief.
	adv := NextSegmentState(SegResearching, items("queued", "queued"), "")
	if adv.To != SegPlanned {
		t.Fatalf("want planned, got %s", adv.To)
	}
	if SegPlanned.OpenForWork() {
		t.Fatal("a breakdown nobody has reviewed must not be dispatchable — this gate is the whole reason the roadmap layer exists")
	}
	// A reviewer accepts it, and only now does the work open.
	if adv := NextSegmentState(SegPlanned, items("queued"), "pass"); adv.To != SegApproved {
		t.Fatalf("a passing plan review should approve the deliverable, got %s", adv.To)
	}
	if !SegApproved.OpenForWork() {
		t.Fatal("an approved breakdown must be dispatchable")
	}
	// A reviewer rejects it, and it goes back to be decomposed again.
	if adv := NextSegmentState(SegPlanned, items("queued"), "reject"); adv.To != SegResearching {
		t.Fatalf("a rejected plan should go back for decomposition, got %s", adv.To)
	}
}

func TestADeliverableIsDeliveredOnlyWhenEveryItemIsFinished(t *testing.T) {
	if adv := NextSegmentState(SegBuilding, items("done", "in_progress"), ""); adv.Inferred {
		t.Fatalf("work is still in flight; nothing should move, got %s", adv.To)
	}
	if adv := NextSegmentState(SegBuilding, items("done", "done"), ""); adv.To != SegDelivered {
		t.Fatalf("want delivered, got %s", adv.To)
	}
	// Every item cancelled and none done is not a delivery.
	if adv := NextSegmentState(SegBuilding, items("cancelled", "cancelled"), ""); adv.Inferred {
		t.Fatal("a deliverable whose every item was cancelled has not been delivered")
	}
}

func TestProgressCountsFinishedWorkNotBusyWork(t *testing.T) {
	p := Progress(items("done", "in_progress", "verifying", "queued"))
	if p.Total != 4 || p.Done != 1 {
		t.Fatalf("unexpected counts: %+v", p)
	}
	if p.Percent() != 25 {
		t.Fatalf("want 25%%, got %d%% — the bar measures items finished, not items touched", p.Percent())
	}
	if p.ByStage["testing"] != 1 || p.ByStage["building"] != 1 || p.ByStage["idea"] != 1 {
		t.Fatalf("stage rollup wrong: %+v", p.ByStage)
	}
}

func TestEveryLifecycleStateMapsOntoAStageAPersonReads(t *testing.T) {
	for _, s := range AllStates() {
		if StageOf(s).Label == "" {
			t.Errorf("%s maps to no readable stage", s)
		}
	}
}
