# Getting started

Fifteen minutes from nothing to a fleet building something.

## 1. Install and initialise

```bash
go install github.com/CyborgShadow/ADLC/cmd/adlc@latest
cd /path/to/your/project
```

Copy a config to start from. There are two in the repository: `adlc.json` for a Go codebase, and
`examples/infra.adlc.json` for provisioning and hardening work.

```bash
curl -O https://raw.githubusercontent.com/CyborgShadow/ADLC/main/adlc.json
mkdir agents && cd agents
# copy the prompt files from the repository's agents/ directory
```

Then:

```bash
adlc init
```

That creates `.adlc/ledger.db` and registers the roles your config declares. It prints what it
found, including the auto-apply threshold, so you can see immediately how much the fleet is
allowed to do unattended.

## 2. Point it at your toolchain

Open `adlc.json` and edit `source_roots` and `checks` so they describe your project rather than
this one. A check is a command, the channel its verdict is read from, and the lifecycle edges it
gates:

```json
{
  "id": "test",
  "command": ["npm", "test", "--", "--reporter=json"],
  "verdict": "count_min",
  "count_pattern": "\"numTotalTests\":(\\d+)",
  "min_count": 1,
  "required_for": ["in_progress->ready_for_testing", "testing->ready_for_review"]
}
```

Check it works before you rely on it:

```bash
adlc gate run
```

This runs every declared check right here and prints what it observed. If a tool is missing you
get `UNKNOWN` rather than a pass — fix that now, because `UNKNOWN` satisfies nothing later.

## 3. Set the agent command

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

## 4. Set your prices

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

The table ships empty on purpose. With no entry, runs report `UNPRICED` — cost unknown, not zero
— and the dashboard says so rather than printing `$0.00` over real spend. Fill it in before
running unattended, or the cap can never be reached.

## 5. Describe something you want

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

## 6. Start the fleet

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

## 7. Stop it

Ctrl+C. Each lane finishes the dispatch it is in and exits. Nothing is held in memory — every
outcome is already on the ledger — so starting it again resumes exactly where it was.

## If nothing is happening

```bash
adlc dispatch plan       # what would be picked next, in order, and why
adlc schedule status     # which lanes are live, stale, or have never fired
adlc report fleet
```

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
