# Operating

## Running it

```bash
adlc schedule run              # every lane on its cadence, the merge queue, and the dashboard
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

## The three things it needs you for

**Sign-off.** A deliverable sits as a `theory` until you agree it is worth pursuing. Research,
decomposition and plan validation all cost runs, so the pipeline stops in front of them rather
than after them. It is one click on the Roadmap page, or `adlc segment advance`.

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

## Driving it by conversation

With `console.enabled`, a panel rides on every dashboard page and `/console` holds the whole
transcript. You say what you want in a sentence; the console answers in prose and proposes
actions, each with a control beside it saying what it would do and what it costs if it is wrong.

It is not a shortcut past anything. An item it raises goes through the same admission rules a
planner's proposals face; a state change goes through the same transition authority a lane's work
does. What it removes is having to assemble the flags.

What it does with a proposal rather than handing it to you is one setting.

| `console.authority` | what it executes |
|---|---|
| `propose` | nothing. Every action renders as a control you press |
| `act` | anything the control plane could do on its own. The three gates a person owns, it drafts |
| `full` | those three as well |

The three are signing off a deliverable, answering a blocking question, and deciding an approval.
They are not gated because they are the most dangerous — cancelling an item is arguably worse.
They are gated because each one *is* the checkpoint, and an agent that clears its own checkpoint
has removed it. At `full` there is no human checkpoint left in the pipeline; the record still
says, on every one of them, that the console pressed it.

An unrecognised authority level is refused at load rather than treated as the safest one, because
a console that silently does less than you configured is one you would not notice was wrong.

Every turn is a run. It is dispatched as a declared worker holding the `converse` capability, its
assembled prompt is retained like any other, and both the ask and the reply are events on the
chain. Actions the console took on its own are recorded under the actor `console`; actions you
pressed are recorded under your name. That distinction is the whole point of the actor column, so
the console never files its own work under yours.

A turn that timed out, crashed or wrote nothing readable is recorded as a failed turn with the
reason. An ask with no reply and no explanation looks exactly like a console that has quietly
stopped working.

The conversation is one per project, not one per browser tab: two operators holding separate
conversations about the same fleet would each be missing half of why it is in the state it is in.
The agent has no memory between turns beyond the transcript replayed into it —
`console.history_turns` is how much, and the panel carries the last six.

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
adlc ledger verify      # integrity, derivation and knowledge, answered separately
adlc ledger rebuild     # re-derive the projections from the chain
adlc ledger events      # the chain; kinds this build cannot read are marked
adlc ledger head
```

`verify` asks three separate questions rather than one. Does the chain describe itself — hashes,
the anchor held outside the event table, and the append-only guards, which it *proves still fire*
by attempting the writes they exist to refuse, since a guard that has gone quiet reads exactly
like a clean one. Do the derived tables match a replay of the chain, rebuilt in a scratch database
and diffed, so "the reports agree with the record" is checked rather than assumed. And did this
build understand everything it read.

Four verdicts:

| verdict | what it means | exit |
|---|---|---|
| `INTACT` | everything passed and this build understood all of it | 0 |
| `STALE PROJECTION` | the chain is fine; a derived table does not match a replay of it | 8 |
| `UNKNOWN` | integrity holds, but this binary is too old to interpret part of the record. Upgrade it. This is not an accusation | 5 |
| `TAMPERED` | the record does not describe itself. Stop and investigate | 3 |

`STALE PROJECTION` is separate from `TAMPERED` because the disagreement it reports has two causes
that look identical from outside: an upgrade changed how a row is derived, or something wrote to
a projection without going through the chain. Calling that tampering asserts a cause the check
cannot establish, and it fired on every ordinary upgrade — an alarm that goes off on routine
releases is one people learn to ignore, which costs exactly the one time it was real.

The consequence is different too. A broken chain means history is unaccounted for. A wrong
projection means a cache is wrong, and it is one command:

```bash
adlc ledger rebuild
```

That deletes every projection table and re-derives it from the chain, then verifies and tells you
where the ledger now stands. Nothing touches the chain — the append-only guards stay in force
throughout — because a rebuild that could rewrite history would defeat the point of having any.
It is deliberately not automatic on startup: an upgrade that changed how a row is computed is
worth somebody knowing about once.

If the rebuild itself stops partway, it names the sequence number and event kind it stopped on.
The chain is intact and this build cannot derive a projection from it, which is the same recovery
as `UNKNOWN` — upgrade the binary.

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
