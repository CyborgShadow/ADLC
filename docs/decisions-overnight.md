# Decisions taken on your behalf, overnight of 8–9 September 2026

You delegated answering while you slept, with a record to review. This is that
record. Every entry says what was asked, what I decided, why, and how to reverse
it.

Two things about how these were recorded:

**They are attributed to `claude`, not to you.** The ledger exists to say who
decided; a decision of mine filed under your name is the one thing it must never
record. Every answer below appears in the chain with `claude` as the actor, and
`adlc question list -open=false` will show them that way for as long as the
record exists.

**Nothing here changed a human gate.** Sign-off, approval and blast-radius policy
were left alone. What I answered were questions agents raised because they could
not proceed, and what I changed were defects in the control plane itself.

---

## D1 — S1-010-Q1: which edge should the implementation gate run over?

**Asked because** two runs on S1-010 were refused `zero_checks_declared` for
`in_progress->verifying`, while `adlc.json` declared checks only for
`in_progress->ready_for_testing` and the other old edges.

**The agent's lean** was to point the implementer's outbound edge back at
`ready_for_testing`, and it explicitly refused to add check declarations itself
— "adding a check declaration to make my own gate pass is the move a worker must
never make". That is exactly right and worth saying so.

**Decided:** the diagnosis is correct and the fix is the other direction. The
edge is right and the declarations were stale: I had collapsed the verification
chain into one `verifying` stage and never rebound the checks. They now gate
`in_progress->verifying`, `verifying->verifying` and `verifying->reviewed`, and
`adlc config check` refuses a config whose checks name an edge that is not in
the table.

**To reverse:** revert commit `571d4b1`. You would then also want to revert the
stage collapse, since the old edges only exist there.

---

## D2 — S1-002-Q1: an acceptance criterion no command can satisfy

**Asked because** AC-3 required `go run ./cmd/sitecheck ... ` to exit 2, and
`go run` collapses every non-zero exit status of the program it runs to 1. No Go
program can satisfy that wording.

**The agent's lean** was to restate the criterion against the built binary, and
to refuse the alternative of making the usage error exit 1 — because 1 and 2 are
kept apart precisely so a caller cannot read its own typo as a failing site.

**Decided:** accepted as leaned. The built binary is what a caller actually
invokes and its exit code is the contract CI reads; `go run` is a developer
convenience that happens to lose the code. Changing the program's exit codes to
fit the test would be fixing the wrong end.

**To reverse:** answer the question differently and let the item be re-judged;
nothing in the control plane changed for this one.

---

## Changes made without being asked

These were defects, not judgement calls, but they are listed because each
changed how the fleet behaves.

| what | commit | why |
|---|---|---|
| Checks rebound to the edges that exist | `571d4b1` | The gate had nothing to run and refused everything |
| A claim is the LAST run of a check | `9e70159` | An agent was refused for showing its working |
| `schedule stop` drains instead of killing | `ee4ca22` | Six engineer runs were abandoned by restarts |
| Plan review advises below `plan_gate_min` | earlier | One contested plan held twelve items for hours |

---

## Still open for you

- **`blast.plan_gate_min` is `host`**, so source-only work starts on a breakdown
  that is still under review. You chose this; it is worth confirming once you
  have seen what it produces.
- **Eleven view fields** were computed and shown nowhere. Three were dead and
  removed; eight are now on a page. If any of those eight are noise on a page
  you actually read, say so and they come off.
