---
name: adlc-triage
description: Read-only diagnosis of a running adlc fleet — reads the fleet report, lane liveness, open questions, approvals and ledger integrity, then explains in plain language what is blocked and why, what is waiting on the user, and which lanes have quietly stopped firing. Use when someone asks what the fleet is doing, why nothing is happening, why an item is stuck, what needs their attention, or whether the record is intact. Changes nothing.
---

# Triage a running fleet

**This skill is read-only.** Run only the commands listed below. Do not answer a question,
decide an approval, sign off a deliverable, edit `adlc.json`, dispatch anything, or run
`adlc ledger rebuild`. If something needs doing, say what and hand it back.

## Read the state

Run these, in this order. Each is safe and writes nothing.

```bash
adlc report fleet        # the rollup: segments, items, spend, worker activity, never-run
adlc schedule status     # lane liveness, derived from the tick record
adlc question list       # what the fleet stopped on and could not decide
adlc approval list       # what is waiting to touch a real machine
adlc ledger verify       # is the record still describing itself
adlc dispatch plan       # what would be picked next, in order, and why
```

`adlc dispatch plan` selects nothing and dispatches nothing — it prints the decision. It is
usually the fastest answer to "why is nothing happening".

If more detail is needed on one deliverable or item:

```bash
adlc segment list
adlc report segment <id>
adlc item list -segment <id>
adlc item show <item-id>       # state, criteria, attempts, recent runs, recent refusals
adlc ledger events -limit 40   # the raw chain, newest kinds visible
```

Add `-json` to `question list`, `approval list`, `item list`, `item show`, `ledger verify`
and `schedule status` when you need to parse rather than read.

## Read the exit codes, not just the text

These are part of the contract and each means something different:

| Code | Meaning |
|---|---|
| 0 | fine |
| 1 | usage, or the config/ledger would not open |
| 2 | a transition was refused |
| 3 | the ledger is **TAMPERED** — the record does not describe itself |
| 4 | a prompt lost a mandatory safety clause |
| 5 | the ledger is **UNKNOWN** to this binary — the chain is fine, the binary is too old |
| 6 | the gate is RED |
| 7 | the gate could not be run — which is not a pass |
| 8 | the projections are stale — a different build wrote them; `adlc ledger rebuild` fixes it |

3 and 5 are deliberately different answers. Never report 5 as tampering. Never report a
missing tool, an UNKNOWN verdict or a silent role as a pass.

## What to look for

### Waiting on the user

Three things, and only three, ever need a person:

1. **A deliverable at `theory`.** It has not been signed off, so no researcher runs and
   nothing under it is decomposed. Nothing is being spent on it. Roadmap page.
2. **A blocking question.** `question list` marks it `BLOCKING`, and the item is parked
   until it is answered. Each carries the raiser's own recommendation on a `lean:` line —
   quote that, because it is usually most of the answer. Questions page.
3. **An open approval.** `approval list` shows `OPEN`; `report fleet` lists them under
   AWAITING YOUR APPROVAL with the plan digest. These are reviewed, green and stopped.
   Approvals page.

Note: the AWAITING YOUR APPROVAL block prints the command for each open request, keyed on
the approval id rather than the item's, because `approval decide` answers one request and an
item can carry more than one. Quote that line as it stands and let them run it; this skill
decides nothing.

### Lanes that have quietly stopped

`schedule status` gives each lane one of four statuses, derived from the tick record rather
than self-reported — every firing writes a tick including an idle one, so a lane that
stopped reads `STALE` rather than looking like a lane with nothing to say:

- `live` — ticking on schedule
- `STALE` — has not ticked in three of its own intervals. **This is the one that matters.**
  Something is wrong with the scheduler or the process died; a verification lane that
  stopped is an absence, not a pass
- `NEVER RUN` — zero ticks. Either the scheduler was never started, or this lane is new
- `disabled` — switched off in the config, deliberately

Read the `disp` column too: a lane ticking with zero dispatches over a long period is
finding nothing to do, which is a different problem from a lane that is not ticking.

### Work that is stuck

Read `report fleet` and `dispatch plan` together. The usual causes, most likely first:

- **The deliverable is `planned`, not `ready`.** Its breakdown has not been reviewed. Check
  that some role holds `validate` and that the review lane is enabled.
- **An item is filed under an area nothing routes to.** The dispatcher logs `UNREACHABLE`
  with the area name. Fix is a routing entry, not a re-dispatch.
- **A blocking question is open.** The item is parked.
- **A lane is paused or STALE.**
- **The item is past its rework limit.** `item show` prints `attempts N of M`, and the
  segment report flags it: escalate rather than re-dispatch.
- **Refusals.** `report fleet` ends with REFUSED TRANSITIONS, BY REASON. A cluster on one
  reason is a systematic problem, not bad luck — `claim_discrepancy` in particular means an
  agent's envelope disagreed with what the gate observed when it ran the commands itself.
- **No agent command configured.** If `dispatch.command` is empty the fleet validly refuses
  to dispatch anything.

### Roles that have never run

`report fleet` has a NEVER RUN section counted from the registry, not inferred from
silence. Report it as what it is: absence, not health. A role marked `low_cadence` there is
expected; one that is not is worth asking about.

### Money

`report fleet` prints 24-hour and total spend. Four things can appear beside those two
figures:

- `no daily cap configured — unlimited, which is not the same as zero`, on the 24-hour
  line. Say it plainly if it appears.
- `INCOMPLETE (a floor: N run(s) reported no usage)`, on either figure. The figure is a
  lower bound, not a total. Report it as a floor — a floor read as a total is how a fleet
  is believed to be under a cap it has already passed.
- `UNMEASURED` — N finished runs reported no token usage at all, with the first one named.
  Their cost is UNKNOWN, not zero. This is what the INCOMPLETE marker is counting.
- `UNPRICED` — N finished runs used tokens under a model with no price entry, with the
  first one named. Cost unknown, not cost nothing, it is not included in the figures above,
  and the daily cap can never be reached while a model is unpriced.

The last two are different faults: UNMEASURED means nobody recorded the tokens, UNPRICED
means the tokens were recorded but the model has no price. Naming the wrong one sends
somebody to fix the wrong thing — a harness that reports no usage, or a missing entry in
the pricing table.

### The record itself

`ledger verify` reports INTEGRITY and KNOWLEDGE separately, on purpose. Report the verdict
word it printed — INTACT, TAMPERED, UNKNOWN or a stale projection — and the failing check
name if there is one. Do not paraphrase UNKNOWN as a failure of integrity: it means this
binary is too old to interpret the chain, and the fix is upgrading the binary.

## Report back

Plain language. No table of raw output. This order:

1. **One line on whether anything needs them.** Either the specific things —
   *"Two blocking questions and one approval are waiting on you"* — or *"Nothing needs you;
   the fleet is working."*
2. **What is waiting on them**, item by item: the question text and the raiser's lean; the
   approval, what it changes and its blast radius; the deliverable that needs signing off.
   Say where each is on the dashboard.
3. **What is blocked and why**, cause first, not symptom. *"S1-004 has been sitting because
   the area `payments` routes to nobody"*, not *"S1-004 is in state planned"*.
4. **Lanes that stopped**, with how long they have been silent, and roles that have never
   run.
5. **Anything about the record or the money** worth knowing — TAMPERED, UNKNOWN, stale
   projections, UNMEASURED or UNPRICED runs, an INCOMPLETE figure, no daily cap.
6. **The single next action**, and whose it is. If it is theirs, say exactly where. If it is
   a config change, describe it and offer to do it in a separate step — this skill does not
   make it.
