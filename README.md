# adlc

**A control plane for a fleet of coding agents.** It lives alongside whatever you are building,
turns a written goal into reviewed work, dispatches agents to do it, refuses anything it cannot
verify, and shows you the whole thing on a dashboard.

Agents propose. `adlc` decides. Both answers land on an append-only, hash-chained ledger the
agents cannot write to.

```bash
go install github.com/CyborgShadow/ADLC/cmd/adlc@latest
adlc config init -project "…" -source-root src -check 'test:go_test_json:go test -json ./...'
adlc config check && adlc init
adlc schedule run                  # then open http://127.0.0.1:8099
```

Or copy `.claude/skills/adlc-*` into your project and say "set up adlc here". The `adlc-setup`
skill reads your repository, asks about what it could not work out, and runs those commands for
you.

---

## What it does

You describe a deliverable in plain language. From there the tool runs the loop:

```
                  you sign it off
                        │
theory ─▶ roadmap ─▶ signed off ─▶ researched ─▶ planned ─▶ plan validated
                                                                  │
       ┌──────────────────────────────────────────────────────────┘
       ▼
   in progress ─▶ tested ─▶ judged ─▶ validated ─▶ janitored ─▶ arbitrated
                                                                  │
       ┌──────────────────────────────────────────────────────────┘
       ▼
   approved ─▶ applied ─▶ confirmed ─▶ merged ─▶ lessons ─▶ done
       ▲
   only if it touches something real
```

Planning first, because deciding what to build and building it are different jobs with different
failure modes:

- **A researcher** turns your intent into a written approach: what exists, the options, which one.
- **A planner** breaks that approach into work items with acceptance criteria a command can check.
- **A validator** checks the plan against your intent *before any of it is built*.

Then delivery, with each stage done by somebody who did not do the one before it:

- **Builders** implement one item each, in isolation, and write tests for what they wrote.
- **A tester** executes those tests. A suite that matched nothing is a failure, not a pass.
- **A judge** rules on whether the work serves what you asked for and fits the deliverable. The
  criteria a command settles are run by the control plane itself, not by an agent.
- **A validator** reviews adversarially and is the only role that can reject.
- **A janitor** does the hygiene pass; **an arbiter** judges the change against the whole system.
- **Disciplines** — frontend, backend, api, database, query, sre, architect — take the items their
  area routes to, each because it brings judgement the generalist prompt does not warn about.
- **Risk reviewers** — security, performance, resilience — take the items whose area says so.
- **An operator** performs anything that touches a real machine, after you approve the exact plan.
- **The merge queue** lands it, one branch at a time, re-gating on the rebased tree.
- **An improver** records what the item taught and raises fixes as their own work.

You are involved in exactly three places: signing off an idea before it is researched, answering a
question an agent stopped on, and approving something irreversible. All three are one click on the
dashboard.

## Answering questions instead of writing JSON

Four Claude Code skills live in `.claude/skills/`. Each conducts an interview and then runs the
CLI; none hand-assembles configuration the tool could generate and validate itself, and none
proceeds past a command `adlc` refused.

| skill | what it does |
|---|---|
| `adlc-setup` | Asks about the project, its existing checks, how far its changes reach and how much authority the console gets, then generates `adlc.json`, validates it and creates the ledger |
| `adlc-deliverable` | Draws a fuzzy goal out into a brief a researcher can work from, a rationale in your words, and a sensible number of items to keep in flight |
| `adlc-role` | Fills a gap in the roster: the worker declaration, the routing entry and the prompt file, written together and validated |
| `adlc-triage` | Read-only. Says what is blocked and why, what is waiting on you, and which lanes have quietly stopped firing |

Copy the directory into the project where `adlc.json` lives. Claude Code finds skills under
`.claude/skills/` on its own — there is nothing to register.

The CLI is the other path and it is complete: `adlc config init` takes the same answers as flags,
`adlc config check` validates a config with the loader a running fleet uses, and `adlc config show`
prints it with every default filled in.

## What makes it different

**The tool advances the status; agents only report.** An agent is never asked what state work
should move to. It reports `pass`, `fail`, `reject` or `blocked`, and a pure function computes
the rest. An agent cannot name a state that does not exist, or one it is not allowed to take.

**The gate runs the checks itself.** An envelope is a declaration, never evidence. The control
plane executes the declared commands in the run's own tree and compares what it observed against
what the agent claimed:

```
REFUSED  S1-001  in_progress -> verifying
  [claim_discrepancy] for test the verdict is the output, not the exit code:
  the envelope's own output reads RED (zero tests reported a result — a run that
  discovered nothing is a failure, not a pass), the gate observed GREEN (59 tests ran)
```

**Checking is a state, not a schedule.** An item cannot leave `verifying` without a tester, a judge
and a validator, and none of them can be the run that did the work. A verification layer that
quietly stops running is then a queue that visibly stops draining, rather than an absence nobody
notices.

**Absence never renders as a pass.** Verdicts have three values. A missing tool is `UNKNOWN`, a
hang is `RED`, a ledger this binary is too old to read is `UNKNOWN` and not `TAMPERED`. Every
report lists declared roles with zero runs, counted from the registry rather than inferred from
silence. Spend works the same way: a model nobody has priced costs `UNPRICED` rather than nothing,
and a figure priced from the table that shipped in the binary says so rather than passing itself
off as one somebody confirmed.

**An alarm only accuses when it can.** `adlc ledger verify` answers three questions separately.
Broken hashes are `TAMPERED`. A record this build is too old to interpret is `UNKNOWN`. Derived
tables that no longer match a replay of the chain are `STALE PROJECTION`, which `adlc ledger
rebuild` fixes in a second — that used to report as tampering and fired on every ordinary upgrade,
and an alarm that goes off on routine releases is one people learn to ignore.

**Nothing irreversible happens without an approval that names the plan.** Items declare a blast
radius — `none`, `host`, `fleet`, `region`, `global`. Above your threshold an item stops and
waits for a named person, who approves one specific dry-run digest. If the plan changes
afterwards, the approval stops applying.

**Every run is reproducible.** The bytes of each prompt and envelope are retained,
content-addressed. `adlc run replay <id>` re-derives a past decision from the record and tells
you whether it still holds.

## The dashboard

`adlc serve`, or automatically with `adlc schedule run`. Loopback only and no external assets — it
has to work when something has gone wrong, which is the only time anyone opens it. Every control is
a plain form and the refresh is a meta tag; the single piece of JavaScript streams a console turn as
the agent writes it, and removing it costs you the streaming and nothing else.

| page | answers |
|---|---|
| **Overview** | What is running right now: run, role, item, elapsed, tries, tokens, cost |
| **Roadmap** | The deliverables in plain language, their state, what each is waiting for |
| **Progress** | The fleet rollup — everything by stage, each deliverable's completion |
| **Questions** | What the fleet stopped on, with the agent's recommendation, and a box to answer |
| **Approvals** | What is waiting to touch a machine, what it changes, Approve / Reject |
| **Coordination** | The fourteen handoffs in order, who owns each, the lanes, the area routing |
| **Roles** | Every role, its capabilities and areas, its measured activity, its full prompt |
| **Config** | What you can change live — safety policy, spend caps, console authority, lanes — and what stays in the file |
| **History** | Every run in plain language, and the raw hash-chained ledger |
| **Console** | The conversation with the fleet, and every action it proposed |
| **About the ADLC** | The lifecycle explained for a newcomer, with diagrams |

Item, run and deliverable pages hang off those. The banner is the whole status in one line:
*"Waiting on you: 2 blocking questions. 1 approval."* — or *"Nothing needs you. The fleet is
working."*

## Driving it by conversation

A console panel rides on every page. You say what you want in a sentence; it answers in prose and
proposes actions, each with a control beside it. It is not a shortcut past anything — an item it
raises faces the same admission rules a planner's proposals do, and a state change goes through
the same authority a lane's work does. Every turn is a run: dispatched as a declared worker, its
prompt retained, both the ask and the reply on the ledger.

How much of what it proposes it presses itself is declared, because the right answer differs by
project. At `propose` it executes nothing. At `act` it does what the control plane could have done
on its own and only drafts the three gates a person owns — signing off an idea, answering a
blocking question, approving something irreversible. At `full` it presses those too, and the fleet
has no human checkpoint left in it. The record names the console as the actor on every one it
pressed, and your name on every one you did.

It is off until you switch it on, because it invokes an agent and an agent costs money.

## Docs

- [Getting started](docs/getting-started.md) — zero to a fleet building something
- [Configuration](docs/configuration.md) — checks, roles, routing, lanes, budget, safety
- [Roles and prompts](docs/roles.md) — the roster, and tuning it for your project
- [Operating](docs/operating.md) — running it, answering it, stopping it, reading it back
- [Design](docs/design.md) — the decisions this is built on
- [Command reference](docs/reference.md) — every command and exit code
- [Skills](.claude/skills/README.md) — the four of them, and copying them into your own project

## Requirements

Go 1.25 or newer. One dependency (`modernc.org/sqlite`, pure Go) — no cgo, so it cross-compiles
and runs on hosts with no C toolchain. Git, for per-run worktree isolation. An agent CLI of your
choosing.

## Status

Working and used. Every package carries its own tests, including the dashboard and the decision
replay path, and each guard is pinned by both a firing case and a clean case — a test that only
asserts a guard fires passes vacuously the day it starts flagging everything.

## Licence

MIT. See [LICENSE](LICENSE).
