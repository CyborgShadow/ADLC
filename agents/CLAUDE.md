# agents/

The prompts. One shared preamble, one file per role. This directory is the definition of what every
worker is told — the dispatcher reads it at dispatch time and carries no prompt of its own.

- `_preamble.md` — fleet-wide policy, assembled above every role prompt at dispatch. Edit it once
  and every role changes.
- one file per role, named by its prompt id (`planner.md`, `judge.md`, `validator.md`, …). A role's
  worker declaration in `adlc.json` names which file it runs; `implementer.md` deliberately backs
  more than one role, because what separates those roles is which files they touch and nothing
  else.

## What goes wrong here

**Restating policy in a role file.** Anything true for every worker belongs in `_preamble.md`.
Duplicated into role files it drifts between them at the first edit, and a fleet-wide change becomes
twenty edits with no way to tell which roles received it.

**Deleting a mandatory clause.** `adlc.json` declares clauses that must appear verbatim, and
`adlc prompt check` fails the gate rather than warning — a run must not be able to remove a safety
clause from its own instructions. They are carried by the preamble, so removing one there fails
every prompt at once.

**Writing a role that has no failure of its own.** A role is worth declaring when the generalist
prompt does not warn about the failure mode it owns. `docs/roles.md` states that test and the
reasoning behind each split in the shipped roster; adding a role also means a worker declaration and
a routing entry in `adlc.json`, or the work filed for it is unreachable.

**Telling a worker what state to move to.** No prompt here may name a lifecycle state as an
outcome. A worker reports `pass`, `fail`, `reject` or `blocked`; the control plane computes the
rest. A prompt that invites a state name invites a refusal.

**Renaming a file.** The prompt id comes from the front matter, or from the filename when there is
none. Two files resolving to one id is refused at load: a pin that resolves to two files resolves to
neither. Past runs pinned the bytes they were given, so an edit is a new version rather than a
correction of history.

**Writing prose that only sounds firm.** These files are the voice of the system. State a rule by
naming the failure it prevents; a rule with no consequence attached is one a fresh session will
reason its way past and then defend coherently.

## Checking

```
go run ./cmd/adlc prompt check      # clauses intact, every role's prompt present
go run ./cmd/adlc prompt list       # what exists, what is missing, what nothing names
go run ./cmd/adlc prompt assemble <id>   # the exact text a run is given
```

`prompt list` reports any `.md` file here that no role names — this file included. That is a note,
not a fault.
