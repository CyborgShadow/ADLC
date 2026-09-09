# internal/dispatch

Selects the work, mints the run's identity, isolates it, invokes an agent, and puts the result to
the authority.

- `dispatch.go` — `Candidates`, `TickScoped`, `dispatchOne`, generation admission, prompt variables.
- `schedule.go` — the declared lanes, their firing, and the tick record their liveness is derived
  from.
- `workspace.go` — the per-run git worktree.
- `runner.go` — `ExecRunner`, the default way an agent process is started.
- `breaker.go` — what a dispatch that produced no envelope earns: `unknown`, a recorded refusal,
  the agent's own output on the chain, a lane backoff, and a fleet that stops itself.
- `stream.go` — `LineWriter` and `TailBuffer`, for a caller watching a run in progress.
- `merge.go` — the merge lane's use of `internal/merge`.

## What goes wrong here

**Treating streamed output as a result.** `Invocation.OnOutput` exists so the console can show a
turn as it happens. It is a view: the envelope is still read from the FILE the runner named, for
the reason it always was — an agent that narrates its reasoning would otherwise bury its own
result, and a parser reading stdout would end up guessing which JSON object was the real one. A
runner that ignores `OnOutput` entirely is still a correct runner.

**Decoding inside a runner.** `OnOutput` hands over one line at a time, without its newline, and
nothing more. Whoever is watching decides what a line means — the console does, in
`internal/server/stream_json.go`. Putting a particular agent's output format in here would be the
first vendor in a control plane that has none, and it would make two runners behave differently
for no reason. (They did, briefly, and the console dropped every newline on one of the two paths.)

**Picking from the wrong end.** Work is drained from the *finished* end: `authority.CapabilityFor`
returns a priority and a lower number is dispatched first, so verification is picked before new
implementation. Reverse it and the verification layer goes dark exactly the way a separately
scheduled one did — the queue keeps filling and nothing tells you.

**Letting the alphabet route.** The item's area resolves the owning worker before anything else. A
specialist roster is decorative if the picker takes whichever worker with the right capability sorts
first.

**Letting a run's names diverge.** The run id, the workspace directory and the lease key are one
string, minted and collision-checked before the work is picked. A run whose names diverged was
invisible to the guard keyed on the workspace and to the guard keyed on the id, and both reported
clean.

**Claiming after the fact.** The lease is taken before the work starts. A claim taken at the end
records a collision rather than preventing one.

**Cutting a workspace from the trunk, or from a branch that is not ahead of it.** A builder's
commit sits on its own branch until the merge lane lands it, so a workspace cut from the trunk
hands the tester, the judge and the reviewer a tree without the change they were dispatched to
examine — and they report on the trunk believing they reviewed it. `workspaceBase` is that rule,
and it is deliberately not `branchFor`; its doc comment says why the merge queue's answer is the
wrong one here.

**A lane that fires and writes nothing.** Every firing writes a tick, including the idle ones. A
lane that stops firing then stops producing ticks and becomes a derived alarm; a lane that only
records dispatches is indistinguishable from a lane with nothing to do.

**Treating a killed run as a failure of the work.** A run that timed out or died leaves `UNKNOWN`,
not `fail`. Accurate is the point; converting it to a verdict invents evidence. The same applies to
a run that never *started*: a dispatch whose agent errored and wrote no envelope is `unknown`, and
it was `fail` for long enough to fill a ledger with 295 broken changes nobody had made.

**A failure path that returns before it records.** The no-envelope path used to `return` above
`recordRefusal`, so `no_envelope` read zero on the fleet report against 276 real occurrences — the
largest failure mode in the record, visible nowhere. Every exit from `dispatchOne` records what it
decided, including the ones that decided nothing happened.

**A failure path that discards what the run cost.** A run that consumed tokens and then died is not
a free run. Every `finish` carries the usage that is actually known — the envelope's, the runner's,
or what `salvageUsage` can read back out of an envelope the parser refused — and absence stays
absence rather than becoming a confident zero.

**A cadence that does not notice it is failing.** Lanes back off on consecutive no-envelope
dispatches and the fleet drains itself past `maxNoEnvelopeStreak`. Without it a broken invocation
fires on cadence at a hundred per cent failure: 212 runs in one hour, 81 of them on one item.

**Enforcing the rework limit by making the item invisible.** The limit bounds BUILDING. Applied to
the whole item it removed at-limit work from every lane, so finished work could not even be
reviewed and two items sat in `in_progress` at 3 of 3 that nothing could see and nothing escalated.
`escalateAtLimit` raises the blocking question and drives the declared `rejected -> blocked` edge.

**Carrying a prompt in code.** The dispatcher reads the file from `internal/prompt` at dispatch
time and assembles the shared preamble above it. Anything hard-coded here gives the fleet two
prompts for one role and no way to tell which one a run was given.

**Admitting generated items on taste.** `authority.AdmitItem` judges *shape* — a free id, a
declared area with an owner, at least one checkable criterion, a known radius, a named resource for
anything that reaches a machine. Both admissions and refusals are recorded.

## Tests

`dispatch_test.go` runs a whole dispatcher over a fake runner: the area decides the worker,
verification is picked before new implementation, a lane only sees its own work, a leased item is
skipped rather than stolen, a segment under its target asks for work, generated items are admitted
or refused and both are recorded, an idle loop still writes a tick, a loop that never fired is not
silent, a killed run leaves `UNKNOWN`, and the run id, workspace and lease are one string.
`merge_test.go` covers the merge lane's held and empty cases. `workspace_test.go` builds real
repositories: a workspace carries the item's unmerged branch, and — the clean case, without which
the rule could quietly widen — stays on the trunk when this item's branch is not ahead of it and
when the branch that is ahead belongs to somebody else. It also covers `copyTree`'s declared roots.
`breaker_test.go` pins the no-envelope path against the night it was written for: `unknown` rather
than `fail`, the refusal recorded, the agent's output on the chain, the streak that stops the fleet
and the failing-but-working run that must not, the per-segment cap firing and an undeclared one
refusing nothing, the queue wait and the dispatching process on the record, and usage surviving
both a reap and a malformed envelope without being invented where there is none.
