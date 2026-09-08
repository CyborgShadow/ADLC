# Design

The decisions this is built on, stated plainly. Each one is a constraint the rest of the system
depends on.

## The control plane is the only writer

Agents emit an envelope. The CLI decides what that envelope earns and appends the result. A
worker with write access to its own audit record is not auditable.

A refused proposal is appended too. A system that records only what it accepted cannot answer
the question it will actually be asked.

## Agents report; the tool decides

An agent is never asked what state work should move to. It reports `pass`, `fail`, `reject` or
`blocked`, and a pure function computes the rest from the state the item was in, the capability
the run held, and the item's blast radius.

Two things follow. An agent cannot name a state that does not exist, or one it is not allowed to
take — a whole class of failure that costs a run each time. And the decision is re-derivable: a
decision that depends on what an agent happened to write cannot be replayed at all.

## The gate runs the checks itself

An envelope is a declaration, never evidence. Substring-matching a list of commands an agent
typed checks that it *claimed* something ran, never that it did.

So the control plane executes the declared commands in the run's own tree, and what it observes
is the evidence. The agent's account survives only as a claim to be compared, and a disagreement
is its own recorded refusal.

## A verdict has three values

`GREEN`, `RED`, `UNKNOWN`. The third is load-bearing everywhere.

"I could not tell" is never a pass. A missing tool is `UNKNOWN`, a host that did not answer is
`UNKNOWN`, and `UNKNOWN` satisfies nothing. A hang is `RED` — a check nobody can wait for is a
check that gets skipped, and a skipped check is an absent one.

Two judging rules follow from the same idea:

- Some commands report their verdict in their **output** and exit 0 either way. Reading their
  exit code reads a channel carrying no information.
- A run that discovered **zero units of work** is a failure. Zero tests matched, zero hosts
  scanned, zero rules evaluated. A filter that silently matches nothing converts "I ran nothing"
  into "everything passed".

## Absence never renders as a pass

The roster of roles is declared, not inferred from what has reported. A role that never runs
contributes no rows, so reading activity alone renders its silence as an absence rather than as
the alarm it is.

The same rule governs scheduled lanes: every firing writes a tick, including the idle ones, so a
lane that stops firing stops producing ticks and becomes a derived alarm.

## Verification is a state, not a schedule

An item cannot leave `verifying` without a verifier, and the verifier cannot be the run that did
the work. Verification is the next step of an item already in flight rather than a competing job,
and the dispatcher drains from the finished end — so checking never queues behind building.

A lane that both writes and judges its own work will always find it acceptable.

## A breakdown is reviewed before it is built

A brief is decomposed by one role and the decomposition is checked against the brief by another,
before any of it becomes work. A fleet that starts building the moment a brief is decomposed
builds exactly what the decomposition said, including the parts that do not add up to what was
asked for — and nobody finds out until the deliverable is finished and wrong.

A deliverable filled in by hand skips this: there is no agent breakdown to review.

## Nothing irreversible happens without an approval that names the plan

Most delivery pipelines assume every action is undone by reverting a commit. Once the artifact is
a running machine, that stops being true.

So an item declares a blast radius, and above a configured threshold it stops and waits for a
named person who approves **one specific dry-run digest**. If the plan changes afterwards the
approval no longer applies. An unrecognised radius fails closed.

## Evidence binds to what it describes

A commit says what the source was. It says nothing about what was built or applied from it, so a
check that runs against a machine must name that machine's digest, and the gate refuses to record
one without it.

Evidence produced over a tree with uncommitted changes describes a state that has no commit and
that nobody can check out again, so the gate refuses that too.

## Coordination state is never committed

Claims are files with a TTL, outside version control. Coordination has a lifetime of hours;
putting it in history costs a commit to claim and one to release, and still races.

The claim key is the work item's own id, canonicalised, so every spelling collapses to one key.
Resources are leased hierarchically — leasing a fleet refuses a claim on a host inside it, and
the reverse. Two agents editing one file conflict at merge, which is loud and cheap. Two agents
changing one machine cause an outage.

## A run wears one name

The run id, the workspace directory and the lease key are the same string, minted and
collision-checked before the work is picked. A run whose names diverge is invisible to every
guard that keys on either one.

## Guards need a scope and a clean case

A tree-walking guard needs a stated scope as much as it needs a firing case. Unscoped, it counts
vendored dependencies and build output as uncommitted work and refuses runs over a tree nobody
edited.

Every guard in the test suite carries both a firing case and a clean case. A test that only
asserts a guard fires passes vacuously the day that guard starts flagging everything.

## Integrity and knowledge are separate verdicts

Broken hashes mean `TAMPERED`. An event kind, item state or schema version this build does not
recognise means `UNKNOWN`, and the recovery is to upgrade the binary.

A tamper detector that accuses at its own obsolescence gets ignored, and an ignored detector is
not a control. The head anchor lives outside the event table for the same reason: truncating a
chain leaves one that still walks cleanly, and only a second copy of the tip notices.

Projections are derived and never authoritative. `verify` rebuilds every one from the chain in a
scratch database and diffs, so "the reports agree with the record" is checked rather than hoped
for. It also proves the append-only guards still fire by attempting the writes they exist to
refuse.

## Accounting nothing acts on is decoration

Token usage is priced on the way in and wired to a refusal. An autonomous dispatcher with no
spend accounting has no throttle, and a cost column nobody reads renders identically whether it
is right or absent.

A model with no price entry costs `UNKNOWN`, not zero. A cap of zero means unlimited and reports
as unlimited: "no budget configured" and "budget exhausted" are opposite facts.

## Render from the record, never edit the render

Every figure on every surface is a projection of the chain. Anything hand-edited downstream is a
second answer to a question the record already answers, and the two diverge the moment either
changes.

## Prompts are gated artefacts

A prompt that lives only inside a scheduler is unversioned and unreviewable. Here the file is the
definition: the dispatcher reads it at dispatch time and carries no prompt of its own.

Mandatory clauses must appear in every role prompt and the gate checks it, so a run cannot delete
a safety clause from its own instructions. The shared preamble is stored once and assembled at
dispatch, so fleet-wide policy is one edit — and cannot drift between roles.

## Statuses the tool can compute, it computes

Readiness, roadmap position and anything else derivable is recomputed on every pass. A status
nothing computes is a status nothing sets, and an item stuck behind a step nobody performs looks
exactly like an empty backlog.

## Package layout

```
cmd/adlc/            the CLI — the only writer to the ledger
internal/ledger/     hash-chained append-only store, two-tier verification, blobs, migrations
internal/authority/  the transition table, state advancement, generation admission, roadmap
internal/gate/       runs the declared checks; compares claims to observations
internal/dispatch/   selection, routing, isolation, invocation, the scheduled lanes
internal/server/     the operator dashboard
internal/story/      run narratives and decision replay
internal/lease/      ephemeral claims on items and resources
internal/config/     the declared surface that makes this generic
internal/envelope/   the worker output contract
internal/spend/      token accounting wired to a refusal
internal/prompt/     versioned prompt library, mandatory-clause gating
internal/report/     deterministic projections
agents/              the prompts — one shared preamble, one file per role
```
