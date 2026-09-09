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

An item cannot leave `verifying` until its verification task clears, and it cannot be cleared by the
run that did the work. Checking is the next step of an item already in flight rather than a
competing job, and the dispatcher drains from the finished end — so checking never queues behind
building.

A lane that both writes and judges its own work will always find it acceptable. That principle
requires the judgement not to be the builder's; it does not require it to be another agent, and for
two of the three tasks this stage used to hold it was better not to be.

## Most verification is an observation, so no agent should be asked for it

The stage held three tasks — a tester, a judge and a validator — and two of them put an agent
between the control plane and a question it could answer itself.

The **tester** re-ran a suite the gate already executes on the same edge. The gate's answer is
evidence; the tester's was a claim about it, and this project refuses claims everywhere else. The
**judge** was handed the acceptance criteria as prose and believed about what it ran: ten to fifteen
minutes of agent time per item to execute commands that were already written down. A criterion
carrying a command is now run by the control plane in the item's own tree, in seconds, by the same
executor and the same verdict rules the declared checks use. Measured over one deliverable: 221
criteria, 11 of them written as commands — so the machinery existed, was never connected to
anything, and would have crashed on the first pattern rule anybody wrote if it had been.

What is left for an agent is the question no command answers, and it is the one this fleet actually
got wrong: **does this work serve what was asked for, and does it fit the deliverable it is part
of?** A well-tested change that satisfies every criterion and answers a question nobody asked is the
expensive failure, and no check catches it. So the judge stayed, and was pointed at that instead.

Adversarial review moved to the segment, where its own declaration always said it belonged — it
"looks wider than one unit of work", and running it per item made it re-read the whole system once
per change.

Two problems disappeared rather than being fixed. Three tasks held separate leases on one item, so
whichever finished last proposed from a state the others had already left and was refused stale,
after paying for a full gate run. And a failing task invalidated its siblings' earned passes,
because a pass is keyed on the round and a setback bumps the round — so one narrow rejection re-ran
everything. With one task there is no sibling to race and no round to invalidate.

The cost is stated rather than hidden: one agent now looks at an item where three did. The defect
that reaches the trunk because of it will be one no command could catch and one reader missed. The
defect that used to reach the trunk was an entire deliverable nobody wanted, and three agents looked
carefully at every part of it.

## A breakdown is reviewed before it is built

A brief is decomposed by one role and the decomposition is checked against the brief by another,
before any of it becomes work. A fleet that starts building the moment a brief is decomposed
builds exactly what the decomposition said, including the parts that do not add up to what was
asked for — and nobody finds out until the deliverable is finished and wrong.

A deliverable filled in by hand skips this: there is no agent breakdown to review.

## An idea is signed off before anything is spent on it, and so is the approach

Research, decomposition and plan validation all cost runs, so the pipeline stops before them and
waits for a person to say the intent is worth pursuing. It is deliberately the cheapest possible
thing to ask of somebody: one click, before any money is spent, rather than a review of work
already done.

**That gate is not enough on its own, because it asks about the goal and the money is committed by
the route.** A researcher given a brief for a small static page for two children compared three
options, argued carefully, and recommended building a 3,381-line site checker before the page. It
wrote in its own notes that this "deliberately overturns the brief's second half". A validator
reviewed the resulting plan and found it sound — which it was, on its own terms. Nobody was asked
whether it was *worth* it. The page the checker was for is 189 lines and never shipped.

So the pipeline stops twice. `roadmap` asks whether the goal is worth pursuing; `researched` asks
whether the approach is worth its cost, with the researcher's own options, touched files and
out-of-scope list on the screen. Both are one click and both are a person's alone.

A rejected plan returns to `approach_agreed` rather than to the gate, because a bad decomposition
of an accepted approach is not a change of mind about the approach — and a gate that re-fires on
every repair round is one somebody clicks through without reading, which is the same as not having
it.

## The change is judged against the item, then against the system

Every check before arbitration asks "does this do what it said it would?" and can answer yes about
a change that makes the system worse — a second mechanism for something the codebase already does
once, a rule stated here and contradicted there. Those are invisible from inside one diff, so one
role looks wider than one unit of work, exactly once, at the point where the change is otherwise
finished.

## One branch lands at a time, re-gated after the rebase

Work happens in isolated worktrees, so a branch cleared to merge was gated against a tree that has
since moved. The queue rebases it, re-runs the checks on the rebased tree, and fast-forwards under
a lock held only long enough to read the tip and move it — the gate runs outside the lock, because
a lock held across a full check run throttles the whole fleet behind one execution and the queue
then grows faster than it drains. If the tip moved while the gate ran, the attempt is abandoned
rather than forced.

One guard there is not obvious. A rebase can produce a tree that changes files the branch never
touched, restoring them to what they were when the branch started — silently reverting work that
landed in between. Duplicated work is loud, because two agents doing one job collide. Reversion is
silent, and every check stays green throughout. So the invariant is checked directly: a branch may
only change files its own commits touch.

## The last step of an item is what it taught

A landed item's own history — every state, every run, every refusal — is read by one more run
before the item is closed. Anything that would make the next item cheaper is written down as a
rule somebody could follow, and anything the system itself got wrong is raised as ordinary work
with acceptance criteria a command can check. Both outputs are allowed to be empty, and often
should be: a lesson manufactured to fill a field is how a lessons file becomes something nobody
reads.

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

## Integrity, derivation and knowledge are separate verdicts

Broken hashes mean `TAMPERED`. An event kind, item state or schema version this build does not
recognise means `UNKNOWN`, and the recovery is to upgrade the binary.

A tamper detector that accuses at its own obsolescence gets ignored, and an ignored detector is
not a control. The head anchor lives outside the event table for the same reason: truncating a
chain leaves one that still walks cleanly, and only a second copy of the tip notices.

Projections are derived and never authoritative. `verify` rebuilds every one from the chain in a
scratch database and diffs, so "the reports agree with the record" is checked rather than hoped
for. It also proves the append-only guards still fire by attempting the writes they exist to
refuse.

A disagreement there gets its own verdict, `STALE PROJECTION`, and that is the third separation.
It used to report as `TAMPERED`, which fired on every upgrade that changed how a row is derived —
an alarm that goes off on routine releases is one people learn to ignore, and it then costs
exactly the one time it was real. It is also an accusation the check cannot support: two causes
produce it, an upgrade or a write that bypassed the chain, and from outside they are
indistinguishable, so the verdict reports what was observed and names both.

The consequence differs as much as the cause. A broken chain means history is unaccounted for. A
stale projection means a cache is wrong while the record was never in doubt, and `adlc ledger
rebuild` re-derives it deterministically in a second — with the append-only guards still in force
throughout, because a rebuild that could rewrite the chain would defeat the point of having one.
The rebuild is deliberately not automatic on startup: an upgrade that changed a derivation is
worth somebody knowing about once.

## Accounting nothing acts on is decoration

Token usage is priced on the way in and wired to a refusal. An autonomous dispatcher with no
spend accounting has no throttle, and a cost column nobody reads renders identically whether it
is right or absent.

A model with no price entry costs `UNKNOWN`, not zero. A cap of zero means unlimited and reports
as unlimited: "no budget configured" and "budget exhausted" are opposite facts.

A default price table ships, and the source of every figure ships with it. Both alternatives were
worse. With no table, every run reports `UNPRICED`, and a number nobody has ever seen is a number
nobody notices going wrong. With a table that defaults unknown models to zero, real spend renders
as `$0.00` — the same failure the three-valued verdicts exist to prevent, an absence reading as a
good result. So the numbers get a figure onto the screen and the source says how far to trust it:
`configured` was checked by somebody here, `default` is real money at an unconfirmed rate, and
`unpriced` is still unknown. The fallback is per model rather than per table, because an operator
who priced two models and forgot a third should not have their two figures quietly replaced.

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

## A conversation is a run, and its authority is declared

The operator console changes nothing about how the system works. A turn is dispatched as a
declared worker holding `converse`, it writes an envelope, its assembled prompt is retained, and
everything it wants to happen goes through the same transition authority and the same item
admission rules a lane's work faces. What it adds is a way to say what you want in a sentence
instead of assembling the flags. A conversational surface that reached past the authority would be
a second way into the system with different rules, and the record could no longer answer what was
allowed to happen.

How much of what it proposes it may execute is declared in config rather than settled in code,
because the answer is a property of the project: a fleet whose work reaches production wants a
different answer from one building a prototype nobody depends on.

Three of the actions are gated separately from the rest — signing off a deliverable, answering a
blocking question, deciding an approval. Not because they are the most dangerous; cancelling an
item is arguably worse. Because each one *is* a human checkpoint, and an agent that clears its own
checkpoint has removed it. So `act` executes everything else and drafts those, and `full` presses
them too, which is a fleet with no human checkpoint left in it. That is a real choice with a real
cost and it is written down rather than hidden, and the record names the console as the actor on
every one it pressed — an action an agent took filed under an operator's name is the one thing the
record must never say.

An unrecognised authority level is refused at load. Failing closed to "executes nothing" would be
safe and silent, and a console that quietly does less than the config says is one nobody notices
is misconfigured.

## Policy is editable at runtime; structure is not

The safety policy, the spend caps, the console's authority, the rework budget and the timeouts can
all be changed from the dashboard. They are judgement calls somebody makes and revises, often
during an incident, and requiring an editor and a restart means they get made badly or not at all.
Each is validated whole before any part of it is written, because a half-applied safety policy is
worse than either the old one or the new one: nobody can say which rules were in force.

Checks, workers and routing stay in the file. A check is a command line, and a form that writes
arbitrary argv into something the control plane will execute is a remote shell wearing a hat —
"it is only bound to loopback" is the kind of reasoning that ages badly. Workers and routing are
structure, and belong in a commit somebody reviewed next to the prompt files they name.

## A dispatch that never started is not a verdict about the work

An agent process that errors and writes no envelope has said nothing about the item. Recording that
as `fail` is the same mistake as recording a killed run as one: it invents evidence, and it puts a
broken change on the record where there was only a broken invocation.

So it is `unknown`, the refusal is recorded under its own reason so the fleet report can show it,
and the agent's own output is kept — without which nobody can say afterwards what broke.

A fleet also has to notice. A failed dispatch releases its slot in about three seconds, against five
to twelve minutes for a run that did something, so a broken invocation does not slow a fleet down —
it speeds it up, and the lanes fire on cadence at a hundred per cent failure. That is not
hypothetical: it produced 212 runs in one hour, 81 of them against a single item, and half of one
night's entire run count in its last two hours. Lanes now back off while a no-envelope streak is
running, and past a declared streak the fleet drains itself and stops.

## Raising a repair is not deciding to build it

The last step of an item is what it taught, and part of that is raising what the system itself got
wrong as ordinary work. Ordinary work is signed off, researched, decomposed and reviewed. An
improver's proposals were none of those: they became queued items the moment they were written.

They also went under the deliverable whose work provoked them, on the argument that this keeps the
reason and the work in the same place. The reason belongs there; the work does not. Those items
counted against that deliverable's own open-item target, so `SegmentNeedsWork` returned zero, the
planner stopped being dispatched, and the deliverable was starved by its own byproduct. Thirty-nine
of forty-four items arrived that way, after the last thing anybody reviewed.

So they are filed under one deliverable of their own, which starts on the roadmap waiting for a
person, and every item carries the run and item that provoked it in its rationale. Nothing is
dropped — that was the failure the direct-creation path was added to fix, and dropping them
silently would be worse than either. What changed is that the fleet may now *ask* for work without
that being the same act as *starting* it.

Its target is zero open items, deliberately: a planner asked to keep a backlog topped up here would
invent maintenance to fill the quota, which is the failure the improver already has to be argued
out of.

## Generation judges relevance, by shape

Generation is the one place an agent decides what work *exists*, which makes it the one place an
agent can widen its own scope. The admission rules are about shape rather than taste, and one of
those shapes is whether the work belongs to this deliverable at all.

A title can echo any brief; a file scope either lands where the deliverable's work lands or it does
not. So an item whose declared paths fall entirely outside anything the segment touches is refused,
and the refusal quotes the brief back. A reviewed five-item plan once became forty-four items with
nobody having agreed to thirty-nine of them, and 77% of the money went to the fleet improving itself
rather than to the thing that was asked for.

The related rule is that two open items may not declare the same file. Two builders on one file are
serialised however wide the fleet is, so depth in a plan is wall clock and breadth is not — which
makes an overlapping scope a planning defect that looks like ordinary work.

## Package layout

```
cmd/adlc/            the CLI — the only writer to the ledger
internal/ledger/     hash-chained append-only store, three-tier verification, rebuild, blobs
internal/authority/  the transition table, state advancement, generation admission, roadmap
internal/gate/       runs the declared checks; compares claims to observations
internal/dispatch/   selection, routing, isolation, invocation, the scheduled lanes
internal/merge/      the merge queue: rebase, re-gate, fast-forward, clobber guard
internal/server/     the operator dashboard, and the console panel on every page
internal/console/    the console's action vocabulary, fleet briefing and turn parsing
internal/story/      run narratives and decision replay
internal/lease/      ephemeral claims on items and resources
internal/config/     the declared surface that makes this generic, and the scaffold
internal/envelope/   the worker output contract
internal/spend/      token accounting wired to a refusal
internal/prompt/     versioned prompt library, mandatory-clause gating
internal/report/     deterministic projections
agents/              the prompts — one shared preamble, one file per role
.claude/skills/      the four setup and triage skills, which drive the CLI and never bypass it
```
