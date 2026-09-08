# Operating

## Running it

```bash
adlc schedule run              # every lane on its cadence, plus the dashboard
adlc schedule run -serve=false # lanes only
adlc serve                     # dashboard only
```

Ctrl+C stops cleanly: each lane finishes the dispatch it is in and exits. Nothing is held in
memory — every outcome is written to the ledger as the run progresses — so starting it again
resumes exactly where it was. There is no state file to corrupt and no shutdown sequence to get
wrong.

A process killed hard leaves a run with a start and no end. That shows as `UNKNOWN` — never as a
pass — and the item is untouched, so the next pass picks it up again. Its lease expires on its
own TTL, so a dead run cannot hold a resource forever.

## The two things it needs you for

**Questions.** An agent that hits something it cannot defensibly decide stops and asks rather
than guessing. The Questions page shows each one with the agent's own recommendation and the
evidence behind it. Your answer is recorded verbatim and unblocks the item — summarising it later
would lose the reasoning that sets the severity of everything decomposed from it.

**Approvals.** Anything whose blast radius exceeds your threshold stops before it touches
anything real. The Approvals page shows what will change, on what, and the worst case if it is
wrong. You approve **one specific plan digest**; if the plan changes afterwards, the approval
stops applying and the apply is refused as `approval_stale`.

Both are also available from the CLI:

```bash
adlc question list
adlc -actor you question answer -id Q-1 -answer "…"
adlc approval list
adlc -actor you approval decide -id AP-1 -verdict approve -approver you -note "…"
```

## Watching it

```bash
adlc report fleet
adlc report segment S1
adlc schedule status      # lane liveness: live / STALE / NEVER RUN
adlc dispatch plan        # what would be picked next, in order, and why
```

`schedule status` is derived from the tick record, not self-reported. Every firing writes a tick
including the idle ones, so a lane that has quietly stopped reads `STALE` rather than looking
like a lane with nothing to report.

## Reading a run back

```bash
adlc run show <id>       # what it was given, what it did, why it mattered
adlc run steps <id>      # the same as a flat timeline
adlc run prompt <id>     # the exact text that run was handed
adlc run envelope <id>   # exactly what it reported back
```

`run show` answers the question people actually ask weeks later. It assembles the chain — this
run advanced that item, which serves this deliverable, which exists because you wrote this
rationale — from recorded links rather than from anybody's memory.

```
RUN v-20260908T175925Z-01 — validator
============================================================

validator moved S2-002 from validating to reviewed.

WHY THIS MATTERED
  Work item S2-002 — Cover internal/config with tests
  Deliverable S2 — Close the coverage gaps. An unreachable backlog looks
  exactly like a backlog nobody has got to.

WHAT HAPPENED, IN ORDER
  1. Dispatched as validator  [17:59:25]
  2. Gate GREEN on validating->reviewed  [17:59:26]
  3. Agent reported "pass"  [17:59:26]
  4. Advanced validating → reviewed  [17:59:26]
```

## Replaying a decision

```bash
adlc run replay <id>
```

This re-derives the **decision**, not the agent. Re-invoking a language model reproduces nothing
and claiming otherwise would be dishonest. But everything the control plane did is a pure
function of recorded inputs: the retained envelope is re-parsed, the declared checks are re-run,
and the result goes back through the same authority.

```
REPLAY v-20260908T175925Z-01

  recorded at the time   admitted validating -> reviewed
  re-derived now         admitted validating -> reviewed
  gate against this tree GREEN

The decision reproduces. Given the same recorded inputs, the control plane
makes the same call today that it made then.
```

A disagreement is not automatically a defect — rules are allowed to get stricter, and a run
admitted under an older policy may be refused under a newer one. Exit code 2 says they differ;
the output says how. Replay against a dirty working tree will report a gate difference, and says
so.

For a planner, replay re-runs the item-admission rules over the same proposals and reports how
many are judged the same way today.

## Retrying

```bash
adlc run retry <id>
```

Re-opens a rejected item for rework, with the reason recorded so the next run can read what went
wrong. It is deliberately not "run that agent again": re-invoking the same agent against the same
state reproduces the same conditions and usually the same outcome.

Past `max_attempts` it refuses and tells you so. That is the point — an item that has failed
three times needs a decision, not a fourth attempt.

## Moving a deliverable by hand

```bash
adlc -actor you segment advance -id S1 -to paused -why "waiting on the vendor"
```

Every roadmap state is reachable this way, and `-why` is required: a move with no stated reason
is indistinguishable from a mistake.

## Auditing the record

```bash
adlc ledger verify      # integrity and knowledge, answered separately
adlc ledger events      # the chain; kinds this build cannot read are marked
adlc ledger head
```

`verify` runs eight integrity checks and two knowledge ones. The integrity set includes a
**replay**: every projection table is rebuilt from the chain in a scratch database and diffed, so
"the reports agree with the record" is a checked property rather than a convention. It also
*proves the append-only guards still fire*, by attempting the writes they exist to refuse — a
guard that has gone quiet reads exactly like a clean one.

Three verdicts:

- `INTACT` — everything passed and this build understood all of it
- `UNKNOWN` — integrity holds, but this binary is too old to interpret part of the record.
  Upgrade it. This is not an accusation
- `TAMPERED` — the record does not describe itself. Stop and investigate

## Backing up

The ledger is `.adlc/ledger.db`. Copy it while nothing is writing, or use SQLite's own backup.
The blobs — retained prompts and envelopes — live in the same file, so one copy takes everything.

`.adlc/` is gitignored by default. If you want the audit record in version control, export it
rather than committing the database.

## When something is wrong

**Everything is idle but there is work.** `adlc dispatch plan` prints what would be picked and
why. If it prints nothing, the usual causes are a deliverable still at `planned` (its breakdown
has not been reviewed), an item in an area nothing routes to (the dispatcher logs `UNREACHABLE`),
an open blocking question, or a paused lane.

**A lane says STALE.** It has not fired within three times its own cadence. Either the scheduler
is not running, or the lane is paused, or it is wedged in a long dispatch.

**Every run is refused for the same reason.** Read the reason: it names what was missing. A
`claim_discrepancy` means the agent's account of a check disagreed with what the control plane
observed. A `gate_failed` means the checks genuinely failed. An `uncommitted_tree` means the run
did not commit before reporting.

**A run shows as UNKNOWN forever.** Its process died without recording an end. The item is
untouched and will be picked up again; the run stays in the record as an unknown, which is the
honest thing for it to be.
