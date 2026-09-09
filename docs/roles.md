# Roles and prompts

A role is a config entry plus a prompt file. The dispatcher reads the file at dispatch time and
carries no prompt of its own, so changing a role is an ordinary reviewable edit — and every past
run can still say exactly which bytes it was given.

## The shipped roster

| role | capability | declared areas | what it does |
|---|---|---|---|
| `researcher` | research | *generalist* | Turns an intent into a written approach. Creates no work items |
| `planner` | plan | *generalist* | Decomposes an approach into work items with checkable criteria |
| `engineer` | implement | *generalist* | Builds anything no specialist claims |
| `cli-dev` | implement | cli | Builds the command surface |
| `docs-writer` | implement | docs | Builds the documentation |
| `frontend` | implement | frontend | Builds the surface a person looks at, including the states other than the populated one |
| `backend` | implement | backend | Builds the service tier, for a request that arrives twice and concurrently with another |
| `api` | implement | api | Builds the published contract. Owns whether a change is additive or breaking |
| `database` | implement | database, migration | Owns the stored shape and the migrations that change it |
| `query` | implement | query | Owns the access path: the statements issued, the plans they take, the transactions they run in |
| `sre` | implement | sre, infrastructure | Writes the infrastructure, the deploy path and the signals. Applying is the operator's step |
| `tester` | test | *generalist* | Retired from the lifecycle: the gate executes the declared suite itself. Kept on the roster; no state dispatches it |
| `judge` | judge | *generalist* | Rules on whether the work serves the brief and fits the deliverable |
| `validator` | validate | *generalist* | Adversarial review, over a plan before it is built and a delivered batch after |
| `architect` | validate | architecture | Reviews boundaries, dependency direction, and decisions that are expensive to undo |
| `security` | validate | security | Reviews for what an attacker would do |
| `performance` | validate | performance | Reviews for scale and load. Findings need a number |
| `resilience` | validate | resilience | Reviews for what is left behind when something dies halfway |
| `janitor` | curate | hygiene | Stale docs, dead references, duplicated facts. Low cadence |
| `arbiter` | arbitrate | — | Judges the change against the system rather than against the item |
| `improver` | improve | *generalist* | Records what a landed item taught; raises fixes as work. Low cadence |
| `operator` | operate | platform, ci | Applies changes to real resources. Low cadence |
| `console` | converse | *generalist* | Answers an operator in the dashboard and drafts actions. Advances no item on its own |

Twenty-one prompt files back these twenty-three roles. `cli-dev` and `docs-writer` share
`implementer.md` with the generalist, because what makes them separate roles is which files they
touch and nothing else. The engineering disciplines each have their own file, because what makes
them separate is a failure mode the generalist prompt does not warn about — that is the test for
whether a role is worth declaring at all.

`engineer` declaring no areas is load-bearing rather than an oversight. Routing is consulted
first, and a worker that lists no area is preferred over an unrelated specialist, so an item filed
under an area nobody claimed reaches the generalist instead of whichever specialist happened to
sort first.

The *generalist* rows are roles that declare no areas of their own. Several of them are still the
routed owner of an area — `verification` routes to `judge`, `review` to
`validator`, `core` to `engineer` — which is what an item filed under that area is tagged with. A
declared area narrows what a role will be picked for; a routing entry says who owns work tagged
that way. `console` never appears in routing because it advances no work item.

### Why `database` and `query` are two roles

They fail differently and need different evidence. A migration runs once against data that is
already there, and `git revert` does not undo it — so the evidence it owes is a rehearsal at
realistic size, a lock duration, and a written rollback. A query change is revertible with a
commit, and the evidence it owes is a plan and a row count at production volume. The two are also
prone to opposite mistakes: the schema side adds a column that rewrites a large table, and the
access-path side fixes one read with an index that every write then pays for.

The line between them, when an item could be either: if it changes what is stored, it is
`database`; if it changes how what is stored is reached, it is `query`. Getting an index onto a
live table is `database` even when `query` asked for it, because how it lands is an operational
question rather than an access-path one.

## The handoff

Roles never talk to each other. Each is handed a prompt, does a bounded job, reports a verdict,
and exits — the control plane decides what happens next. A handoff cannot be dropped because it
is arithmetic on recorded state, not a message somebody has to deliver.

```
you            write a deliverable: a title, a brief, how many items to keep in flight
  ↓
you            sign it off — the one planning gate no machine passes on its own
  ↓
researcher     turns the intent into an approach: what exists, the options, which one and why
  ↓
planner        breaks the approach into items with acceptance criteria
  ↓
validator      checks the breakdown against the intent — would these items deliver it?
  ↓            (on a reject it goes back for decomposition; nothing is built until it passes)
builder        implements one item, writes tests for what it wrote, stops
  ↓
control plane  runs the declared suite AND every criterion carrying a command, in the
  ↓            item's own tree. No agent, no lane wait. Work that fails its own
  ↓            criteria goes straight back rather than on to somebody who would
  ↓            discover by hand what a command already established
judge          rules on intent and fit — does this serve the brief, and does it fit the
  ↓            deliverable. The one question no command answers. May reject with blockers
  ↓            (a failure goes back to the builder with what was observed)
janitor        hygiene pass over what landed
  ↓
arbiter        judges the change against the system rather than against the item
  ↓            (the next two happen only if the change touches something real)
operator       applies the change, after an approval that named the exact plan
  ↓
validator      confirms against the applied artifact, not the plan
  ↓
merge queue    rebases, re-gates on the rebased tree, fast-forwards — the control plane itself
  ↓
improver       records what was learned and raises self-improvements as their own items
```

The Coordination page renders this live, with who currently owns each stage and whether their
lane is firing.

## Adding a specialist

Two steps. Declare the role:

```json
{
  "type": "mobile",
  "layer": "worker",
  "description": "Builds the phone client, where a release cannot be rolled back.",
  "prompt": "implementer",
  "capabilities": ["implement"],
  "areas": ["mobile"]
}
```

and route the area to it:

```json
"routing": { "mobile": "mobile" }
```

`adlc-role` does both halves and the prompt file together, then validates with `adlc config check`
and `adlc prompt check`. Doing it by hand is the same three edits.

Write a dedicated prompt when the guidance genuinely differs — a security reviewer looks for
different things than a performance reviewer, and a frontend builder falls into different holes
than a database one. Reuse `implementer.md` when the difference is only which files the role
touches. The question to answer before adding a role is what its prompt would say that no
existing prompt says; if the answer is nothing, the role is a file scope, not a discipline.

**Never add a role without an area that routes to it, and never an area without a role.** An item
filed under an unowned area is unreachable, and an unreachable item looks exactly like one nobody
has got round to yet. The config refuses a routing entry naming an undeclared role.

The other half of that rule is quieter and costs more. A role whose area nobody files work under
never runs, and the Roles page reports it as a red zero — which is correct, and which is why a
speculative roster is worse than a short one. Declare the disciplines the project actually has.

One area has one owner in `routing`, but two roles can still share an area when they hold
different capabilities. `security` owns the `security` area for review; an item tagged `security`
that needs building falls through to the generalist implementer, because the routed owner does not
hold `implement`. The resolution order is: the area's owner if it holds the capability, then a
worker holding the capability that lists the area, then a worker holding the capability that
declares no areas, then anything holding it.

## Writing a prompt

Front matter, then the role text:

```markdown
---
id: database
version: v1
---

# Worker: database

You implement one work item...
```

The shared preamble in `_preamble.md` is assembled above every role prompt at dispatch, so
fleet-wide policy is one edit. The seam is a literal `---` line.

### Variables

Substituted at dispatch. Every key is always present, so an unfilled placeholder never reaches an
agent as literal template text.

| variable | |
|---|---|
| `{{run_id}}` `{{worker_type}}` | the run's identity |
| `{{work_item_id}}` `{{segment_id}}` | what it is working on |
| `{{title}}` `{{state}}` `{{criteria}}` `{{rationale}}` | the item |
| `{{blast_radius}}` `{{resources}}` `{{file_scope}}` | its reach |
| `{{workdir}}` `{{envelope}}` | where to work, where to write |
| `{{brief}}` `{{needed}}` `{{existing_items}}` `{{areas}}` | generation and plan review |

### What every prompt must carry

`prompts.mandatory_clauses` lists text that must appear verbatim in every role prompt. The gate
checks it, so a run cannot delete a safety clause from its own instructions. The shipped set:

- `You never write the ledger.`
- `You never weaken, skip or delete a check to make a gate pass.`
- `You never mark your own work done.`
- `If you could not run something, say so.`

They live in the preamble, which satisfies every role at once.

## What an agent actually has to do

Read a prompt. Do the bounded job. Write an envelope. That is the whole contract.

```json
{
  "envelope_version": "1",
  "run_id": "<ADLC_RUN_ID>",
  "worker_type": "<ADLC_WORKER>",
  "work_item_id": "<ADLC_ITEM>",
  "verdict": "pass",
  "summary": "what you did, what you proved, what you could not",
  "head_sha": "<the commit your work is on>",
  "commands_run": [
    { "check_id": "test", "cmd": "…", "exit_code": 0, "output_tail": "…" }
  ],
  "outputs": {},
  "questions": [],
  "usage": { "input_tokens": 0, "output_tokens": 0 }
}
```

**An agent never names a next state.** It reports `pass`, `fail`, `reject` or `blocked`, and the
control plane computes the rest from the state the item was in, the capability the run held, and
the item's blast radius.

Role-specific output goes in `outputs`: `notes_md` for a researcher or an improver,
`work_items` for a planner or an improver, `criteria` for a judge, `findings` for a
validator, `files_changed` for a builder.

A command the agent honestly could not run is declared `"not_run": true` with a reason, and is
excluded from claim matching entirely. Omitting the line, or writing an exit code it did not see,
is what gets caught.

## Tuning over time

The Roles page shows each role's measured activity — runs, passes, refusals — next to its prompt.
A role with zero runs shows as a red zero rather than as an empty row.

When a role keeps getting refused for the same reason, the prompt is usually the fix. The
refusal reason names what was missing; add it to the role text and pin the version.
