# Command reference

```
adlc [global flags] <command> [args]
```

## Global flags

| flag | default | |
|---|---|---|
| `-config` | `adlc.json` | the declared project config |
| `-db` | `.adlc/ledger.db` | the ledger |
| `-actor` | `cli` | recorded on every row this invocation writes |
| `-repo` | `.` | repository root |
| `-json` | | machine-readable output where supported |

Set `-actor` to your name for anything you do by hand. It is what the record shows later.

## init

```bash
adlc init
```

Creates the ledger, applies forward-only migrations, and registers the roles the config declares.
Safe to re-run.

## segment — the roadmap

```bash
adlc segment create -id S1 -title "…" [-brief "…"] [-why "…"] [-target N] [-rank N] [-depends-on S0]
adlc segment list
adlc segment advance -id S1 -to paused -why "…"
```

`-brief` is what a planner decomposes; `-target` is how many unfinished items to keep in flight.
Together they make a deliverable autonomous. Without them it is hand-filled, and skips plan
review because there is no agent breakdown to check.

`-why` is the rationale a run cites when explaining why its work mattered.

States: `theory`, `roadmap`, `signed_off`, `researching`, `researched`, `planning`, `planned`,
`validating`, `ready`, `building`, `delivered`, `paused`.

## item — the work

```bash
adlc item create -id S1-001 -segment S1 -title "…" -criterion "…" \
  [-area core] [-radius none|host|fleet|region|global] [-resource "fleet:prod"] \
  [-file path] [-depends-on S1-000] [-why "…"]
adlc item list [-segment S1] [-state done]
adlc item show S1-001
adlc item amend -id S1-001 -field title -value "…" -why "…"
```

At least one `-criterion` is required: an item nobody can verify is one nobody can finish.
Anything with a radius other than `none` must name at least one `-resource`, or the lease cannot
protect it.

`amend` appends a correction with its authority rather than editing the row that was wrong.

## run — reading and reproducing

```bash
adlc run show <id>          # what it was given, what it did, why it mattered
adlc run steps <id>         # a flat timeline
adlc run prompt <id>        # the exact text it was handed
adlc run envelope <id>      # exactly what it reported back
adlc run replay <id>        # re-derive the decision and compare
adlc run retry <id>         # re-open a rejected item for rework
adlc run list
adlc run start -id … -worker … [-item …]   # record a run's facts by hand
adlc run finish -id … [-envelope path]
```

`replay` exits 0 when the decision reproduces and 2 when it does not.

## gate

```bash
adlc gate run [-workdir .] [-from in_progress -to ready_for_testing] [-artifact sha256:…]
```

Runs the declared checks here and reports what was observed. With `-from`/`-to`, only the checks
that gate that edge.

Exits 6 if red, 7 if it could not be run.

## transition

```bash
adlc transition table
adlc transition propose -item S1-001 -to ready_for_testing [-run …] [-worker …] [-pm] [-envelope path] [-reason "…"]
```

`table` prints all 35 edges with who may propose each and what must be true. Everything not on it
is refused.

`propose` is the manual path; the dispatcher uses the same authority.

## dispatch

```bash
adlc dispatch plan     [-capability … -areas a,b -worker …]
adlc dispatch once     [same]
adlc dispatch loop     [-every 60s] [-max N]
```

`plan` shows what would be picked, in order, and why — the first thing to run when nothing seems
to be happening.

## schedule

```bash
adlc schedule run [-serve] [-addr 127.0.0.1:8099]
adlc schedule once -loop verify
adlc schedule status
```

`run` starts every declared lane on its cadence, plus the dashboard. `status` reports derived
liveness: `live`, `STALE`, `NEVER RUN`, `disabled`.

## question

```bash
adlc question raise -id Q-1 -text "…" -lean "…" [-item …] [-evidence "…"] [-blocking]
adlc question answer -id Q-1 -answer "…"
adlc question list [-open]
```

`-lean` is required. A question with no recommendation hands back the analysis the run was
dispatched to do.

## approval

```bash
adlc approval request -id AP-1 -item S1-001 [-plan digest] [-summary "…"]
adlc approval decide -id AP-1 -verdict approve -approver you [-note "…"]
adlc approval list
```

Approving requires a name. An approval nobody's name is on is an approval nobody gave.

## lease

```bash
adlc lease acquire -key S1-001 -run <id> [-worker …] [-resource "fleet:prod"] [-subject "…"]
adlc lease release -key S1-001 -run <id>
adlc lease list
```

Exits 2 when a live sibling holds the key or an overlapping resource.

## report

```bash
adlc report fleet
adlc report segment S1
```

## ledger

```bash
adlc ledger verify
adlc ledger events [-from 1] [-limit 40] [-kind run.started]
adlc ledger head
```

`verify` exits 3 on `TAMPERED` and 5 on `UNKNOWN`.

## prompt

```bash
adlc prompt list
adlc prompt show <id>
adlc prompt assemble <id>    # preamble + role, as an agent receives it
adlc prompt check            # every mandatory clause still present
```

`check` exits 4 if a clause is missing. Run it in CI.

## serve

```bash
adlc serve [-addr 127.0.0.1:8099]
```

Loopback only. A non-loopback or empty host is refused with the reason.

## Exit codes

| code | meaning |
|---|---|
| 0 | ok |
| 1 | usage or write error |
| 2 | a transition was refused, or a replay disagreed |
| 3 | the ledger is **TAMPERED** — the record does not describe itself |
| 4 | a prompt lost a mandatory safety clause |
| 5 | the ledger is **UNKNOWN** to this build — upgrade the binary; the chain is fine |
| 6 | the gate is **RED** |
| 7 | the gate **could not be run** — which is not a pass |

3 and 5 are separate deliberately. A binary too old to read part of a record has not found
tampering, and a detector that accuses at its own obsolescence gets ignored.

## Environment given to an agent

| variable | |
|---|---|
| `ADLC_RUN_ID` | the run's single name |
| `ADLC_WORKER` | the role it was dispatched as |
| `ADLC_ITEM` | the work item, if any |
| `ADLC_PROMPT` | path to the assembled prompt |
| `ADLC_ENVELOPE` | **where to write the envelope** |

The same values are substituted into `dispatch.command` as `{{run_id}}`, `{{worker}}`,
`{{item}}`, `{{prompt}}`, `{{envelope}}` and `{{workdir}}`.
