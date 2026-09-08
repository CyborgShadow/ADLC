# adlc

**A control plane for a fleet of coding agents.** It lives alongside whatever you are building,
turns a written goal into reviewed work, dispatches agents to do it, refuses anything it cannot
verify, and shows you the whole thing on a dashboard.

Agents propose. `adlc` decides. Both answers land on an append-only, hash-chained ledger the
agents cannot write to.

```bash
go install github.com/CyborgShadow/ADLC/cmd/adlc@latest
adlc init && adlc schedule run     # then open http://127.0.0.1:8099
```

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
- **A judge** checks the item against its criteria by executing things, never by reading.
- **A validator** reviews adversarially and is the only role that can reject.
- **A janitor** does the hygiene pass; **an arbiter** judges the change against the whole system.
- **Specialists** — security, performance, resilience — take the items their area routes to.
- **An operator** performs anything that touches a real machine, after you approve the exact plan.
- **The merge queue** lands it, one branch at a time, re-gating on the rebased tree.
- **An improver** records what the item taught and raises fixes as their own work.

You are involved in exactly three places: signing off an idea before it is researched, answering a
question an agent stopped on, and approving something irreversible. All three are one click on the
dashboard.

## What makes it different

**The tool advances the status; agents only report.** An agent is never asked what state work
should move to. It reports `pass`, `fail`, `reject` or `blocked`, and a pure function computes
the rest. An agent cannot name a state that does not exist, or one it is not allowed to take.

**The gate runs the checks itself.** An envelope is a declaration, never evidence. The control
plane executes the declared commands in the run's own tree and compares what it observed against
what the agent claimed:

```
REFUSED  S1-001  in_progress -> ready_for_testing
  [claim_discrepancy] for test the verdict is the output, not the exit code:
  the envelope's own output reads RED (zero tests reported a result — a run that
  discovered nothing is a failure, not a pass), the gate observed GREEN (59 tests ran)
```

**Checking is a state, not a schedule.** An item cannot leave `testing` without a tester or
`judging` without a judge, and neither can be the run that did the work. A verification layer that
quietly stops running is then a queue that visibly stops draining, rather than an absence nobody
notices.

**Absence never renders as a pass.** Verdicts have three values. A missing tool is `UNKNOWN`, a
hang is `RED`, a ledger this binary is too old to read is `UNKNOWN` and not `TAMPERED`. Every
report lists declared roles with zero runs, counted from the registry rather than inferred from
silence.

**Nothing irreversible happens without an approval that names the plan.** Items declare a blast
radius — `none`, `host`, `fleet`, `region`, `global`. Above your threshold an item stops and
waits for a named person, who approves one specific dry-run digest. If the plan changes
afterwards, the approval stops applying.

**Every run is reproducible.** The bytes of each prompt and envelope are retained,
content-addressed. `adlc run replay <id>` re-derives a past decision from the record and tells
you whether it still holds.

## The dashboard

`adlc serve`, or automatically with `adlc schedule run`. Loopback only, no JavaScript, no
external assets — it has to work when something has gone wrong, which is the only time anyone
opens it.

| page | answers |
|---|---|
| **Overview** | What is running right now: run, role, item, elapsed, tries, tokens, cost |
| **Roadmap** | The deliverables in plain language, their state, what each is waiting for |
| **Progress** | The fleet rollup — everything by stage, each deliverable's completion |
| **Questions** | What the fleet stopped on, with the agent's recommendation, and a box to answer |
| **Approvals** | What is waiting to touch a machine, what it changes, Approve / Reject |
| **Coordination** | The fourteen handoffs in order, who owns each, the lanes, the area routing |
| **Roles** | Every role, its capabilities and areas, its measured activity, its full prompt |
| **Config** | Lane controls you can change live, the safety policy, the gate checks |
| **History** | Every run in plain language, and the raw hash-chained ledger |
| **About the ADLC** | The lifecycle explained for a newcomer, with diagrams |

The banner is the whole status in one line: *"Waiting on you: 2 blocking questions. 1 approval."*
— or *"Nothing needs you. The fleet is working."*

## Docs

- [Getting started](docs/getting-started.md) — zero to a fleet building something
- [Configuration](docs/configuration.md) — checks, roles, routing, lanes, budget, safety
- [Roles and prompts](docs/roles.md) — the roster, and tuning it for your project
- [Operating](docs/operating.md) — running it, answering it, stopping it, reading it back
- [Design](docs/design.md) — the decisions this is built on
- [Command reference](docs/reference.md) — every command and exit code

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
