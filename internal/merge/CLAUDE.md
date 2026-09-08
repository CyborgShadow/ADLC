# internal/merge

The one lane onto the trunk: rebase, re-gate on the rebased tree, fast-forward under a lock. It
takes a `GateFunc` rather than a gate, so the queue has no opinion about what a check is and can be
tested without one.

## What goes wrong here

**Gating the pre-rebase tree.** Work happens in isolated worktrees, so a branch cleared to merge
was gated against a tree that has since moved. A branch that was green before the rebase is not
green after it, and gating the pre-rebase tree checks a state that will never exist on the trunk.

**The silent revert.** A rebase can produce a tree that changes files the branch never touched,
restoring them to what they were when the branch started — undoing a sibling's landed work.
Duplicated work is loud, because two agents doing one job collide; reversion is silent, and every
check stays green throughout. So the invariant is checked directly: a branch may change only files
its own commits touch, and everything else in the rebased tree must still equal the trunk. That is
what `Clobbered` reports, and refusing leaves the trunk untouched.

**Holding the lock across the gate.** The lock is held only long enough to read the tip and move
it. A lock held across a full check run throttles the whole fleet behind one execution, and the
queue then grows faster than it drains. Because the gate therefore runs outside the lock, the tip
may have moved by the time it finishes — in which case the attempt is abandoned and retried, never
forced.

**Landing nothing quietly.** A branch with no commits on it is not a merge, and a fast-forward that
moved nothing is an error rather than a silent no-op. An empty merge that reports success is a
queue that appears to drain while work stays where it was.

**Inventing a refusal vocabulary.** `Outcome.Reason` uses the same closed vocabulary the transition
authority does, so a refusal reads identically wherever it surfaces.

## Tests

`merge_test.go` drives real git repositories: a clean branch fast-forwards, the clobber guard names
what would be reverted, a refusal to clobber leaves the trunk alone, two branches on disjoint files
both land, a branch that does not rebase is named a conflict, a gate that fails on the rebased tree
stops the merge, the tip moving during the gate abandons the attempt, a branch with nothing on it
is not a merge, a lock held by a live holder is not stolen, and landing nothing is an error rather
than a silent no-op.
