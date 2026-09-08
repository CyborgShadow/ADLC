---
name: adlc-role
description: Add or change a role in an adlc roster — interview about the gap, then write the worker declaration, the routing entry and the prompt file together, and validate with `adlc config check` and `adlc prompt check`. Use when someone wants a new agent role or specialist (security, performance, docs, infrastructure), wants work in some area routed somewhere specific, or reports that items in an area are not being picked up.
---

# Add a role to the roster

A role in adlc is three things that must exist together:

1. a **worker declaration** in `workers[]` in `adlc.json` — its type, layer, capabilities,
   areas and the prompt it runs;
2. a **routing entry** in `routing` — `area -> worker type`;
3. a **prompt file** in the agents directory.

Any one of them alone is broken. `adlc item create -area X` refuses outright when `X` is
not a key in `routing`, in those words: *an item filed under an unowned area is
unreachable, and an unreachable item looks exactly like one nobody has got round to.*

There is no `adlc role add` command. This is the one place a skill edits `adlc.json`
directly, and it is only acceptable because `adlc config check` re-validates the whole file
afterwards with the same loader a running fleet uses. **Make the smallest possible edit,
then check. If the check refuses, fix the file until it passes — never proceed past it.**

## Read the current roster first

```bash
adlc config check     # what is declared now: roles, areas routed, lanes
adlc config show      # the effective config, defaults filled in
adlc prompt list      # every prompt file that exists, with its id
```

`config show` prints the *effective* config, not the file — the gap between the two is
where a surprising afternoon comes from. Read `workers`, `routing` and `loops` from it.

Then look at the actual problem before you propose a role. If items are not moving, run
`adlc dispatch plan` and `adlc report fleet` — the gap may be a missing lane or an
unanswered question rather than a missing role, and a new role fixes neither.

## Refuse the half-answer

Two things this skill will not do:

- **A role without an area, when the role is a specialist.** A specialist that declares no
  areas is a generalist: routing takes the area's declared owner first, and a specialist
  with no area is only ever reached as a fallback. If the user wants security review to
  actually happen on security items, `security` must be an area and it must route to that
  role. Say so and ask which area.
- **An area without a role.** Adding `routing: {"payments": "..."} ` pointing at a worker
  that is not declared is refused by the loader. Adding an area no worker declares means
  every item filed there falls through to a generalist.

If the user asks for one half, ask for the other before writing anything.

## The interview

### Round 1 — what is actually missing

1. What work is not being done well, or not being picked up at all? Name a concrete item
   or a concrete kind of item.
2. Should this role **do** work, or **check** it? That decides the capability, and the
   split matters more than any other choice here: a role that writes work and also judges
   it will always find it acceptable.
3. What should it be called, and what area tag do items in its domain carry?

### Round 2 — the specifics

4. Which capability? One per role unless there is a reason:

   | Capability | The stage it owns |
   |---|---|
   | `research` | intent → written approach |
   | `plan` | approach → work items with checkable criteria |
   | `implement` | builds one item |
   | `test` | executes the tests and reports what ran |
   | `judge` | checks an item against its criteria by executing things |
   | `validate` | adversarial review, and plan-against-intent. **The only capability that may reject** |
   | `curate` | hygiene |
   | `arbitrate` | judges a change against the whole system |
   | `operate` | anything touching a real machine, after an approval |
   | `improve` | records what a landed item taught |
   | `converse` | the operator console. Advances no work item |

5. Which layer — `planning`, `worker`, `verification`, `stewardship`, `platform`? This is
   descriptive; it groups the role in reports.
6. Does it run rarely? If so mark `low_cadence: true`, so its silence is reported in the
   never-run roll call without being raised as an alarm.
7. Is there a lane that drains this capability? Check `loops` in `config show`. A role with
   a capability no enabled lane drains never dispatches — `config check` warns about
   exactly this.

## Write the three pieces

### 1. The worker declaration

Append to `workers[]`:

```json
{
  "type": "security",
  "layer": "verification",
  "description": "Adversarial security review of items routed to the security area.",
  "prompt": "security",
  "areas": ["security"],
  "capabilities": ["validate"]
}
```

`prompt` is the prompt **id**, which is the filename without `.md`.

### 2. The routing entry

Add to `routing`, in the same edit:

```json
"security": "security"
```

### 3. The prompt file

Write `<agents dir>/<prompt id>.md`. Front matter is required:

```markdown
---
id: security
version: v1
---

# Worker: security

You review items routed to the security area, adversarially...
```

Placeholders the dispatcher substitutes — every one is always present, so an unfilled
placeholder never reaches an agent as literal template text:

`{{run_id}}` `{{worker_type}}` `{{workdir}}` `{{envelope}}` `{{areas}}`
`{{work_item_id}}` `{{segment_id}}` `{{title}}` `{{state}}` `{{blast_radius}}`
`{{resources}}` `{{file_scope}}` `{{criteria}}` `{{brief}}` `{{needed}}`
`{{existing_items}}` `{{rationale}}`

Model it on an existing prompt of the same capability in the agents directory — read one
first. Do not restate the shared preamble; it is prepended to every prompt automatically.
Do say, concretely: what this role is given, what its job is, what is explicitly *not* its
job, and what it must raise as a question instead of deciding.

## Validate — and stop if it refuses

```bash
adlc config check
adlc prompt check
```

`config check` non-zero means the config no longer loads and nothing will start against it.
Show the exact message and fix the file. Common ones:

- `routing: area "x" is owned by "y", which is not a declared worker` — the routing entry
  and the worker declaration disagree
- `worker s: unknown capability "x"` — see the table above
- `duplicate worker type "x"`

Then read the warning block, if any: *"This config is valid, and these will still leave
work sitting"*. A capability with no role strands every item that reaches it; a role whose
capability has no enabled lane never dispatches. Report these; do not skip them because
the exit code was 0.

`prompt check` exits 4 if a prompt lost a mandatory safety clause. Be honest about what it
proves: the clauses are matched against the preamble **and** the body concatenated, so a
new prompt file passes on the preamble alone. It is evidence the safety clauses survive,
not evidence the prompt is any good.

### The gap you have to close by hand

Neither `config check` nor `prompt check` notices that a worker's `prompt` names a file
that does not exist. Verify it yourself:

```bash
adlc prompt show security      # exits 1 with `no prompt "security" in agents` if missing
```

## Then register it

A newly declared worker is not in the ledger's registry until:

```bash
adlc init
```

`init` is safe to re-run — it registers only workers it has not seen. This is what makes
"this role has never run" detectable, because a worker that never runs contributes no rows
to read.

## Report back

- The role: its type, capability, layer, areas, and the prompt file you wrote.
- The routing entry, and therefore which items will now reach it.
- Which lane dispatches it and how often — or, if none does, say so plainly, because the
  role will sit there.
- The output of `config check` and `prompt check`, including warnings.
- The next step: `adlc init` to register it, then `adlc report fleet` after it has had a
  chance to run — it will appear under NEVER RUN until it does.
