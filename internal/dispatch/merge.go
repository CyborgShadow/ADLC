package dispatch

import (
	"context"
	"fmt"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/gate"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/merge"
)

// MergeResult is what one pass of the merge queue did.
type MergeResult struct {
	Landed   []string
	Requeued []string
	Returned []string
	// Idle explains an empty pass, so "nothing merged" is never silent.
	Idle string
}

// Merge drains the ready_to_merge queue.
//
// This is the control plane's own step. No agent performs it and none is asked
// to: merging is arithmetic over refs, and the one judgement in it — whether a
// rebase would silently revert a sibling's landed work — is a comparison, not
// an opinion. An agent invited to decide it would sometimes decide wrong, and
// the failure is invisible because every gate stays green either way.
//
// Items are taken oldest first, one at a time. A parallel merge queue is a
// contradiction: the whole point is that exactly one tree at a time is the tree
// that would land.
func (d *Dispatcher) Merge(ctx context.Context) (MergeResult, error) {
	var res MergeResult
	items, err := d.Led.Items("")
	if err != nil {
		return res, err
	}
	var queue []ledger.Item
	for _, it := range items {
		if authority.State(it.State) == authority.StateReadyToMerge {
			queue = append(queue, it)
		}
	}
	if len(queue) == 0 {
		res.Idle = "no item is cleared to merge"
		return res, nil
	}

	trunk := d.Cfg.Dispatch.Trunk
	if trunk == "" {
		trunk = "main"
	}
	q := &merge.Queue{
		Repo: d.Repo, Trunk: trunk,
		Gate: d.mergeGate(), Now: d.now, Log: func(s string) { d.log("%s", s) },
	}
	for _, it := range queue {
		branch, err := d.branchFor(it.ID)
		if err != nil {
			return res, err
		}
		if branch == "" {
			d.holdMerge(it, authority.ReasonMergeConflict,
				"no run recorded a branch for this item, so there is nothing to land")
			continue
		}
		if err := d.controlMove(it, authority.StateReadyToMerge, authority.StateMerging,
			"the merge queue took "+branch); err != nil {
			return res, err
		}
		out, err := q.Land(ctx, branch)
		if err != nil {
			return res, fmt.Errorf("land %s: %w", branch, err)
		}
		switch {
		case out.Landed:
			if err := d.controlMove(it, authority.StateMerging, authority.StateMerged, fmt.Sprintf(
				"%s rebased onto %s, re-gated on the rebased tree, and fast-forwarded to %s",
				branch, trunk, out.MergedSHA)); err != nil {
				return res, err
			}
			res.Landed = append(res.Landed, it.ID)

		case out.Reason == "stale_base_clobber":
			// Not retryable on its own: the same rebase produces the same
			// reversion every time. It goes back to a builder with the paths
			// named, because a person or an agent has to look at what came back.
			if err := d.leaveMerging(it, authority.StateInProgress,
				authority.ReasonStaleBaseClobber, out.Detail); err != nil {
				return res, err
			}
			res.Returned = append(res.Returned, it.ID)

		case out.Reason == "merge_gate_failed" && !out.Retryable:
			// Green before the rebase and red after it is a real conflict with
			// what landed in between, not a transient.
			if err := d.leaveMerging(it, authority.StateInProgress,
				authority.ReasonMergeGateFailed, out.Detail); err != nil {
				return res, err
			}
			res.Returned = append(res.Returned, it.ID)

		default:
			reason := authority.ReasonMergeConflict
			switch out.Reason {
			case "trunk_moved":
				reason = authority.ReasonTrunkMoved
			case "merge_gate_failed":
				reason = authority.ReasonMergeGateFailed
			}
			if err := d.leaveMerging(it, authority.StateReadyToMerge, reason, out.Detail); err != nil {
				return res, err
			}
			res.Requeued = append(res.Requeued, it.ID)
		}
	}
	return res, nil
}

// mergeGate re-runs the project's own checks over the rebased tree.
//
// Gating the pre-rebase tree checks a state that will never exist on the trunk,
// so the checks run again on the tree that would actually land.
func (d *Dispatcher) mergeGate() merge.GateFunc {
	from, to := string(authority.StateMerging), string(authority.StateMerged)
	checks := d.Cfg.ChecksForEdge(from, to)
	if len(checks) == 0 {
		// Fall back to whatever gates work leaving the builder. A merge queue
		// with no checks fast-forwards anything, which is most of what this
		// step exists to prevent.
		from, to = string(authority.StateInProgress), string(authority.StateReadyForTesting)
		checks = d.Cfg.ChecksForEdge(from, to)
	}
	if len(checks) == 0 {
		return nil
	}
	edge := from + "->" + to
	return func(ctx context.Context, dir string) (bool, string, error) {
		g := &gate.Runner{Cfg: d.Cfg, Dir: dir}
		r, err := g.RunEdge(ctx, from, to)
		if err != nil {
			return false, "", err
		}
		return r.Green(), fmt.Sprintf("%s came back %s", edge, r.Status), nil
	}
}

// branchFor finds the ref an item's work lives on: the branch of the most
// recent run against it that recorded one.
func (d *Dispatcher) branchFor(itemID string) (string, error) {
	runs, err := d.Led.Runs(itemID, 0)
	if err != nil {
		return "", err
	}
	for _, r := range runs {
		if r.Branch != "" {
			return r.Branch, nil
		}
	}
	return "", nil
}

// leaveMerging records both halves of a refusal: the reason, in the same place
// every other refusal in the system lives, and the state the item goes back to.
func (d *Dispatcher) leaveMerging(it ledger.Item, to authority.State, reason authority.Reason, detail string) error {
	if _, err := d.Led.Append(d.Actor, ledger.KindTransitionRefused, it.ID, ledger.TransitionOutcome{
		ItemID: it.ID, From: string(authority.StateMerging), To: string(authority.StateMerged),
		Reason: string(reason), Detail: detail,
	}); err != nil {
		return err
	}
	d.log("MERGE REFUSED %s [%s] %s", it.ID, reason, detail)
	return d.controlMove(it, authority.StateMerging, to, string(reason)+": "+detail)
}

// holdMerge refuses without moving the item, for the case where there is
// nothing to attempt at all.
func (d *Dispatcher) holdMerge(it ledger.Item, reason authority.Reason, detail string) {
	_, _ = d.Led.Append(d.Actor, ledger.KindTransitionRefused, it.ID, ledger.TransitionOutcome{
		ItemID: it.ID, From: string(authority.StateReadyToMerge), To: string(authority.StateMerging),
		Reason: string(reason), Detail: detail,
	})
	d.log("MERGE HELD %s [%s] %s", it.ID, reason, detail)
}

// controlMove takes an edge in the control plane's own name. It still goes
// through the transition authority: a step that writes a state directly is a
// step the record cannot explain.
func (d *Dispatcher) controlMove(it ledger.Item, from, to authority.State, why string) error {
	now := d.now()
	facts, err := authority.Gather(d.Led, it.ID, "", nil, "", now)
	if err != nil {
		return err
	}
	// The queue is the thing that establishes this, and it has just done so.
	facts.MergeClean = to == authority.StateMerged
	dec := authority.New(d.Cfg).Decide(authority.Request{
		Actor: d.Actor, From: from, To: to, Reason: why, Now: now,
	}, facts)
	if !dec.Admitted {
		_, _ = d.Led.Append(d.Actor, ledger.KindTransitionRefused, it.ID, ledger.TransitionOutcome{
			ItemID: it.ID, From: string(from), To: string(to),
			Reason: string(dec.Reason), Detail: dec.Detail,
		})
		return fmt.Errorf("%s %s -> %s refused [%s] %s", it.ID, from, to, dec.Reason, dec.Detail)
	}
	if _, err := d.Led.Append(d.Actor, ledger.KindTransitionAdmitted, it.ID, ledger.TransitionOutcome{
		ItemID: it.ID, From: string(from), To: string(to),
	}); err != nil {
		return err
	}
	_, err = d.Led.Append(d.Actor, ledger.KindItemTransitioned, it.ID, ledger.ItemTransitioned{
		ItemID: it.ID, From: string(from), To: string(to), Reason: why,
	})
	return err
}
