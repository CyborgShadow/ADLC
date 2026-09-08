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
