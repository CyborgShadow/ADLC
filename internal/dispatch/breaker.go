package dispatch

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// Noticing that the agents are not starting at all.
//
// Agent invocation broke fleet-wide at 12:29 one night and the fleet did not
// notice for ninety minutes. Every part of the loop behaved as designed, which
// is why nothing caught it: a dispatch that could not start an agent released
// its concurrency slot in three seconds, bumped no attempt counter, took no
// lease it kept, and left the item exactly as it found it — so the lanes fired
// on cadence at a hundred per cent failure. 212 runs in one hour, 120 in the
// next, half the night's total, 81 of them on a single item, and not one line
// anywhere said "the agent is not starting".
//
// Worse, the record actively said something else. 295 of the 297 failures
// carried an empty envelope_sha and lasted two or three seconds against a
// healthy run's five to twelve minutes, and every one of them was recorded with
// the same word a verifier uses for genuinely broken work.
//
// Four separate things follow, and they are separate on purpose.
//
// A dispatch that produced no envelope is UNKNOWN, not fail. That is the rule a
// killed run already gets — converting an absence into a verdict invents
// evidence, and here it invented 295 pieces of it.
//
// The refusal is recorded. The old path returned before recordRefusal was ever
// reached, so `no_envelope` stood at zero on the fleet report against 276 real
// occurrences: the largest failure mode in the ledger was visible on no
// surface at all.
//
// A lane that keeps producing nothing waits longer before trying again. Firing
// on cadence into a broken invocation spends a lease, a worktree and a slot per
// firing to learn the same thing.
//
// And past a point the fleet stops itself. A control plane that spends money
// for ninety minutes to learn nothing is worse than one that is down, because a
// fleet that is down is obvious.

// maxNoEnvelopeStreak is how many consecutive no-envelope dispatches the fleet
// tolerates before it asks itself to stop.
//
// Five. A healthy run takes five to twelve minutes and one that never reached
// an agent comes back in two or three seconds, so five in a row across every
// lane is not a run of bad luck — it is the invocation itself. Five is small
// enough that the failure this exists for costs a handful of runs rather than
// 332, and large enough that one timeout, one killed process and one genuinely
// crashed agent in a row do not stop a fleet that is working.
const maxNoEnvelopeStreak = 5

// stderrKept is how much of a failed agent's output reaches the chain.
//
// It used to reach only d.log. When invocation broke fleet-wide nobody could
// afterwards say WHAT broke, because the only account of it was in a console
// that had scrolled: the ledger held 276 identical shrugs. Four thousand bytes
// is what the runner's own tail buffer keeps, so this stores what there is
// rather than a second, shorter truncation of it.
const stderrKept = 4000

// sawEnvelope records that an agent produced a result, whatever that result
// said.
//
// A fail, a reject and a blocked verdict all mean the invocation works, so only
// the total absence of an envelope counts towards the breaker. A breaker that
// tripped on failing WORK would stop the fleet exactly when it was doing its
// job.
func (d *Dispatcher) sawEnvelope() { d.noEnv.Store(0) }

// sawNoEnvelope counts one dispatch that produced nothing, and reports the
// streak so the caller can decide what it has earned.
func (d *Dispatcher) sawNoEnvelope() int { return int(d.noEnv.Add(1)) }

// NoEnvelopeStreak is how many dispatches in a row have produced no envelope.
// Exported because a lane's backoff and the dashboard both read it.
func (d *Dispatcher) NoEnvelopeStreak() int { return int(d.noEnv.Load()) }

// tripBreaker stops the fleet once too many dispatches in a row have produced
// nothing.
//
// It reuses the drain path rather than inventing a second way to stop. Drain()
// halts new dispatch in THIS process at once, and the stop file says so to any
// other process sharing the runs directory — which is the case that matters,
// because a second control plane is one of the two explanations for observed
// concurrency exceeding the declared ceiling. Runs already in flight are left
// to finish: they are being paid for either way.
func (d *Dispatcher) tripBreaker(streak int) {
	// Saying it again on every firing is not saying it harder, and a second
	// stop file write would relitigate a decision already taken.
	if Draining() {
		return
	}
	Drain()
	why := fmt.Sprintf(
		"%d dispatches in a row produced no envelope. A run that never reached an agent returns in seconds, so this is the invocation failing rather than the work failing, and every further firing spends a lease, a worktree and a slot to learn the same thing. Dispatching nothing new; runs already in flight are left to finish.",
		streak)
	d.log("CIRCUIT BREAKER — %s", why)
	if err := d.AskToStop(); err != nil {
		// Worth saying: without the file, another process holding the same runs
		// directory keeps firing into the same broken invocation.
		d.log("CIRCUIT BREAKER could not write the stop signal (%v), so only this process is draining", err)
	}
	_, _ = d.Led.Append(d.processActor(), ledger.KindNoteRecorded, "dispatch", ledger.NoteRecorded{
		About: "circuit_breaker", Text: why,
	})
}

// backoff stretches a lane's cadence while dispatch is producing nothing.
//
// Doubling per consecutive failure: waiting costs nothing when the fleet is
// broken, and not waiting cost 332 runs. Capped at sixteen times the declared
// cadence so a fleet that recovers is one long sleep from firing again rather
// than an hour, and so a lane never becomes silent enough to look dead.
func backoff(cadence time.Duration, streak int) time.Duration {
	if streak <= 0 || cadence <= 0 {
		return cadence
	}
	if streak > 4 {
		streak = 4
	}
	return cadence << uint(streak)
}

// recordNoEnvelope puts everything known about a dispatch that produced no
// envelope onto the chain, and returns the detail for the caller's result.
//
// Both halves matter. The refusal is what makes `no_envelope` appear in the
// fleet report's reason table, which is where a person looks for what went
// wrong; the note is what carries the agent's own output, which is the only
// thing that can say why.
func (d *Dispatcher) recordNoEnvelope(runID string, c Candidate, agentErr error, out Result) string {
	detail := fmt.Sprintf("the agent returned an error and wrote no envelope: %v", agentErr)
	d.recordRefusal(runID, c, authority.ReasonNoEnvelope, detail)

	said := strings.TrimSpace(out.Stderr)
	if said == "" {
		// Distinguished deliberately from an agent that printed something
		// nobody kept: "it said nothing" is itself the diagnosis when a command
		// is missing or a binary will not launch.
		said = "(the agent printed nothing at all, which is what a command that does not exist looks like)"
	}
	_, _ = d.Led.Append(d.processActor(), ledger.KindNoteRecorded, runID, ledger.NoteRecorded{
		About: "no_envelope:" + runID,
		Text: fmt.Sprintf("%s\nexit code %d\n--- what the agent printed ---\n%s",
			detail, out.ExitCode, tail(said, stderrKept)),
	})
	return detail
}

// noteReportedCost records the figure the harness itself reported, beside the
// one the control plane re-priced from token counts.
//
// They are different claims and the difference is the whole point. The recorded
// total is a reconstruction from a default price table at list rates — a night
// reported as $1,242.94 was a reconstruction, not a bill. This is recorded
// rather than substituted: a reported figure nobody ran the numbers on does not
// become the cost, and an unpriced model still costs UNKNOWN rather than zero.
func (d *Dispatcher) noteReportedCost(runID string, out Result) {
	if !out.CostKnown {
		return
	}
	_, _ = d.Led.Append(d.processActor(), ledger.KindNoteRecorded, runID, ledger.NoteRecorded{
		About: "reported_cost:" + runID,
		Text: fmt.Sprintf("the agent harness reported this run cost $%.4f. Recorded beside the re-priced figure, not instead of it: one is what the tool was billed, the other is what this control plane derives from token counts and its own price table.",
			out.CostUSD),
	})
}

// salvageUsage reads the token counts out of an envelope the parser refused.
//
// A malformed envelope still describes a run that consumed tokens, and
// recording ledger.Usage{} for it put real eleven-minute runs on the bill at
// zero. Absence stays absence: nothing found stays nothing, so a run whose
// consumption is genuinely unknown does not acquire a confident zero here.
func salvageUsage(raw []byte) ledger.Usage {
	var probe struct {
		Usage ledger.Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ledger.Usage{}
	}
	return probe.Usage
}

// salvagedUsage reads the token counts out of a retained envelope blob.
//
// Used where a run is being closed by something other than itself: the claim
// cannot be admitted, but what it consumed is not a claim about the work and is
// worth keeping. A blob that is missing or unreadable yields nothing, which is
// the honest answer.
func (d *Dispatcher) salvagedUsage(sha string) ledger.Usage {
	if sha == "" {
		return ledger.Usage{}
	}
	body, err := d.Led.Blob(sha)
	if err != nil {
		return ledger.Usage{}
	}
	return salvageUsage(body)
}

// defaultModel is the model a run is assumed to have used when nothing said.
// A reaper runs on a Dispatcher that may have been built for one job, so the
// config is not assumed present.
func (d *Dispatcher) defaultModel() string {
	if d.Cfg == nil {
		return ""
	}
	return d.Cfg.Budget.DefaultModel
}

// priced converts usage to a cost, saying so when the model has no price entry.
//
// Zero tokens is not an unpriced run: it is a run that consumed nothing, and
// reporting it as a pricing gap would bury the real ones.
func (d *Dispatcher) priced(runID, model string, u ledger.Usage) int64 {
	if d.Cfg == nil {
		return 0
	}
	if model == "" {
		model = d.Cfg.Budget.DefaultModel
	}
	cost, ok := spend.Cost(d.Cfg.Budget, model, u)
	if !ok && (u.InputTokens > 0 || u.OutputTokens > 0) {
		d.log("UNPRICED %s used model %q, which has no price entry; its cost is unknown, not zero", runID, model)
	}
	return int64(cost)
}
