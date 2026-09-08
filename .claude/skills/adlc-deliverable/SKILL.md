---
name: adlc-deliverable
description: Turn a fuzzy goal into an adlc deliverable — draw out a brief a researcher can work from, a rationale in the user's own words, and a sensible target for items in flight, then run `adlc segment create`. Use when someone wants adlc to build something, describes what they want built, or asks how to create a segment or add work to the roadmap.
---

# Turn a goal into a deliverable

A deliverable in adlc is a segment. You conduct the interview and `adlc segment create`
records it. Never append to the ledger any other way — the ledger is append-only and
hash-chained, and a row written badly cannot be corrected, only annotated.

The output is three pieces of prose and two numbers. The prose is the whole job.

## What you need

| Flag | What it is | Who reads it |
|---|---|---|
| `-id` | short stable id, e.g. `S1`, `AUTH` | everything downstream; item ids are derived from it |
| `-title` | one line | the Roadmap page, every report |
| `-brief` | the intent | **a researcher works this up and a planner decomposes it** |
| `-why` | why it matters, in the user's language | whoever asks, months later, what a run was for |
| `-target` | how many unfinished items to keep in flight | the planner, as its refill target |
| `-rank` | roadmap order, lower first | the Roadmap page |
| `-depends-on` | another segment id, repeatable | dispatch ordering |

`-id` and `-title` are required. `-target` above 0 without a `-brief` is refused, in those
words: *a planner asked to keep work in flight with no direction will invent scope.*

## Before you ask anything

Check what already exists, so you do not collide with an id or duplicate a deliverable:

```bash
adlc config check      # the config loads at all
adlc segment list      # ids and titles already on the roadmap
```

Read enough of the repository to ask sharp questions. If they say "password reset", find
out whether there is already an auth package, a mailer, a rate limiter. A brief that
ignores what exists produces a researcher run that spends tokens discovering it.

## The interview

Two rounds, then a draft.

### Round 1 — the shape of it

1. In one line, what should be true when this is finished?
2. Who is it for, and what goes wrong for them today? (This becomes `-why`, and you want
   their words, not yours.)
3. What is explicitly **not** in this — the neighbouring thing you should stay out of?

### Round 2 — the parts a planner will otherwise invent

Ask about whichever of these the answer to Round 1 left open. Do not ask all of them;
ask the ones that are genuinely undetermined:

- Constraints that are not negotiable — a protocol, a schema, a library already chosen,
  a compatibility promise.
- The failure cases that matter. (These are usually the difference between a brief that
  produces a demo and one that produces a feature.)
- Anything that must **not** happen — data that must not leak, a behaviour that must not
  regress, an interface that must not change.
- Does any of this touch a real machine? Migrations, deploys, infrastructure? Say that in
  the brief, because it decides what blast radius the items get and therefore what stops
  for an approval.
- Does it depend on another deliverable already on the roadmap?

### Then: how much in flight

`-target` is how many unfinished items the planner keeps open here. Without it the
deliverable is hand-filled and no planner ever runs against it.

Ask it as a question about pace, not a number: *how many pieces of this do you want being
worked on at once?* Three to five is an ordinary answer for a deliverable of a week or two.
One is right for something that must land in order. Zero, deliberately, is right when they
intend to write the items themselves.

## Write the brief and read it back

Draft `-brief` as a short paragraph, in the user's terms, containing: what must be true
when it is done, the constraints, the failure cases, and what is out of scope. Concrete
enough that a researcher can go and find out what exists and choose between options.

**Show the user the exact brief and rationale before you run anything.** This text is what
the whole deliverable is decomposed from, and it goes onto an append-only record. If they
change a word, change it.

A useful shape:

> A user who has forgotten their password can get back in without contacting support.
> Single-use token, expires in an hour, rate limited, and the reset itself never reveals
> whether an address is registered. Reuses the existing mailer in internal/mail. Does not
> change the session model. Out of scope: SSO and account recovery for locked accounts.

## Create it

```bash
adlc -actor "their name" segment create \
  -id S1 \
  -title "Password reset that works" \
  -brief "..." \
  -why "..." \
  -target 5 \
  -rank 10
```

Pass `-actor` with the person's name, not `cli`. It is recorded on the row, and an
unattributable row on an append-only chain cannot be corrected afterwards.

If it refuses, show the message and fix the input with the user. Do not work around it.

## Tell them what happens next — this part matters

The deliverable is now a **theory**. Nothing is spent on it yet.

> It sits at `theory` until you sign it off on the Roadmap page. That is the one planning
> gate no machine passes on its own. Until you do, the research lane will not dispatch a
> researcher and nothing is decomposed or built.

Then, once signed off: research writes an approach, plan proposes work items, review checks
the breakdown against the intent *before any of it is built*, and only then does the work
open. If you disagree with the plan, the reject at review is much cheaper than the reject
after it is built.

Start the fleet and sign off:

```bash
adlc schedule run     # lanes start, dashboard on http://127.0.0.1:8099
```

Roadmap page → the deliverable → sign off.

## Report back

- The id, title and target you created, and that it is at `theory`.
- The brief and rationale as recorded.
- That nothing is spent until they sign it off on the Roadmap page, and where that is.
- Anything you asked about and did not get a firm answer on, so they know what the
  researcher will have to decide for itself.
