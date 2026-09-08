package authority

import (
	"time"

	"github.com/CyborgShadow/adlc/internal/ledger"
)

// Gather reads the facts a decision needs out of the ledger.
//
// It is separate from Decide so that the rules are a pure function of stated
// inputs. That is not tidiness: a rule that runs its own query is a rule that
// cannot be tested without a database, and rules that cannot be tested cheaply
// are rules nobody writes a negative case for.
func Gather(l *ledger.Ledger, itemID, runID string, commitReachable func(string) bool, headSHA string, now time.Time) (Facts, error) {
	var f Facts
	item, err := l.Item(itemID)
	if err != nil {
		return f, err
	}
	f.Item = item

	qs, err := l.Questions(itemID, true)
	if err != nil {
		return f, err
	}
	for _, q := range qs {
		if q.Blocking {
			f.OpenBlockingQs = append(f.OpenBlockingQs, q)
		}
	}

	if f.Approvals, err = l.Approvals(itemID); err != nil {
		return f, err
	}

	// The run that last drove this item into verification is the run whose work
	// is now being judged. Naming it explicitly is what lets the authority refuse
	// a verifier that is also the author.
	props, err := l.Proposals(itemID, false, 0)
	if err != nil {
		return f, err
	}
	for _, p := range props {
		if p.Admitted && State(p.To) == StateVerifying {
			f.ImplementRunID = p.RunID
			break // newest first
		}
	}

	if runID != "" {
		started, err := l.RunExists(runID)
		if err != nil {
			return f, err
		}
		f.RunStarted = started
	}

	f.DepStates = map[string]State{}
	for _, dep := range item.DependsOn {
		d, err := l.Item(dep)
		if err != nil {
			continue // absent dependencies are reported as "no such item"
		}
		f.DepStates[dep] = State(d.State)
	}

	if commitReachable != nil && headSHA != "" {
		f.CommitReachable = commitReachable(headSHA)
	}

	dayStart := now.Add(-24 * time.Hour)
	if f.SpendDayMicros, err = l.SpendMicros(dayStart, ""); err != nil {
		return f, err
	}
	if f.SpendSegmentMicros, err = l.SpendMicros(time.Time{}, item.SegmentID); err != nil {
		return f, err
	}
	return f, nil
}

// ReadyItems computes which queued items have their dependencies satisfied.
//
// Readiness is COMPUTED and recomputed; no agent proposes it. That is what
// keeps "not started because its dependency is open" a different visible fact
// from "not started because nobody picked it".
func ReadyItems(l *ledger.Ledger) ([]ledger.Item, error) {
	items, err := l.Items("")
	if err != nil {
		return nil, err
	}
	state := map[string]State{}
	for _, it := range items {
		state[it.ID] = State(it.State)
	}
	var out []ledger.Item
	for _, it := range items {
		if State(it.State) != StateQueued {
			continue
		}
		ok := true
		for _, dep := range it.DependsOn {
			if state[dep] != StateDone {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// noteRunStarted is documented on Facts.RunStarted; see Gather.
