# Configuration

Everything project-specific lives in one file, `adlc.json`. It is a gated artefact: the fleet
reads it, and changing it is an ordinary reviewable edit.

Generate it with `adlc config init` rather than writing it by hand — see
[getting started](getting-started.md). `adlc config check` re-validates it with the loader a
running fleet uses, and `adlc config show` prints it with every default filled in.

Any object may carry a `_comment` string; it is ignored.

## What the dashboard may change

Some of this file is editable from the Config page and some is not, and the line is drawn at
whether the setting is a *policy* decision or a *structural* one.

Editable live: the safety policy, the spend caps and per-model prices, the console's authority and
whether it is on, the rework budget, the per-run timeout, the dashboard's own refresh rate, and
each lane's cadence and pause switch. Those are judgement calls somebody makes and revises, often
during an incident, and making them require an editor and a restart means they get made badly or
not at all. Every one is written back to `adlc.json`, and a policy is validated whole before any
part of it is written — a half-applied safety policy is worse than either the old one or the new
one, because nobody can say which rules were in force.

File-only: checks, workers and routing. A check is a command line, and a form that writes
arbitrary argv into something the control plane will execute is a remote shell wearing a hat;
"it is only bound to loopback" is the kind of reasoning that ages badly. Workers and routing are
structure — they belong in a commit somebody reviewed, next to the prompt files they name.

## source_roots

```json
"source_roots": ["cmd", "internal", "agents", "site", "adlc.json", ".gitattributes"]
```

Scopes every tree-walking guard. An unscoped walk counts vendored dependencies, build output and
generated files as uncommitted work, and refuses runs over a tree nobody edited. Required.

A root may be a plain file as well as a directory. The two files above are the ones that configure
the guards themselves: left out of this list, `adlc.json` could be edited and the gate would still
report a clean tree over it.

## checks

A check is a command, the channel its verdict is read from, and the lifecycle edges it gates.

```json
{
  "id": "fmt",
  "kind": "source",
  "command": ["gofmt", "-l", "./cmd", "./internal"],
  "verdict": "output_empty",
  "required_for": ["in_progress->verifying", "verifying->verifying", "verifying->reviewed"]
}
```

| field | meaning |
|---|---|
| `id` | Stable. The envelope's `commands_run[].check_id` joins to it. |
| `kind` | `source`, `dryrun` or `behavioural`. |
| `command` | argv. Never passed through a shell — a shell would make the recorded command and the executed command two different strings. |
| `dir` | Working directory, relative to the run's workspace. |
| `verdict` | Which channel carries the answer. See below. |
| `required_for` | Edges as `"from->to"`. A check gates only the edges it names. |
| `timeout_seconds` | Default 900. A timeout is `RED`, never pending. |

### Verdict rules

| rule | the verdict is |
|---|---|
| `exit_zero` | the exit code |
| `exit_in` | the exit code, against `allowed_exits` |
| `output_empty` | **the output.** For a tool that lists problems and exits 0 either way |
| `output_nonempty` | the output |
| `output_matches` | `expect_pattern` matched the output |
| `output_not_matches` | it did not |
| `go_test_json` | how many tests reported a result. **Zero is a failure** |
| `count_min` | an integer captured by `count_pattern`, against `min_count` |

`count_min` is the one to reach for whenever a tool can succeed having examined nothing — a test
filter that matched no tests, a play that reached no hosts, a scanner given an empty scope. Those
exit 0 and tell you nothing, and a green that means "I ran nothing" is worse than a red.

`exit_in` exists because some tools use exit codes as information rather than as failure — a plan
that exits 2 for "there are changes" is a success.

### Behavioural checks

```json
{
  "id": "cis-scan",
  "kind": "behavioural",
  "binds_artifact": true,
  "command": ["scripts/scan.sh", "--target", "{{artifact}}"],
  "verdict": "count_min",
  "count_pattern": "([0-9]+) rules evaluated",
  "min_count": 200,
  "required_for": ["confirming->ready_to_merge"]
}
```

A behavioural check runs against the assembled artifact — a booted image, a provisioned host —
rather than against source. It must set `binds_artifact`, and the gate refuses to record its
result without a digest: a scan of "the host" means nothing if nobody can say which host, at what
state, later.

This is the difference between "the plan is valid" and "the machine is actually hardened", and it
is the only kind of check that can answer the second one.

## workers

A role declares **capabilities** and, optionally, **areas**.

```json
{
  "type": "security",
  "layer": "verification",
  "description": "Reviews items whose area says security is the risk.",
  "prompt": "security",
  "capabilities": ["validate"],
  "areas": ["security", "auth"]
}
```

| capability | what it may do |
|---|---|
| `research` | turn an intent into a written approach |
| `plan` | decompose an approach into proposed work items |
| `implement` | build one item |
| `test` | execute the tests and report what ran |
| `judge` | check an item against its acceptance criteria |
| `validate` | review adversarially, and check a plan against its intent. The only capability that can reject |
| `curate` | the hygiene pass over what landed |
| `arbitrate` | judge the change against the system rather than against the item |
| `operate` | perform an apply against a real resource |
| `improve` | record what a landed item taught, and raise fixes as work |
| `converse` | answer an operator in the console and draft actions. Advances no item on its own |

`low_cadence: true` marks a role expected to run rarely, so the never-run roll call reports it
without raising it as an alarm.

At least one role must declare `validate`, or nothing can ever finish. More generally, a
capability nobody declares strands every item that reaches it, so the config is refused rather
than loaded — a stranded item looks exactly like one nobody has got round to yet.

## routing

```json
"routing": { "api": "backend", "ui": "frontend", "security": "security" }
```

Maps an area to the role that owns it. Selection order:

1. the area's declared owner, if it holds the needed capability
2. any role holding that capability which lists the area
3. a **generalist** — a role declaring no areas at all
4. anyone with the capability

The generalist fallback is deliberate: dispatch must never strand a live item, so work in an
unclaimed area still reaches somebody sensible rather than whichever specialist sorts first.
Declare at least one generalist per capability you use.

Generation is stricter. A planner may only file work under an area you have **declared**,
because generation is the one place an agent decides what work exists.

An area with no owner is a configuration error, not a warning.

## loops

Concurrency is N scheduled lanes, not a thread pool.

```json
{
  "name": "judge",
  "enabled": true,
  "every_seconds": 180,
  "offset_seconds": 0,
  "capability": "judge",
  "max_per_tick": 2
}
```

| field | meaning |
|---|---|
| `name` | Stable — the tick record keys on it, so renaming restarts its liveness history |
| `enabled` | Editable from the dashboard; takes effect on the lane's next tick |
| `every_seconds` | Cadence. Minimum 15 |
| `offset_seconds` | Stagger. Two lanes firing on the same second contend for leases |
| `capability` / `areas` / `worker` | Scope. Empty means anything |
| `max_per_tick` | Dispatches per firing |

Give verification a shorter cadence than building, and put it first. Verification that competes
with building for capacity loses, and a fleet that builds faster than it checks accumulates
unverified work that looks exactly like progress.

Every firing writes a ledger tick, including the idle ones, so a lane that quietly stops shows as
`STALE` rather than looking like a lane with nothing to report.

One lane is not declared here and cannot be. The **merge** lane drains the merge queue every 120
seconds, and merging is the control plane's own step rather than an agent's — a lane with no
capability has no worker to route to. It still appears in `adlc schedule status` with a cadence
and a tick on every firing, because a merge queue that has quietly stopped draining and one with
nothing to drain look identical otherwise. It is the one lane `adlc schedule once -loop …` cannot
name; `adlc schedule run` drives it.

## blast

```json
"blast": {
  "auto_apply_max": "none",
  "named_approver_min": "host",
  "two_approvals_min": "region",
  "approval_ttl_minutes": 1440
}
```

| setting | meaning |
|---|---|
| `auto_apply_max` | The largest radius applied with no approval. Anything above stops and waits |
| `named_approver_min` | From here up, the approval must name a person |
| `two_approvals_min` | From here up, two distinct approvers |
| `approval_ttl_minutes` | How long an approval stays valid even if the plan has not moved |

Start at `none`, so nothing applies unattended until you raise it deliberately. An unrecognised
radius fails closed.

## budget

```json
"budget": {
  "price_micros_per_mtok": { "your-model": { "input": 3000000, "output": 15000000 } },
  "default_model": "your-model",
  "per_run_micros": 0,
  "per_day_micros": 50000000,
  "per_segment_micros": 0
}
```

Prices are in micros — millionths of a dollar — per million tokens, so $15 per million input
tokens is `15000000`. Integers throughout: money in floats accumulates error across thousands of
runs, and a spend cap that drifts is a cap nobody trusts. Cache reads bill at a tenth of input and
cache writes at 1.25x, which is the shape of the published rates rather than a per-model figure,
so `adlc config init` and the Config page both ask only for input and output.

`config init` writes the built-in table rather than an empty one, and every cost figure says which
of three places its rate came from:

| source | meaning |
|---|---|
| `configured` | your own `price_micros_per_mtok` entry priced it |
| `default` | the table that shipped in the binary priced it. Real money, unconfirmed rate |
| `unpriced` | neither did. Cost unknown, which is not the same as zero |

The distinction is the point. With no table at all every run reports `UNPRICED`, and a number
nobody has ever seen is a number nobody notices going wrong; with a table that quietly defaults
unknown models to zero, real spend renders as `$0.00`, which is the same failure the three-valued
verdicts exist to prevent. So the defaults get a figure onto the screen and the source is what
stops it being mistaken for one somebody confirmed. The Config and Overview pages mark a project
still running entirely on defaults.

The fallback is per model, not per table. Pricing two models and forgetting a third leaves your
two figures alone and prices the third from the defaults; a model in neither table stays
`UNPRICED` rather than being guessed at.

A cap of `0` means unlimited and reports as unlimited. "No budget configured" and "budget
exhausted" are opposite facts and must not collapse into one number.

## console

```json
"console": {
  "enabled": true,
  "authority": "act",
  "worker": "console",
  "timeout_seconds": 180,
  "history_turns": 20
}
```

The operator console is a way to drive this tool by talking to it: a panel on every dashboard
page, and a Console page holding the transcript. It is off by default, because it invokes an agent,
an agent costs money, and a surface that starts spending without being asked is one nobody trusts.

| setting | meaning |
|---|---|
| `enabled` | Whether the panel exists at all. Off means no panel and no tab |
| `authority` | `propose`, `act` or `full`. Empty means `propose` |
| `worker` | The declared worker a turn runs as. Must hold `converse`. Empty picks the first that does |
| `timeout_seconds` | Bounds one turn. Default 180 |
| `history_turns` | How much of the conversation is replayed into each turn. Default 20 |

`authority` is the only setting here worth deliberating over.

| level | what it executes |
|---|---|
| `propose` | nothing. Everything renders as a control a person presses |
| `act` | anything the control plane could do on its own, through the same transition authority as everything else. The three gates a person owns, it drafts |
| `full` | those three as well |

The three gates are signing off a deliverable, answering a blocking question, and deciding an
approval. They are not gated because they are the most dangerous — cancelling an item is arguably
worse. They are gated because each one *is* the human checkpoint, and an agent that clears its own
checkpoint has removed it. `full` is a real choice with a real cost: an approval an agent granted
itself is not an approval, and a fleet running that way has no human checkpoint left. The record
still says, on every one of them, that the console pressed it.

An unrecognised level is refused at load rather than treated as the safest one. Failing closed
would give a console that silently does less than the config says, which is worse than a config
that will not load.

`worker` must hold `converse` because a console turn is a run like every other and is attributed
to a role in the roster; a worker that cannot converse would appear in the record having done
something it is not declared to do.

`enabled` and `authority` are editable from the Config page. The rest is file-only.

## lease

```json
"lease": { "dir": ".adlc/leases", "ttl_minutes": 180, "strip_tokens": ["host", "fleet"] }
```

Leases are files with a TTL, outside version control. A run claims its item and every resource
the item touches before it starts.

Resources are hierarchical and `/`-separated: leasing `fleet:prod` refuses a claim on
`fleet:prod/host:web-01`, and the reverse. Two agents editing one file conflict at merge, which
is loud and cheap. Two agents changing one machine cause an outage.

`strip_tokens` are words removed before two claims are compared for descriptive overlap — add
your project's common vocabulary so it does not trigger advisories on every pair.

## dispatch

```json
"dispatch": {
  "command": ["claude", "-p", "@{{prompt}}"],
  "workdir_template": ".adlc/workspaces/{{run_id}}",
  "trunk": "main",
  "max_attempts": 3,
  "timeout_seconds": 3600,
  "isolation": "worktree"
}
```

`workdir_template` must contain `{{run_id}}`. A run wears one name — its id, its workspace and
its lease key are the same string — and a workspace named for anything else is invisible to every
guard that keys on the run.

`isolation` is `worktree` (a git worktree per run, the default), `copy` (for a tree that is not a
repository), or `none` (the shared checkout; correct only for a single lane).

`trunk` is the branch the merge queue lands on; it defaults to `main`. Each run's branch is
`adlc/<run_id>`, and the queue rebases that onto the trunk, re-runs the checks declared for
`merging->merged` on the rebased tree, and fast-forwards. Declare no checks for that edge and it
falls back to whatever gates work leaving the builder — a merge queue with no checks at all
fast-forwards anything.

`max_attempts` is how many times an item may be reworked before it escalates to a person instead
of looping.

## prompts

```json
"prompts": {
  "dir": "agents",
  "preamble_file": "agents/_preamble.md",
  "mandatory_clauses": ["You never write the ledger."]
}
```

`mandatory_clauses` must appear verbatim in every role prompt. `adlc prompt check` — which the
gate runs — fails if any is missing, so a run cannot delete a safety clause from its own
instructions.

## server

```json
"server": { "addr": "127.0.0.1:8099", "refresh_seconds": 15 }
```

Must be loopback. The dashboard writes to the ledger and has no authentication, and those two
facts stay welded together: `0.0.0.0:8099` and `:8099` are both refused by name.
