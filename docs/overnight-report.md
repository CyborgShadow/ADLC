# Overnight, 8–9 September 2026

You asked for a working product and an ADLC that can redo a test in under two
hours. Here is what happened, what I changed, and what I would not decide for
you.


## The thing you should look at first

**The cat website is barely built, and the reason is in the plan, not the
pipeline.** Of 32 work items, 9 are done — and almost all of the finished work
is the ADLC fixing itself. The deliverable's own items are a strict serial
chain, four layers deep:

```
S1-003 (cats + stylesheet) → S1-004 (facts) ┐
S1-007 (citations) → S1-009 → S1-012 ───────┴→ S1-011 (the page) → S1-008 (CI)
```

Only S1-003 and S1-007 can run at all right now; everything else waits on them.
The parallelism you asked for is real and it is working — eleven runs abreast as
I write this — but what filled it was twenty self-improvement items the improver
raised, not the website. The website is five sequential item-lifecycles no matter
how many agents you point at it.

Two things follow, and both are your call rather than mine:

- **The planner serialised the deliverable.** Some of it is genuinely necessary —
  S1-011 holds `site/cats/index.html`, which is in no other item's file scope, so
  nothing else *can* write the page. Some of it may not be. That graph is a
  product judgement and I did not rewrite it.
- **Self-improvement items compete with the deliverable on equal terms.** Nothing
  in the queue prefers the thing you asked for over the fleet's own maintenance,
  and the fleet is very good at finding work for itself. The overnight run
  produced a much better ADLC and a much thinner website, and that ordering was
  not chosen by anybody.

## The headline

**The ADLC built, verified and merged its own work, unattended.** Sixty-two
commits reached the trunk overnight through the full lifecycle — engineer, then
test, judge and adversarial review running together, then hygiene, then
coherence review, then the merge queue — with the gate re-run on the rebased
tree each time. The trunk is green: 325 tests across 16 packages, most of them
written by agents.

The first item to make it through is a real fix with 108 lines of test behind
it: an anchored `count_pattern` was finding nothing where it should have found
the line it names.

## What the night actually cost, and why

| | |
|---|---|
| Runs finished | 188 |
| Recorded spend | $40.75, and that is a floor — see below |
| First three items | 13–15 runs each, ~3h 30m wall each |
| Items dispatched after the fixes | 2 runs each, 4–14 minutes |

That contrast is the whole story. The first three items paid for every defect in
the pipeline; the ones after them went through cleanly. **The design was not the
problem — thirteen specific bugs were, and twelve of them were mine.**

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



## Three more, found by counting refusals rather than by watching

The fleet was healthy by 08:30 and still throwing runs away. Counting refusals
by reason rather than reading the log found three more classes, all fixed, each
with a guard verified to fail without the fix.

**10. Five of seven malformed envelopes were formatting, not meaning.** Agents
wrote `criteria` as an object keyed by criterion id, `"met"` where the schema
says `"pass"`, and findings as prose. None was wrong about the work; each threw
away a whole run. `Parse` already held the line — forgiving about encoding,
unforgiving about meaning, because refusing a fenced envelope costs a run to
punish formatting — and it now extends to shape and stops exactly there. A
status word maps only from a closed list; an unstated finding severity is
resolved against the worker's own verdict, so it fails closed; a severity from
no vocabulary is still refused rather than guessed. Raw bytes are untouched, so
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
carrying every failure in one line and bumping the round. Adversarial review
still outranks the rest: a blocker rejects whoever else agreed.

**12. A branch that could not rebase went round the merge queue forever.** A
rebase that conflicts against a fixed trunk conflicts identically every time, so
the merge lane attempted the same impossible rebase on every tick while the item
looked, on every surface, exactly like work nobody had got round to. That is the
shape a stall takes when nothing errors. It now goes back to a builder with the
conflict named, which is the only actor that can resolve one. `trunk_moved`
stays retryable, because that one really does succeed next time.

**And a thirteenth thing, which is not a bug.** An answered question reached
nobody. The item unblocked and the run that picked it up was a fresh agent with
no memory of the decision, so it re-derived it or asked it again — two runs
raised the same question about the same acceptance criterion, neither able to
see the other's, and both stopped for a person who had already answered. Settled
decisions now ride in the slot the preamble already renders, headed separately
and told plainly that they are settled. No role prompt changed.

## Decisions I took for you

Six, recorded in full in `docs/decisions-overnight.md` and attributed on the
chain to `claude` rather than to you — the ledger exists to say who decided.

- **S1-010-Q1** — an agent correctly diagnosed the gating-edge bug and refused to
  add a check declaration to make its own gate pass. I told it the diagnosis was
  right and the fix was the other direction.
- **S1-002-Q1 and Q2** — an acceptance criterion required `go run` to exit 2,
  which no Go program can do. Accepted its lean: assert against the built binary
  rather than weaken the program's exit codes to fit the test. Asked twice, by
  two runs that could not see each other's question.
- **S1-017-Q2 / QJ1, S1-005-Q1, S1-017-Q1/Q3/Q4, S1-016-Q1, S1-018-Q1,
  S1-001-QJ1** — scope calls, follow-up items and one "leave it derived".
- **S1-019-Q1** — an agent reported that every self-service `adlc` command was
  broken for it. Its diagnosis of the DSN was right and its refusal to make the
  handle writable was right; the cause was neither of the two it offered. Its
  workspace is cut from a branch older than the fix, so `go run ./cmd/adlc` in
  its own tree builds an `adlc` that predates DSN support. No item — and a real
  design trap it found, below.

I did not touch sign-off, approvals or blast-radius policy. Those are the three
gates a person owns, and me pressing them would make the record say something
untrue about who decided.

## Left for you

- **A worker self-services with a binary built from its own branch**, so any
  change to the control plane's CLI contract is invisible to every run already
  in flight, and looks to that run like a defect in the tree it is holding.
  S1-019 found this. The fix is to bring an item's branch up to trunk when its
  workspace is prepared — merging where clean, carrying on where not. I have not
  done it: it rewrites real branches carrying real work, and I want you watching
  that one rather than waking up to it.
- **The schema-2 anomaly is solved, and it was not a mystery.** An arbiter
  asked, at 09:40, why `adlc_event` carries a `revision` column no build
  declares. It is S1-016's own: that branch declares `SchemaVersion = 2` and adds
  the column, and the migration reached the *live* chain because until `5e70b57`
  at 03:35 agents held a writable handle to the real ledger — a run of
  `go test ./...` under the gate, from that branch, opened it read-write and
  applied its own migration. The handle is read-only now and this resolves itself
  the moment S1-016 merges and the build declares a 2. Integrity held throughout
  and `ledger verify` said UNKNOWN rather than TAMPERED, with the reason named.
  I have left both the column and the version alone: a hand-edit of the chain to
  make a report read better is the one repair this system must never make, and
  the arbiter is raising a test instead — that a chain table carrying a column
  the build does not declare still verifies INTACT, so the next stray write is
  caught by a check rather than by somebody looking for something else.
- **Post-verification ceremony.** After verification an item still goes through a
  janitor, an arbiter and an improver — three more full agent runs, serially, on
  work whose blast radius is `none`. Collapsing them for source-only work is the
  largest remaining speed-up, and it changes what the ADLC *is*, so it is your
  call rather than mine.
- **`blast.plan_gate_min` is `host`**, so source-only work starts on a breakdown
  still under review. You chose that; worth confirming now you have seen it run.

## Can it redo a test in two hours?

On the evidence: yes, if nothing new breaks. Items dispatched after the fixes
took 2 runs and minutes rather than 15 runs and hours, and the queue now runs
eight abreast with test, judge and review on one commit at once. A fresh
deliverable spends roughly 40 minutes on research, planning and plan review
before any code, then items flow in parallel waves.

I would not promise it until it has been watched once from an empty ledger. The
honest position is that every stall found overnight is fixed and guarded — nine
found by watching, three more by counting refusals — and the ones nobody has hit
yet are still out there.
