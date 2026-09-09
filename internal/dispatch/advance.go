package dispatch

import (
	"fmt"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Closing the gaps where nothing was moving work along.
//
// The lifecycle is driven by polling: a lane fires, looks for items in a state
// its capability drains, and takes one. That works for every state a capability
// answers for — and it silently does not work for the states where the answer
// is "a person". Two of those were stranding work.
//
// A blocked item waits for a question to be answered. Answering it appended the
// answer and nothing else, so the item stayed blocked: the Questions page said
// "Answer and unblock" and delivered the first half. A rejected item waits for
// rework to be routed, and nothing routed it — the attempt limit exists to
// bound automatic rework that was never happening.
//
// Both are computed here rather than pushed from the handler that caused them,
// for the same reason readiness is: a state the tool can compute, it computes,
// on every pass, from the record. A handler that pushes gets it right once; a
// pass that recomputes gets it right after a crash, after a restart, and after
// somebody answers a question with the CLI instead of the dashboard.

// resumeBlocked moves items whose blocking questions have all been answered
// back to where they were blocked from.
func (d *Dispatcher) resumeBlocked(items []ledger.Item) (int, error) {
	moved := 0
	for _, it := range items {
		if authority.State(it.State) != authority.StateBlocked {
			continue
		}
		open, err := d.Led.Questions(it.ID, true)
		if err != nil {
			return moved, err
		}
		if len(open) > 0 {
			continue
		}
		back, err := d.blockedFrom(it.ID)
		if err != nil {
			return moved, err
		}
		if back == "" {
			// Blocked with no recorded arrival is not something to guess about.
			// It is reported rather than resumed into an invented state.
			d.log("STUCK %s is blocked and the record does not say what it was doing; a person has to place it", it.ID)
			continue
		}
		why := "every blocking question on this item has been answered"
		if err := d.pmMove(it, authority.StateBlocked, back, why); err != nil {
			d.log("RESUME REFUSED %s -> %s: %v", it.ID, back, err)
			continue
		}
		moved++
		d.log("RESUMED %s -> %s (%s)", it.ID, back, why)
	}
	return moved, nil
}

// routeRework sends a rejected item back to a builder while it still has
// attempts left.
//
// The alternative is that every rejection needs a person, which makes the
// attempt limit decorative and means a fleet left running overnight stops at
// the first blocker rather than trying again. Past the limit it stays put: that
// is the limit doing its job, and the item then genuinely does need somebody.
func (d *Dispatcher) routeRework(items []ledger.Item) (int, error) {
	moved := 0
	for _, it := range items {
		if authority.State(it.State) != authority.StateRejected {
			continue
		}
		if it.Attempts >= d.Cfg.Dispatch.MaxAttempts {
			// Past the limit escalateAtLimit takes it. Leaving it here was the
			// dead end: the limit stopped the rework, nothing told anybody, and
			// the item stayed rejected with no question against it.
			continue
		}
		why := fmt.Sprintf("review sent it back; attempt %d of %d",
			it.Attempts+1, d.Cfg.Dispatch.MaxAttempts)
		if err := d.pmMove(it, authority.StateRejected, authority.StateInProgress, why); err != nil {
			d.log("REWORK REFUSED %s: %v", it.ID, err)
			continue
		}
		moved++
		d.log("REWORK %s -> in_progress (%s)", it.ID, why)
	}
	return moved, nil
}

// reworkQuestionID is the question raised when an item runs out of attempts.
//
// The attempt count is in the id so that answering it buys the item another
// look rather than silencing the limit for good: if the next round exhausts a
// raised limit, a NEW question is asked instead of an old answer quietly
// covering it.
func reworkQuestionID(itemID string, attempts int) string {
	return fmt.Sprintf("Q-%s-rework-limit-%d", itemID, attempts)
}

// escalateAtLimit parks an item that has used every rework attempt, and asks a
// person about it.
//
// This is the hole the rework limit left. The limit was enforced by making the
// item invisible — the selection pass skipped anything at the limit, in every
// state, for every capability — and nothing anywhere proposed the edge the
// transition table has declared since the beginning: rejected -> blocked, "the
// attempt limit is reached; a person decides". So two items sat in in_progress
// at 3 of 3 that no lane could see and no surface mentioned, which is the exact
// failure mode this tool exists to make impossible: work that stops with no
// stated reason looks identical to work nobody has got to yet.
//
// A blocking question rather than a state change alone, for two reasons. It is
// what puts the item on the Questions page, where somebody is actually looking.
// And for the states that are not `rejected` — an item at the limit part-way
// through a build — it is the only thing that can stop the loop, because the
// edge into blocked from an active state requires an envelope stating what the
// block is, and the control plane has none to offer. The open question parks
// those items exactly as it parks any other, and the record says why.
func (d *Dispatcher) escalateAtLimit(items []ledger.Item) (int, error) {
	limit := d.Cfg.Dispatch.MaxAttempts
	if limit <= 0 {
		// No limit declared means no limit reached. Treating zero as "every item
		// is over budget" would block the whole roster on first contact.
		return 0, nil
	}
	moved := 0
	for _, it := range items {
		st := authority.State(it.State)
		// Only the two states that are waiting on ANOTHER build attempt. A stage
		// in verification is reviewing work already paid for and must be allowed
		// to finish; blocking it would strand the item somewhere else instead.
		if st != authority.StateRejected && st != authority.StateInProgress {
			continue
		}
		if it.Attempts < limit {
			continue
		}
		id := reworkQuestionID(it.ID, it.Attempts)
		if q, err := d.Led.Question(id); err == nil && q.ID != "" {
			continue // already asked; asking again every pass is not asking harder
		}
		text := fmt.Sprintf(
			"%s (%s) has used all %d of its rework attempts and is still not through review. The fleet will not build it again on its own. Is the item wrong, is the bar wrong for work of this size, or is something in the environment failing in a way no amount of rework will fix?",
			it.ID, it.Title, limit)
		lean := fmt.Sprintf(
			"Read the last rejection before deciding. Answering this releases %s for another round of attempts; if the same objection has come back three times, the plan for it is what needs changing rather than the attempt count.",
			it.ID)
		if _, err := d.Led.Append(d.actor(), ledger.KindQuestionRaised, id, ledger.QuestionRaised{
			ID: id, ItemID: it.ID, Blocking: true, Text: text, Lean: lean, RaisedBy: it.ID,
		}); err != nil {
			// Swallowing this would leave the item exactly as stuck as before,
			// with the added lie that something had handled it.
			d.log("ESCALATION %s could not be recorded: %v — the item will look idle for no reason", it.ID, err)
			continue
		}
		moved++
		// Only `rejected` has a declared edge into blocked that a coordinator
		// can drive on a stated reason alone. From anywhere else the open
		// question is the park, and inventing an edge for it here is exactly
		// what the authority package exists to prevent.
		if st == authority.StateRejected {
			why := fmt.Sprintf("the attempt limit is reached: %d of %d rework attempts used, and %s asks who should decide",
				it.Attempts, limit, id)
			if err := d.pmMove(it, authority.StateRejected, authority.StateBlocked, why); err != nil {
				d.log("ESCALATION REFUSED %s -> blocked: %v — %s is still raised, so it is parked and visible either way", it.ID, err, id)
				continue
			}
		}
		d.log("AT LIMIT %s has used %d of %d rework attempts. Raised %s and parked it; answering releases it for another round.",
			it.ID, it.Attempts, limit, id)
	}
	return moved, nil
}

// blockedFrom reads the state an item was in when it blocked.
func (d *Dispatcher) blockedFrom(itemID string) (authority.State, error) {
	props, err := d.Led.Proposals(itemID, false, 0)
	if err != nil {
		return "", err
	}
	var from authority.State
	var seq int64
	for _, p := range props {
		if p.Admitted && p.To == string(authority.StateBlocked) && p.Seq > seq {
			from, seq = authority.State(p.From), p.Seq
		}
	}
	// A state that is not somewhere work can resume is no better than none.
	if from != "" && !from.Active() && from != authority.StateReady {
		return "", nil
	}
	return from, nil
}

// pmMove drives an edge in the coordinator's name, through the authority.
//
// Anything arriving in in_progress from somewhere other than ready spends an
// attempt. That is what makes the rework budget real: without the bump, routing
// a rejection back would loop between review and building forever, and the
// attempt limit would refuse nothing because the counter never moved.
func (d *Dispatcher) pmMove(it ledger.Item, from, to authority.State, why string) error {
	now := d.now()
	facts, err := authority.Gather(d.Led, it.ID, "", nil, "", now)
	if err != nil {
		return err
	}
	dec := authority.New(d.Cfg).Decide(authority.Request{
		Actor: d.Actor, AsPM: true, From: from, To: to, Reason: why, Now: now,
	}, facts)
	if !dec.Admitted {
		_, _ = d.Led.Append(d.Actor, ledger.KindTransitionRefused, it.ID, ledger.TransitionOutcome{
			ItemID: it.ID, From: string(from), To: string(to),
			Reason: string(dec.Reason), Detail: dec.Detail,
		})
		return fmt.Errorf("[%s] %s", dec.Reason, dec.Detail)
	}
	if _, err := d.Led.Append(d.Actor, ledger.KindTransitionAdmitted, it.ID, ledger.TransitionOutcome{
		ItemID: it.ID, From: string(from), To: string(to),
	}); err != nil {
		return err
	}
	_, err = d.Led.Append(d.Actor, ledger.KindItemTransitioned, it.ID, ledger.ItemTransitioned{
		ItemID: it.ID, From: string(from), To: string(to), Reason: why,
		BumpAttempt: to == authority.StateInProgress && from != authority.StateReady,
	})
	return err
}
