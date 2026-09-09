# Overnight, 8–9 September 2026

You asked for a working product and an ADLC that can redo a test in under two
hours. Here is what happened, what I changed, and what I would not decide for
you.

## The headline

**The ADLC built, verified and merged its own work, unattended.** Seven commits
reached the trunk through the full lifecycle — engineer, then test, judge and
adversarial review running together, then hygiene, then coherence review, then
the merge queue — with the gate re-run on the rebased tree each time. The trunk
is green: 290 tests passing on agent-written work.

The first item to make it through is a real fix with 108 lines of test behind
it: an anchored `count_pattern` was finding nothing where it should have found
the line it names.

## What the night actually cost, and why

| | |
|---|---|
| Runs finished | 45 |
| Agent time | 7h 55m |
| First three items | 13–15 runs each, ~3h 30m wall each |
| Items dispatched after the fixes | 2 runs each, 4–14 minutes |

That contrast is the whole story. The first three items paid for every defect in
the pipeline; the ones after them went through cleanly. **The design was not the
problem — nine specific bugs were, and eight of them were mine.**

## What was broken, in the order it bit

1. **Nothing could leave `in_progress`.** Collapsing the verification chain into
   one stage renamed the states, and every declared check went on gating edges
   that no longer existed. The gate had nothing to run and refused everything,
   while `adlc config check` reported the config valid. Engineers rebuilt the
   same items for an hour.

2. **An agent was refused for showing its working.** It ran `gofmt`, found a file
   it had not formatted, fixed it, ran `gofmt` again, recorded both — and the
   matcher compared against the *first* run.

3. **A claim worse than the gate's observation was treated as a lie.** Even after
   (2), an agent that found a problem and fixed it without re-running was
   refused. The matcher exists to catch a claimed success the gate did not see;
   the reverse is honesty. Only a claim *better* than the observation is a
   discrepancy now. This one was costing a run on every item.

4. **A judge lane ticked 269 times and dispatched nothing.** The lane filter ran
   before the multi-task stage was expanded, so a judge lane was compared against
   `test` and skipped every item in verification. Three items sat waiting for a
   judge while every liveness figure stayed green.

5. **An item left verification five times.** `Refresh` is called from every lane
   at once and its moves are read-check-write; two passes both appended the same
   transition. The state came out right, which is the dangerous part — the only
   symptom was a duplicated log line, and the chain had recorded a move from a
   state the item had already left.

6. **The merge queue tried to land an empty branch.** By the time an item is
   cleared to merge, the newest run against it is an arbiter, which changes
   nothing. The work was on the engineer's branch one row down.

7. **A rejected plan taught the fleet nothing.** Lessons only came from an
   improver, which runs after an item *merges* — so a deliverable that never got
   past planning could be rejected four times and the fifth planner would start
   with exactly what the first had.

8. **Six engineer runs were abandoned** because I restarted the control plane
   while they were building. There was no other way to stop it: a console process
   on Windows cannot be asked politely to exit, which I verified rather than
   assumed. `adlc schedule stop` now drains — stops dispatching, waits for work
   in flight, then exits. Used four times since, nothing abandoned.

9. **An agent's handle to the ledger could write.** I had given agents the real
   ledger's path so they could read the record; an agent runs arbitrary code from
   an unreviewed branch, including the whole test suite under the gate, and
   something in that surface moved the stored schema version to 2. The handle is
   read-only now. The chain's integrity held throughout and it reported UNKNOWN
   rather than TAMPERED, with the reason named — the signal working as designed.

Each of these has a test that was verified to fail without the fix.


## Two more, found by counting refusals rather than by watching

The fleet was healthy by 08:30 and still throwing runs away. Counting the
refusals by reason rather than reading the log found two more classes, both
fixed and both guarded.

**10. Five of seven malformed envelopes were formatting, not meaning.** Agents
wrote `criteria` as an object keyed by criterion id, `"met"` where the schema
says `"pass"`, and findings as prose. None of them was wrong about the work; each
threw away a whole run. `Parse` already held the line — forgiving about
encoding, unforgiving about meaning, because refusing a fenced envelope costs a
run to punish formatting — and it now extends to shape and stops exactly there.
A status word maps only from a closed list; an unstated finding severity is
resolved against the worker's own verdict so it fails closed; a severity from no
vocabulary is still refused rather than guessed. The raw bytes are untouched, so
the blob on the chain is what the agent wrote and every re-shaping is auditable
against it.

The refusals that remain now teach. A malformed envelope was refused *before*
`recordLessons` was reached, so the next run — a fresh agent that cannot see the
ledger — wrote the same shape and lost its work the same way. Three runs did
exactly that in one night.

**11. One failing verification task discarded its two siblings.** This was the
largest remaining cost in the lifecycle. Test, judge and adversarial review ask
three independent questions of one commit; the first failure sent the item
straight back to the builder, so the two runs still examining that commit were
refused as stale when they finished, and the builder was told one of the three
things wrong with its work — learning the other two a whole round later. Two
extra verification rounds per failing item.

A failed task is now a self-edge, exactly like a passing one. `NextState` stays
a pure function of state, capability, verdict and radius — it still knows
nothing about siblings — and `Refresh` settles the stage from the record:
forward when every task passed, backward once every task has *reported*,
carrying every failure in one line. Adversarial review still outranks the rest:
a blocker rejects whoever else agreed.

## Decisions I took for you

Recorded in full in `docs/decisions-overnight.md`, attributed on the chain to
`claude` rather than to you — the ledger exists to say who decided.

- **S1-010-Q1** — an agent correctly diagnosed the gating-edge bug and refused to
  add a check declaration to make its own gate pass. I told it the diagnosis was
  right and the fix was the other direction.
- **S1-002-Q1** — an acceptance criterion required `go run` to exit 2, which no
  Go program can do. Accepted its lean: assert against the built binary rather
  than weaken the program's exit codes to fit the test.

I did not touch sign-off, approvals or blast-radius policy. Those are the three
gates a person owns, and me pressing them would make the record say something
untrue about who decided.

## Left for you

- **The stored schema version says 2 and no merged code defines a 2.** I left it
  alone. Editing a ledger by hand so a report reads better is the one repair this
  system must never make. `ledger verify` will keep saying UNKNOWN until a build
  defines a schema 2 — which is the honest answer, not a fault.
- **Post-verification ceremony.** After verification an item still goes through a
  janitor, an arbiter and an improver — three more full agent runs, serially, on
  work whose blast radius is `none`. Collapsing or skipping those for source-only
  work is the largest remaining speed-up, and it changes what the ADLC *is*, so
  it is your call rather than mine.
- **`blast.plan_gate_min` is `host`**, so source-only work starts on a breakdown
  still under review. You chose that; worth confirming now you have seen it run.

## Can it redo a test in two hours?

On the evidence: yes, if nothing new breaks. Items dispatched after the fixes
took 2 runs and minutes rather than 15 runs and hours. A fresh deliverable would
spend roughly 40 minutes on research, planning and plan review before any code,
then items flow in parallel waves of 8.

I would not promise it until it has been watched once from an empty ledger. The
honest position is that every stall found tonight is fixed and guarded, and the
ones nobody has hit yet are still out there.
