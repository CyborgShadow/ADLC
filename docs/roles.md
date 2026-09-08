# Roles and prompts

A role is a config entry plus a prompt file. The dispatcher reads the file at dispatch time and
carries no prompt of its own, so changing a role is an ordinary reviewable edit — and every past
run can still say exactly which bytes it was given.

## The shipped roster

| role | capability | area | what it does |
|---|---|---|---|
| `planner` | generate | — | Decomposes a brief into work items with checkable criteria |
| `engineer` | implement | *generalist* | Builds anything no specialist claims |
| `cli-dev` | implement | cli | |
| `docs-writer` | implement | docs | |
| `test-author` | implement | testing | Writes tests as work items in their own right |
| `janitor` | implement | hygiene | Stale docs, dead references, duplicated facts. Low cadence |
| `judge` | verify | — | Checks items against criteria by executing things |
| `validator` | validate | *generalist* | Adversarial review; the only role that can say done |
| `security` | validate | security | Reviews for what an attacker would do |
| `performance` | validate | performance | Reviews for scale and load. Findings need a number |
| `resilience` | validate | resilience | Reviews for what is left behind when something dies halfway |
| `operator` | operate | platform, ci | Applies changes to real resources. Low cadence |

Nine prompt files back these twelve roles: several implementers share `implementer.md`, and the
differentiation is the area they are routed work from.

## The handoff

Roles never talk to each other. Each is handed a prompt, does a bounded job, reports a verdict,
and exits — the control plane decides what happens next. A handoff cannot be dropped because it
is arithmetic on recorded state, not a message somebody has to deliver.

```
you            write a deliverable: a title, a brief, how many items to keep in flight
  ↓
planner        breaks the brief into items with acceptance criteria
  ↓
validator      checks the breakdown against the brief — would these items deliver it?
  ↓            (on a reject it goes back for decomposition; nothing is built until it passes)
builder        implements one item, writes tests for what it wrote, stops
  ↓
judge          checks the item against its criteria by executing commands
  ↓            (a failure goes back to the builder with the failing output)
validator      adversarial review; may say done, or send it back with blockers
  ↓
operator       applies the change, after an approval that named the exact plan
  ↓
validator      confirms against the applied artifact, not the plan
```

The Coordination page renders this live, with who currently owns each stage and whether their
lane is firing.

## Adding a specialist

Two steps. Declare the role:

```json
{
  "type": "database",
  "layer": "worker",
  "description": "Schema, migrations and query work.",
  "prompt": "implementer",
  "capabilities": ["implement"],
  "areas": ["database"]
}
```

and route the area to it:

```json
"routing": { "database": "database" }
```

Write a dedicated prompt when the guidance genuinely differs — a security reviewer looks for
different things than a performance reviewer. Reuse `implementer.md` when the difference is only
which files the role touches.

**Never add a role without an area that routes to it, and never an area without a role.** An item
filed under an unowned area is unreachable, and an unreachable item looks exactly like one nobody
has got round to yet. The config refuses a routing entry naming an undeclared role.

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

Role-specific output goes in `outputs`: `work_items` for a planner, `criteria` for a verifier,
`findings` for a validator, `files_changed` for a builder.

A command the agent honestly could not run is declared `"not_run": true` with a reason, and is
excluded from claim matching entirely. Omitting the line, or writing an exit code it did not see,
is what gets caught.

## Tuning over time

The Roles page shows each role's measured activity — runs, passes, refusals — next to its prompt.
A role with zero runs shows as a red zero rather than as an empty row.

When a role keeps getting refused for the same reason, the prompt is usually the fix. The
refusal reason names what was missing; add it to the role text and pin the version.
