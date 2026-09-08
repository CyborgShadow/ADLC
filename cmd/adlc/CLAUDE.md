# cmd/adlc

The control plane binary: the only writer to the ledger. Every subcommand is a way of putting
something to the authority and appending what it decided.

- `main.go` — global flags, the command switch, `withEnv` (config + ledger + leases + prompt
  library), and the exit codes.
- `commands.go` — segment, item, run, question, approval, lease, report, ledger, prompt.
- `commands_gate.go` — `gate run`, `transition propose|table`, `dispatch once|loop|plan`.
- `commands_serve.go` — `serve`, `schedule run|once|status`.
- `commands_story.go` — `run show|steps|prompt|envelope|replay|retry`.
- `commands_config.go` — `config init|check|show`, including the three-field check form.

## What goes wrong here

**Changing an exit code.** They are contract. CI reads them, and so do the skills in
`.claude/skills/`. The distinctions are load-bearing: `3` accuses (the ledger is `TAMPERED`), `5`
does not (this build is too old to interpret the record — upgrade the binary, the chain is fine),
`6` is a red gate and `7` is a gate that could not be run, which is not a pass. `8` is a stale
projection, which `adlc ledger rebuild` fixes. A verifier that reports "I could not tell" as a
failure gets ignored; one that reports it as a pass is worse.

**Deciding something here.** This package parses arguments, gathers facts and prints. The decision
belongs to `internal/authority`, the evidence to `internal/gate`, the record to `internal/ledger`.
A rule implemented in a command is a rule the dashboard, the scheduler and the console do not have.

**Appending without an actor.** `withEnv` defaults it to `cli` rather than leaving it empty: an
unattributable row on an append-only chain cannot be corrected, only annotated.

**Changing a flag or a usage line without the skills.** `.claude/skills/` quotes real flags, real
exit codes and real refusal messages, and goes stale silently — a skill naming a flag that no
longer exists produces a confident failure. If you change this package, check the four skills
against `adlc <command> -h`.

**Printing a command the tool does not have.** `internal/report` is tested on exactly this: every
command a report prints must be one that dispatches. An instruction that does not run is worse than
none, because somebody follows it under pressure.

## Tests

`commands_config_test.go` pins the check-declaration form: the three-field shape is unchanged, a
command keeps its colons, every parameterised verdict rule can be declared, a pattern keeps its
metacharacters, a missing parameter is named here rather than at load, and a spec that cannot be
built is refused. `commands_prompt_test.go` pins that `prompt list` answers on a project with no
prompts yet and exits non-zero while any referenced prompt is absent.
