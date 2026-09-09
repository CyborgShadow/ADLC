# ADLC

`adlc` is a control plane for a fleet of coding agents. It runs a delivery lifecycle alongside
whatever is being built: a written goal becomes reviewed work, agents are dispatched to do it, and
anything the tool cannot verify itself is refused. **Agents propose. `adlc` decides. Both answers
land on an append-only, hash-chained ledger the agents cannot write to.**

This repository is the control plane, and it is also gated by it — `adlc.json` declares the checks,
roles and lanes that a change to this tree has to satisfy. `README.md` has the shape of the
lifecycle; `docs/design.md` states the decision behind every constraint below, one per section.

## The invariants

These are what a change must leave standing. Each exists because of a failure it prevents, and the
failure is the reason — not the rule.

1. **The control plane is the only writer to the ledger.** Workers emit an envelope; `cmd/adlc`
   decides what it earns and appends the result. A worker with write access to its own audit
   record is not auditable.
2. **Refusals are recorded, not just admissions.** A system that records only what it accepted
   cannot answer the question it will actually be asked, which is what went wrong.
3. **Agents never name their own next state.** A run reports `pass`, `fail`, `reject` or `blocked`;
   `authority.NextState` is a pure function of state, capability, verdict and blast radius, and is
   the only thing that decides where work goes. That removes a whole class of failure and is what
   makes a past decision re-derivable — a decision that depends on what an agent happened to write
   cannot be replayed at all.
4. **The gate runs the checks itself.** An envelope is a declaration, never evidence.
   `internal/gate` executes the declared commands in the run's own tree; the agent's account
   survives only as a claim to compare, and a disagreement is its own recorded refusal.
5. **A verdict has three values and absence never renders as a pass.** `GREEN`, `RED`, `UNKNOWN`.
   A missing tool is `UNKNOWN`, a hang is `RED`, a ledger this build is too old to read is
   `UNKNOWN` and not `TAMPERED`. A run that discovered zero units of work is a failure. Declared
   roles with no runs are counted from the registry, never inferred from silence.
6. **Nothing irreversible happens without an approval bound to a plan digest.** Above the
   configured blast radius an item stops for a named person who approves one specific dry-run
   digest; if the plan moves afterwards the approval stops applying. An unrecognised radius fails
   closed.
7. **Render from the record, never edit the render.** Every figure on every surface is a
   projection of the chain. Projections are derived and disposable; the chain is not.

## Where to go

| If the task is | The package that owns it | What not to touch |
|---|---|---|
| A lifecycle state, an edge, who may propose it, what it requires | `internal/authority/` | Do not add a query to a rule — facts arrive through `Facts`. Do not let an agent verdict name a state. |
| How a check is run, or which channel its verdict is read from | `internal/gate/`, with the rule declared in `internal/config/` | Do not give the claim matcher a second way to read a verdict; one definition, read by both. |
| A check, role, routing entry, lane, budget or safety threshold | `adlc.json`, validated by `internal/config/` | Do not add a dashboard form for checks, workers or routing — `internal/config/setters.go` says why. |
| Recording something new, or changing what is stored | `internal/ledger/` | Do not write a projection table outside `apply()`. Do not change an existing event kind's payload. |
| A dashboard page, figure or control | `internal/server/` | Do not bind off loopback. Do not add a write path beyond answering, deciding, sign-off and lane cadence. |
| How work is selected, isolated, invoked, or how lanes fire | `internal/dispatch/` | Do not let the run id, the workspace directory and the lease key drift apart. |
| How a branch lands on the trunk | `internal/merge/` | Do not hold the lock across the gate run, and do not weaken the clobber guard. |
| What a role is told to do | `agents/*.md` and the worker declaration in `adlc.json` | Do not restate fleet-wide policy in a role file; it lives once in `agents/_preamble.md`. |
| A command, a flag, an exit code | `cmd/adlc/` | Exit codes are contract — CI and `.claude/skills/` read them. |
| Cost, pricing, budget refusals | `internal/spend/` (defaults in `internal/config/pricing.go`) | An unpriced model costs `UNKNOWN`, never zero; a cap of zero means unlimited. |
| Claims on items and machines | `internal/lease/` | Leases are never committed, and the key is the item id canonicalised — never a phrase an agent composes. |
| A run's narrative, or replaying a past decision | `internal/story/` | Replay re-derives the decision, not the agent. Do not claim otherwise on any surface. |
| A deterministic report | `internal/report/` | Count the declared roster; never infer a role's activity from its silence. |
| The shape of worker output | `internal/envelope/` | Nothing here returns a verdict — only what a worker claimed one was. |
| Driving the tool by talking to it | `internal/console/` | The console gets no authority the control plane did not already have; the three human gates it only drafts. |
| The versioned prompt library and its clause gate | `internal/prompt/` | Do not carry a prompt in code. The file is the definition. |

Most subdirectories above carry their own `CLAUDE.md` with the specific trap in that package. Read
it before editing there.

## Build and check

These are the commands the gate itself runs, declared in `adlc.json`:

```
gofmt -l ./cmd ./internal          # the OUTPUT is the verdict; it exits 0 either way
go vet ./...
go build ./...
go test ./...                      # CI uses -count=1: a cached PASS reports on an earlier tree
go run ./cmd/adlc prompt check     # every role prompt still carries every mandatory clause
go run ./cmd/adlc config check     # the whole declared surface, through the loader a fleet uses
```

`go run ./cmd/adlc ledger verify` walks the chain when there is a ledger at `.adlc/ledger.db`; it
answers integrity and knowledge separately, so read the verdict rather than the exit status alone.
CI also runs `go test -race -count=1 ./...` as a separate job, because a host with no C toolchain
cannot run the detector at all and "not run" must never be recorded as "passed".

## Stopping a running fleet

**Never kill the control plane process.** It holds agents that are mid-build, and killing it takes
them with it — the reaper then records every one as `UNKNOWN`, which is real money spent on work
nobody will ever see. That is not a hypothetical: it is what `internal/dispatch/drain.go` was
written to end, after six engineer runs went that way in one evening.

```
go run ./cmd/adlc schedule stop            # asks; waits up to 30m for work in flight
go run ./cmd/adlc schedule stop -wait 120  # same, but stop watching after 2m
go run ./cmd/adlc schedule status          # what is in flight, and which lanes are STALE
```

`schedule stop` writes a stop file under `.adlc/runs/`, which any running control plane reads on
its next selection pass. Dispatch halts immediately; runs already in flight are left to finish,
because they are being paid for either way and letting them land is the only thing that makes
stopping cheap. Ctrl+C at the waiting prompt stops the waiting, not the fleet. A fresh control
plane calls `ClearStop`, so a previous instruction never shuts down the next one.

`taskkill /F`, `kill -9` and closing the terminal are all the same mistake wearing different
clothes. Reach for one only when `schedule stop` itself is broken, and expect to pay for every run
it interrupts.

## House style

- Comments explain **why**, not what. A comment that restates the mechanics of the line below it is
  noise; a comment naming the defect the line prevents is the only durable record of it.
- Explain a rule by naming the failure it prevents. That applies to code comments, refusal
  messages, dashboard copy and these files alike — a refusal an agent cannot classify is one it
  will work around rather than fix.
- Every guard needs a firing case **and** a clean case. A test that only asserts a guard fires
  passes vacuously the day that guard starts flagging everything. A tree-walking guard also needs a
  stated scope; `source_roots` in `adlc.json` is it.
- British-leaning spelling, as used throughout: behavioural, artefact, canonicalised, licence.
- Plain, concrete, calm. No marketing tone.
