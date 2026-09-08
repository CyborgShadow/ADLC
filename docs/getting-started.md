# Getting started

Fifteen minutes from nothing to a fleet building something.

## 1. Install

```bash
go install github.com/CyborgShadow/ADLC/cmd/adlc@latest
cd /path/to/your/project
```

## 2. Set it up by answering questions

The repository ships four Claude Code skills in `.claude/skills/`. Copy them into the project
where `adlc.json` will live and `adlc-setup` runs the setup as an interview: it reads your
repository first, asks what it could not work out, and then runs `adlc config init` for you.

```bash
mkdir -p .claude/skills
cp -r /path/to/ADLC/.claude/skills/adlc-* .claude/skills/
```

Then say what you want — "set up adlc here" — and the skill is picked up from its description.
There is nothing to register.

| Skill | What it is for |
|---|---|
| `adlc-setup` | Generates and validates `adlc.json`, then creates the ledger |
| `adlc-deliverable` | Turns a goal into a brief a researcher can work from, and registers it |
| `adlc-role` | Adds a role: the worker declaration, the routing entry and the prompt file together |
| `adlc-triage` | Read-only. Says what is blocked, what is waiting on you, which lanes have stopped |

The skills never hand-assemble JSON that `adlc` could generate and validate itself, and they stop
at the first command the CLI refuses. `.claude/skills/README.md` covers copying them to
`~/.claude/skills/` so they are available in every project rather than one.

## 3. Or set it up with flags

`adlc config init` is the same path without the conversation.

```bash
adlc config init \
  -project "your project" \
  -source-root src -source-root cmd \
  -check 'test:go_test_json:go test -json ./...' \
  -check 'lint:output_empty:gofmt -l ./src' \
  -agent-command 'claude -p --dangerously-skip-permissions @{{prompt}}' \
  -console act
```

A check is `id:verdict:command`. A verdict rule that reads parameters takes them in brackets after
its name, and two parameters are separated by a semicolon:

```
tests:count_min[count_pattern="numTotalTests":(\d+)]:npm test
plan:exit_in[allowed_exits=0,2]:terraform plan -detailed-exitcode
lint:output_matches[expect_pattern=^0 problems]:npx eslint .
```

At least one check is required — a gate with no checks reports green over nothing. The other flags
are `-agents` (where prompt files live, default `agents`), `-trunk` (the branch the merge queue
lands on, default `main`), `-auto-apply-max` (the widest blast radius applied with no person,
default `none`), `-console` (`off`, `propose`, `act` or `full`) and `-force` to overwrite an
existing config.

What comes out is a complete roster: one role per capability, the generalists, the seven
engineering disciplines, ten lanes staggered so no two fire on the same second, and the routing
entries that make each area reachable. Every value is editable afterwards.

Hand-writing `adlc.json` is still possible and is how the two configs in this repository were
made — `adlc.json` for a Go codebase, `examples/infra.adlc.json` for provisioning work. It is not
the recommended start. A hand-written config fails in one of two ways: it is refused at load with
a message about a field, or it loads and then quietly does nothing because a capability has no
role or a role has no lane.

Then check it and create the ledger:

```bash
adlc config check     # what is declared, and whether the loader accepts it
adlc init             # creates .adlc/ledger.db and registers the roles
```

`config check` prints the counts and the two settings that decide how much the fleet does
unattended — the auto-apply radius and the console's authority. `-quiet` prints nothing and
answers with the exit code, which is what to run in CI.

The generated config names prompt files that do not exist yet. Copy them from this repository's
`agents/` directory; `adlc prompt list` says which are missing.

## 4. Check the gate before you rely on it

```bash
adlc gate run
```

This runs every declared check right here and prints what it observed. If a tool is missing you
get `UNKNOWN` rather than a pass — fix that now, because `UNKNOWN` satisfies nothing later.

## 5. Set the agent command

```json
"dispatch": {
  "command": ["claude", "-p", "--dangerously-skip-permissions", "@{{prompt}}"],
  "workdir_template": ".adlc/workspaces/{{run_id}}",
  "isolation": "worktree",
  "max_attempts": 3
}
```

Two things to know. The command runs with its working directory set to the run's **workspace**,
so every path in it must be absolute. And the envelope is read from the file at `$ADLC_ENVELOPE`
(also substituted as `{{envelope}}`), not from standard output.

Any process works: a coding CLI, a shell script wrapping an HTTP API, anything that can read a
prompt and write a JSON file.

## 6. Confirm the prices

`config init` writes a price table rather than an empty one, so a fleet reports what it is
spending from the first run. Those are list prices as of early 2026 and they are yours to
confirm: providers change rates, and a cost report is only worth reading if somebody checked the
numbers behind it once.

Every cost figure carries where its rate came from. `configured` means your own table priced it,
`default` means the table that shipped in the binary did, and `unpriced` means neither — cost
unknown, which is not the same as zero. The Config page marks a project still running entirely on
defaults, and per-model rates are editable there as dollars per million tokens.

```json
"budget": {
  "price_micros_per_mtok": {
    "your-model": { "input": 3000000, "output": 15000000,
                    "cache_read": 300000, "cache_write": 3750000 }
  },
  "default_model": "your-model",
  "per_day_micros": 50000000
}
```

The fallback is per model, not per table. Pricing two models and forgetting a third leaves your
two figures alone and prices the third from the defaults; a model in neither table stays
`UNPRICED` rather than reporting as free.

## 7. Describe something you want

`adlc-deliverable` conducts this as an interview. The command underneath it is:

```bash
adlc -actor you segment create \
  -id S1 \
  -title "Password reset that works" \
  -brief "A user who has forgotten their password can get back in without contacting support. Single-use token, expires in an hour, rate limited, and the reset itself never reveals whether an address is registered." \
  -why "Support spends about a day a week on this and it is the top driver of churn in the first month." \
  -target 5
```

`-brief` is what a planner decomposes. `-target` is how many unfinished items to keep in flight
— without it the deliverable is hand-filled and no planner runs against it. `-why` is what shows
up months later when somebody asks what a particular run was for.

## 8. Start the fleet

```bash
adlc schedule run
```

Every declared lane starts on its own cadence and the dashboard comes up on
<http://127.0.0.1:8099>.

What happens, in order:

1. Your deliverable starts as a `theory`. It sits there until you sign it off — the one planning
   gate no machine passes on its own. Do that on the Roadmap page.
2. The **research** lane dispatches a researcher, which writes down an approach: what exists,
   what the options are, which one and why. The deliverable becomes `researched`.
3. The **plan** lane dispatches a planner. It proposes work items; each is admitted or refused
   against the same kind of rules a transition faces. The deliverable becomes `planned`, and
   **nothing under it is dispatched yet**.
4. The **review** lane checks the breakdown against your intent. On a pass the deliverable
   becomes `ready` and the work opens; on a reject it goes back for decomposition.
5. The **build**, **test**, **judge**, **review**, **curate** and **arbitrate** lanes drain it,
   item by item, each stage done by somebody who did not do the one before it.
6. Anything with a blast radius above your threshold stops on the Approvals page.
7. Cleared work goes through the merge queue — the control plane's own step — and then the
   **improve** lane records what the item taught before marking it done.

Watch it on the Roadmap and Overview pages. If an agent hits something ambiguous it stops and
asks — the Questions page shows the question with the agent's own recommendation, and answering
it unblocks the item.

## 9. Talk to it

If you set `-console` to anything but `off`, a panel rides on every dashboard page and the
Console page holds the whole conversation. Ask it what is happening, or tell it what you want, and
it answers in prose and proposes actions with a control beside each one.

`console.authority` decides how many of those it presses itself. At `propose` it executes nothing.
At `act` it does what the control plane could have done on its own and only drafts the three
things a person owns — signing off an idea, answering a blocking question, approving something
irreversible. At `full` it presses those too, and the fleet has no human checkpoint left in it.
Start at `propose` or `act`; the setting is on the Config page and takes effect on the next turn.

Every turn is a run: it is dispatched as a declared worker, its prompt is retained, and what it
did is on the ledger under the console's own name rather than yours.

## 10. Stop it

Ctrl+C. Each lane finishes the dispatch it is in and exits. Nothing is held in memory — every
outcome is already on the ledger — so starting it again resumes exactly where it was.

## If nothing is happening

```bash
adlc dispatch plan       # what would be picked next, in order, and why
adlc schedule status     # which lanes are live, stale, or have never fired
adlc report fleet
```

Or ask `adlc-triage`, which runs those and reads the answers back in plain language.

The most common causes, in order of likelihood:

- **The deliverable is still `planned`.** Its breakdown has not been reviewed. Run the review
  lane, or check that some role declares the `validate` capability.
- **An item is filed under an area nothing routes to.** The dispatcher logs `UNREACHABLE` with
  the area name.
- **A blocking question is open.** The item is parked until it is answered.
- **A lane is paused.** Check the Config page.

## Next

- [Configuration](configuration.md) for everything in `adlc.json`
- [Roles and prompts](roles.md) for tuning the roster to your project
- [Operating](operating.md) for reading the record back
