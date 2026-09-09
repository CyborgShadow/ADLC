package authority

import (
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
)

// What a rejection has to be able to do, and what it has to cite.
//
// Two defects met here, and they compound: the role allowed to reject could not
// reach the rejecting edge, and the evidence rule underneath that edge counted
// one severity out of the several that mean the same thing. Between them, every
// rejection the fleet produced in fourteen hours was thrown away.

// TestAValidatorsRejectionIsAdmittedRatherThanRefused pins the routing defect.
//
// NextState sent every setback to the verification self-edge, and the
// validator's form of that edge requires ReqVerdictPass — so a rejection
// arrived as a malformed proposal and was refused as one. Six adversarial
// reviews were spent and discarded that way, one of them by a run that had
// diagnosed this defect in the envelope that was then thrown away.
//
// Both halves are asserted, because either alone passes vacuously: the route
// must be verifying -> rejected, AND the authority must admit it.
func TestAValidatorsRejectionIsAdmittedRatherThanRefused(t *testing.T) {
	adv := NextState(StateVerifying, config.CapValidate, "reject", config.RadiusNone, policy())
	if adv.To != StateRejected {
		t.Fatalf("a validator's reject must reach the edge declared for it, got %s (%s)", adv.To, adv.Stall)
	}

	rejecting := env(t, func(m map[string]any) {
		m["worker_type"] = "validator"
		m["verdict"] = "reject"
		m["outputs"] = map[string]any{"findings": []map[string]any{{
			"severity": "blocker", "location": "site/cats/index.html:14",
			"evidence":        "the sources section names no licence",
			"required_change": "name the licence beside each photograph",
		}}}
	})
	d := New(cfg(t)).Decide(Request{
		Worker: "validator", RunID: "v-1", From: StateVerifying, To: adv.To,
		Env: rejecting, Gate: greenGate(), Reason: adv.Why, Now: now,
	}, Facts{RunStarted: true, Item: item("verifying"), CommitReachable: true, ImplementRunID: "p-9"})
	if !d.Admitted {
		t.Fatalf("a substantiated rejection must be admitted, got [%s] %s", d.Reason, d.Detail)
	}
}

// TestAFailingVerificationTaskStillHoldsTheStage is the clean case for the
// routing above, and it is the constraint the fix had to leave standing. A task
// that merely failed must not eject the item, because two siblings are still
// examining the same commit and ejecting refuses them as stale: two runs
// discarded, and the builder handed one complaint out of three.
func TestAFailingVerificationTaskStillHoldsTheStage(t *testing.T) {
	for _, cap := range VerificationCapabilities() {
		if adv := NextState(StateVerifying, cap, "fail", config.RadiusNone, policy()); adv.To != StateVerifying {
			t.Errorf("%s reported fail and the item moved to %s; the stage settles when every task has reported", cap, adv.To)
		}
	}
	// And only the validator may reject: the rejecting edge names it as its
	// sole proposer, so routing anybody else there would swap one refusal for
	// another and fix nothing.
	for _, cap := range []string{config.CapTest, config.CapJudge} {
		if adv := NextState(StateVerifying, cap, "reject", config.RadiusNone, policy()); adv.To != StateVerifying {
			t.Errorf("%s reported reject and the item moved to %s; rejection is adversarial review's", cap, adv.To)
		}
	}
}

// TestARejectionOnAMajorFindingIsNotTaste pins the vocabulary defect.
// ReqBlockerFinding counted only "blocker", while the envelope's own severity
// map reads major, medium, moderate and warning as "major" — so an arbiter that
// rejected on one major finding was told it was rejecting on taste alone. It
// did that six times, at 8m44s and $3.72 each.
//
// The clean case is underneath: minor and note really are taste, and a
// rejection citing nothing heavier is still refused. Without that half this
// test would pass just as well on a rule that had stopped checking anything.
func TestARejectionOnAMajorFindingIsNotTaste(t *testing.T) {
	for _, heavy := range []string{"blocker", "major", "medium", "warning"} {
		if d := arbiterRejects(t, heavy); !d.Admitted {
			t.Errorf("a rejection citing a %q finding must stand, got [%s] %s", heavy, d.Reason, d.Detail)
		}
	}
	for _, light := range []string{"minor", "note"} {
		d := arbiterRejects(t, light)
		if d.Admitted {
			t.Errorf("a rejection citing only a %q finding is taste and must be refused", light)
		}
		if !strings.Contains(d.Detail, "blocker or major") {
			t.Errorf("the refusal must say what would have counted: %q", d.Detail)
		}
	}
}

// TestOnlyABlockerContradictsAPass is the other half of the same vocabulary,
// drawn deliberately in a different place. "blocker" is the one severity named
// for what it does, so filing one and reporting a pass in the same envelope is
// a contradiction. A "major" alongside a pass is a reviewer recording a real
// concern and judging the change coherent anyway, which is its job — and
// refusing that would stall changes everybody had agreed on.
func TestOnlyABlockerContradictsAPass(t *testing.T) {
	if d := arbiterPasses(t, "blocker"); d.Admitted {
		t.Error("a blocker finding and a pass verdict in one envelope is a contradiction")
	}
	if d := arbiterPasses(t, "major"); !d.Admitted {
		t.Errorf("a major finding is a noted concern, not a veto on a pass: [%s] %s", d.Reason, d.Detail)
	}
}

func findingEnv(t *testing.T, severity, verdict string) *envelope.Envelope {
	t.Helper()
	return env(t, func(m map[string]any) {
		m["worker_type"] = "arbiter"
		m["verdict"] = verdict
		m["plan_digest"] = "d1d1d1d1d1d1"
		m["outputs"] = map[string]any{"findings": []map[string]any{{
			"severity": severity, "location": "internal/ledger/read.go:88",
			"evidence":        "a projection is written outside apply()",
			"required_change": "move the write into apply()",
		}}}
	})
}

func arbiterRejects(t *testing.T, severity string) Decision {
	t.Helper()
	return New(cfg(t)).Decide(Request{
		Worker: "arbiter", RunID: "a-1", From: StateArbitrating, To: StateRejected,
		Env: findingEnv(t, severity, "reject"), Gate: greenGate(),
		Reason: "the change conflicts with the system as a whole", Now: now,
	}, Facts{RunStarted: true, Item: item("arbitrating"), CommitReachable: true})
}

func arbiterPasses(t *testing.T, severity string) Decision {
	t.Helper()
	return New(cfg(t)).Decide(Request{
		Worker: "arbiter", RunID: "a-2", From: StateArbitrating, To: StateReadyToMerge,
		Env: findingEnv(t, severity, "pass"), Gate: greenGate(),
		Reason: "coherent with the rest of the system", Now: now,
	}, Facts{RunStarted: true, Item: item("arbitrating"), CommitReachable: true})
}
