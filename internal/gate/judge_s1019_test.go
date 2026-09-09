package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// hangCheck declares a check whose command outlasts any budget worth waiting
// for. It runs the package's one hang helper — TestHangUntilKilled, with
// hangCmd and hangEnv in gate_test.go — rather than a second copy of it: two
// processes that exist only to be killed are two things to keep in step, and
// the next reader has to diff them to discover they are the same process.
func hangCheck(t *testing.T, id string, timeoutSeconds int) *config.Check {
	t.Helper()
	return check(t, config.Check{
		ID:             id,
		Command:        hangCmd(t),
		Env:            []string{hangEnv},
		Verdict:        config.VerdictExitZero,
		TimeoutSeconds: timeoutSeconds,
	})
}

// TestJudgeAC1RunBudgetIsNotBlamedOnTheCheck is AC-1's observable: with a
// parent context whose deadline has already passed and a check declaring a
// 900-second budget, Why must say the RUN's budget was exhausted and must not
// claim the check timed out after 15m0s. Written from the criterion, not from
// the implementation: it asserts on the two things the criterion names.
func TestJudgeAC1RunBudgetIsNotBlamedOnTheCheck(t *testing.T) {
	ch := hangCheck(t, "ac1", 900)
	if got := ch.Timeout(); got.String() != "15m0s" {
		t.Fatalf("precondition: a 900s check must render as 15m0s, got %s", got)
	}

	// A deadline a second in the past. No sleep, no clock to wait on.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	r := &Runner{Cfg: mustConfig(t, []config.Check{*ch}), Dir: "."}
	obs := r.runOne(ctx, ch)

	if strings.Contains(obs.Why, "timed out after") {
		t.Errorf("AC-1: Why blames the check for a timeout it was never given a second of: %q", obs.Why)
	}
	low := strings.ToLower(obs.Why)
	if !strings.Contains(low, "run") || !strings.Contains(low, "budget") {
		t.Errorf("AC-1: Why does not say the run's budget was exhausted: %q", obs.Why)
	}
	if !strings.Contains(low, "exhaust") {
		t.Errorf("AC-1: Why does not say the budget was exhausted: %q", obs.Why)
	}
	if obs.Verdict == StatusGreen {
		t.Errorf("AC-1: a check that never ran must not be GREEN, got %s (%s)", obs.Verdict, obs.Why)
	}
	if obs.Ran {
		t.Errorf("AC-1: Ran must be false for a check the run had no budget left to give: %+v", obs)
	}
	t.Logf("AC-1 observed: verdict=%s ran=%v timed_out=%v why=%q", obs.Verdict, obs.Ran, obs.TimedOut, obs.Why)
}

// TestJudgeAC2ARealHangIsStillRed is AC-2's observable: a live parent and a
// check whose own budget is one second against a command that outlasts it must
// still read 'timed out after 1s' and must still be RED. A fix that
// reclassified every timeout as "out of budget" fails here.
func TestJudgeAC2ARealHangIsStillRed(t *testing.T) {
	ch := hangCheck(t, "ac2", 1)

	// Live parent, with far more budget left than the check's own one second.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	r := &Runner{Cfg: mustConfig(t, []config.Check{*ch}), Dir: "."}
	obs := r.runOne(ctx, ch)

	if obs.Why != "timed out after 1s" {
		t.Errorf("AC-2: Why should read exactly %q, got %q", "timed out after 1s", obs.Why)
	}
	if obs.Verdict != StatusRed {
		t.Errorf("AC-2: a real hang is RED, got %s (%s)", obs.Verdict, obs.Why)
	}
	if !obs.TimedOut {
		t.Errorf("AC-2: TimedOut must be true for a command that outlasted its own budget: %+v", obs)
	}
	t.Logf("AC-2 observed: verdict=%s ran=%v timed_out=%v why=%q dur=%dms", obs.Verdict, obs.Ran, obs.TimedOut, obs.Why, obs.DurationMS)
}
