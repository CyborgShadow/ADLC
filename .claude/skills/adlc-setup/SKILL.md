---
name: adlc-setup
description: Set up adlc in a project — interview the user about what they are building, what already checks it, how far its changes reach, and how much authority the console gets, then generate adlc.json with `adlc config init`, validate it, and create the ledger. Use when someone wants to install, configure, initialise or onboard adlc, or asks what to put in adlc.json.
---

# Set up adlc in a project

You conduct the interview. `adlc config init` writes the config. Never write or edit
`adlc.json` yourself during setup — a hand-assembled config is exactly what this command
exists to prevent, and a config the tool did not generate has not been through the
validator that a running fleet uses.

## Before you ask anything

Read the repository first. Every question you ask that the repo already answers is a
question you should have answered yourself and put to the user as a confirmation.

```bash
adlc help                     # is the binary here at all
ls adlc.json .adlc agents     # is this already set up
```

If `adlc.json` exists, stop and ask whether they want to inspect it (`adlc config check`,
`adlc config show`) or replace it. Replacing needs `-force` and destroys their edits.

Then look for, in this order:

| Look for | Tells you |
|---|---|
| `go.mod` | Go project. Test: `go test -json ./...` (verdict `go_test_json`). Vet: `go vet ./...`. Build: `go build ./...` |
| `package.json` | Read `scripts` — the real `test`, `lint`, `build`, `typecheck` commands are there |
| `pyproject.toml`, `setup.cfg`, `tox.ini` | Python. Look for pytest, ruff, mypy config |
| `Cargo.toml` | Rust. `cargo test`, `cargo clippy` |
| `Makefile` | Read the targets — often the canonical entry points |
| `.github/workflows/*.yml`, `.gitlab-ci.yml`, `azure-pipelines.yml` | **The best source.** Whatever CI runs is what actually checks this project |
| top-level directories | Candidate `-source-root` values (`src`, `internal`, `cmd`, `lib`, `app`) |
| `.git/HEAD`, `git branch --show-current` | Candidate trunk branch |

Do not ask a user what their test command is when it is sitting in their CI config.
Show them what you found and ask whether it is right.

## The interview

Three rounds. Ask each round together, wait for the answers, then move on.

### Round 1 — what this is

1. What is this project called? (Propose the directory or module name.)
2. Which directories hold the source the fleet may touch? (Propose what you found. This
   becomes `-source-root`, repeatable; every tree-walking guard is scoped by it.)
3. What branch does work land on? (Propose the current branch. This is `-trunk`.)
4. What command starts an agent? Offer the common one and say what it means:
   `claude -p --dangerously-skip-permissions @{{prompt}}`. Tell them: it runs with its
   working directory set to the run's workspace, so every path in it must be absolute,
   and the envelope is read from the file at `$ADLC_ENVELOPE`, not from stdout. If they
   have not chosen an agent, leave it empty — the config still validates and the fleet
   refuses to dispatch, which is the honest state.

### Round 2 — what already checks it

Show the commands you found and ask them to confirm, correct or add. You want at least a
test command; a linter, a build and a type-check are each worth having if they exist.

For each check you need an id, a verdict rule and a command, passed as
`-check id:verdict:command` (repeatable). The command keeps its colons.

**`config init` only accepts verdict rules that need no extra parameters:**

| Verdict rule | Green when | Use for |
|---|---|---|
| `exit_zero` | the command exits 0 | linters, builds, type-checks, most things |
| `go_test_json` | tests ran and passed; **zero tests reported is RED** | `go test -json ./...` |
| `output_empty` | the command printed nothing | `gofmt -l .`, formatters that list offenders |
| `output_nonempty` | the command printed something | rarely what you want |

The other rules this build knows — `exit_in`, `output_matches`, `output_not_matches`,
`count_min` — take a parameter (`allowed_exits`, `expect_pattern`, `count_pattern` /
`min_count`) that `config init` has no flag for, and the command refuses rather than
writing a broken config. If the user needs one of those (for example a JS suite gated on
`count_min` with `"numTotalTests":(\d+)` so an empty run reads RED), tell them plainly:
generate the config with the checks that fit, then add that check to `adlc.json` by hand
and run `adlc config check`. Do not silently downgrade it to `exit_zero` — a test runner
that matched nothing usually exits 0, and that is the failure this whole system is built
to catch.

### Round 3 — reach and authority

**Blast radius.** Ask; do not guess. The only case where you may propose an answer without
asking is a project that visibly changes nothing but source in this repository — no
Terraform, Ansible, Kubernetes manifests, deploy scripts, cloud SDK calls, systemd units —
and even then say what you concluded and why, and let them correct it.

> Does anything this fleet builds reach a real machine — deploys, provisioning,
> migrations, infrastructure? And how far do you want it to go without you pressing
> anything?

`-auto-apply-max` is the widest radius applied with no approval row. Anything above it
stops and waits for a named person.

- `none` — nothing irreversible happens unattended. Correct default; start here.
- `host` — one machine
- `fleet` — a group of machines
- `region` / `global` — wider

**Console authority.** The console is the operator talking to the dashboard. It is off by
default because it invokes an agent, and an agent costs money. `-console` takes:

- `off` — no console at all
- `propose` — drafts everything and executes nothing; the operator presses every action
- `act` — executes what the control plane could do on its own. The three gates a person
  owns — signing off an idea, answering a blocking question, approving something
  irreversible — it only drafts
- `full` — executes those three gates too. Say this out loud before they pick it: an
  approval an agent granted itself is not an approval, and a fleet running like this has
  no human checkpoint left in it. The record still says the agent pressed it.

## Generate

Run it. Quote every answer you were given; invent nothing.

```bash
adlc config init \
  -project "their-project" \
  -source-root src -source-root internal \
  -check "test:go_test_json:go test -json ./..." \
  -check "lint:exit_zero:golangci-lint run" \
  -agents agents \
  -agent-command "claude -p --dangerously-skip-permissions @{{prompt}}" \
  -auto-apply-max none \
  -console propose \
  -trunk main
```

**If it refuses, stop.** Show the exact message, fix the input with the user, run it again.
Do not write `adlc.json` yourself to get past a refusal. The messages are specific:

- `-project is required`, `at least one -source-root is required`, `at least one -check is
  required` — the interview missed something
- `"x" is not a verdict rule this build knows` — it lists the alternatives
- `console "x" is not off, propose, act or full`
- `auto_apply_max "x" is not a radius this build knows`
- `adlc.json already exists` — ask before passing `-force`

## Validate

```bash
adlc config check
```

Exit 0 with `adlc.json is valid.` and a summary. **Read out any warnings it printed
verbatim** — the block headed *"This config is valid, and these will still leave work
sitting:"*. Those are capabilities with no role, or roles with no enabled lane. Work that
reaches such a stage stops there and looks exactly like work nobody has got round to.

Non-zero exit means the config does not load. Show the message and fix it together.

## Prompts

`config init` writes a roster of twelve roles, and each names a prompt file that must exist
in the agents directory. These prompt ids:

```
researcher  planner  implementer  tester  judge  validator
janitor     arbiter  improver     systems  console
```

plus `_preamble.md`, which every role gets prepended and which the control plane checks is
intact before it dispatches anyone.

Tell the user those files do not exist yet unless they copied them, and where to get them:
the `agents/` directory of the ADLC repository. Then:

```bash
adlc prompt list     # every prompt found, with its version and content hash
adlc prompt check    # every prompt still carries the mandatory safety clauses
```

Two things to be honest about here:

- `adlc prompt list` **errors out** if the agents directory or `_preamble.md` is missing.
  That error is the answer: the prompts are not there yet.
- Neither `prompt list` nor `prompt check` tells you a role points at a prompt file that
  does not exist. Compare the ids above against what `prompt list` printed yourself, and
  name any that are missing.

## Create the ledger

```bash
adlc init
```

This creates `.adlc/ledger.db` and registers every declared worker, which is what makes
"this role has never run" detectable later — a worker that never runs contributes no rows
to read, so registration cannot come from the runs.

Optionally confirm the checks actually work where they will run:

```bash
adlc gate run
```

A missing tool reports `UNKNOWN`, not a pass. Fix that now: `UNKNOWN` satisfies nothing
later. Exit 6 is RED, exit 7 is could-not-run.

## Report back

Plain sentences, in this order:

1. What you wrote and where — the project name, the source roots, the checks by id and
   command, the trunk, the agent command.
2. The auto-apply maximum and what it means in their words: what stops for them and what
   does not.
3. The console setting and what it may do on its own.
4. Every warning `config check` printed.
5. Which prompt files are still missing, if any.
6. That the budget table shipped with default list prices which they should confirm
   against their provider, and that `config init` sets **no daily cap** — there is no flag
   for one; `budget.per_day_micros` in `adlc.json` is where it goes.
7. The next step: describe something to build (`adlc-deliverable`, or
   `adlc segment create`), then `adlc schedule run` and open http://127.0.0.1:8099.
