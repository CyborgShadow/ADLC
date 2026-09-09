package dispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/gate"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Settling acceptance criteria without dispatching anybody.
//
// A criterion that names a command and how to read it is a thing the control
// plane can run. It was not running them: it dispatched a judge, handed it the
// criteria as prose, and recorded what the judge said it saw. Ten to fifteen
// minutes of agent time, per item, to execute commands that were already
// written down — and the result was a claim rather than an observation, which
// is the one shape this project refuses everywhere else.
//
// So they are run here, in the item's own tree, by the same executor and the
// same verdict rules the declared checks use. A judge is still dispatched for
// criteria no command can settle, and for nothing else.
//
// What this does NOT do is decide the item's state. It records what was
// observed; `authority` decides what that earns, exactly as it does for a gate
// result. Verification moving from an agent to the control plane must not also
// move the decision.

// checksFor turns an item's executable criteria into checks the gate can run.
func checksFor(cs []authority.Criterion) []config.Check {
	var out []config.Check
	for i, c := range cs {
		if !c.Executable() {
			continue
		}
		out = append(out, config.Check{
			ID: fmt.Sprintf("AC-%d", i+1), Command: c.Command,
			Verdict: c.Rule, ExpectPattern: c.Want,
		})
	}
	return out
}

// acceptance runs an item's executable criteria in dir and records the result.
//
// Returns the observation and how many criteria still need somebody's
// judgement. Nothing here refuses or admits anything.
func (d *Dispatcher) acceptance(ctx context.Context, runID string, it ledger.Item, dir string) (*gate.Result, int, error) {
	parsed := authority.ParseCriteria(it.Criteria)
	checks := checksFor(parsed)
	needJudge := len(authority.NeedsJudgement(parsed))
	if len(checks) == 0 {
		return nil, needJudge, nil
	}

	r := &gate.Runner{Cfg: d.Cfg, Dir: dir, Vars: map[string]string{"workdir": dir}}
	res := r.RunCriteria(ctx, checks)

	// Recorded as an observation, in the same shape and the same event kind a
	// gate run is recorded in, so one reader answers both questions.
	if _, err := d.Led.Append(d.actor(), ledger.KindGateObserved, runID, ledger.GateObserved{
		RunID: runID, ItemID: it.ID, Edge: "acceptance",
		TreeSHA: res.TreeSHA, Dirty: res.Dirty,
		Status: string(res.Status), Checks: res.JSON(),
	}); err != nil {
		return res, needJudge, err
	}
	d.log("ACCEPTANCE %s — %s on %s, no agent involved%s",
		it.ID, res.Status, plural(len(checks), "criterion", "criteria"),
		judgeNote(needJudge))
	return res, needJudge, nil
}

func judgeNote(n int) string {
	if n == 0 {
		return "; every criterion was executable, so no judge is needed"
	}
	return fmt.Sprintf("; %s need judgement and a judge is dispatched for those", plural(n, "criterion", "criteria"))
}

// failedCriteria names what did not hold, for the refusal message.
func failedCriteria(res *gate.Result) string {
	if res == nil {
		return ""
	}
	var out []string
	for _, o := range res.Checks {
		if o.Verdict != gate.StatusGreen {
			out = append(out, fmt.Sprintf("%s %s (%s)", o.CheckID, o.Verdict, o.Why))
		}
	}
	return strings.Join(out, "; ")
}
