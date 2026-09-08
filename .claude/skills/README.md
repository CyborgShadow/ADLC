# adlc skills

Four Claude Code skills that drive the `adlc` CLI. Each one conducts an interview; the
tool does the deterministic part. None of them hand-assembles JSON that `adlc` could
generate and validate itself, and none of them proceeds past a command the CLI refused.

| Skill | What it does |
|---|---|
| `adlc-setup` | Interviews about the project, its existing checks, its blast radius and how much authority the console gets, then runs `adlc config init`, `adlc config check` and `adlc init`. Tells you which prompt files still need to exist. |
| `adlc-deliverable` | Turns a fuzzy goal into a brief a researcher can work from, plus a rationale and a target for items in flight, then runs `adlc segment create`. Explains that the deliverable starts as a theory and needs sign-off before anything is spent. |
| `adlc-role` | Interviews about a gap in the roster, then writes the worker declaration, the routing entry and the prompt file together, and validates with `adlc config check` and `adlc prompt check`. |
| `adlc-triage` | Read-only. Reads the live state and says what is blocked and why, what is waiting on you, and which lanes have quietly stopped firing. Changes nothing. |

## How they load

They live in `.claude/skills/<name>/SKILL.md`. Claude Code discovers skills in
`.claude/skills/` under the project root automatically — there is nothing to register and
no setting to turn on. Ask for what you want ("set up adlc here", "why is nothing
happening") and the matching skill is picked up from its `description`.

## Copying them into your own project

The ADLC lives alongside the product it builds, so these skills need to travel with it.
Copy the whole directory into the project where `adlc.json` lives:

```bash
# from the root of your own project
mkdir -p .claude/skills
cp -r /path/to/ADLC/.claude/skills/adlc-* .claude/skills/
```

PowerShell:

```powershell
New-Item -ItemType Directory -Force .claude\skills
Copy-Item -Recurse "C:\path\to\ADLC\.claude\skills\adlc-*" .claude\skills\
```

Nothing in them is specific to this repository — they read your repo to make their
questions sharper, and everything else comes from your `adlc.json` and your ledger. They
assume `adlc` is on your `PATH`:

```bash
go install github.com/CyborgShadow/ADLC/cmd/adlc@latest
```

To make them available in every project rather than one, copy them to `~/.claude/skills/`
instead. Project skills win over personal ones when both define the same name.

## Keeping them honest

The skills quote real flags, real exit codes and real refusal messages. When the CLI
changes, they go stale silently — a skill that names a flag which no longer exists produces
a confident failure. If you change `cmd/adlc`, check these four against
`adlc <command> -h` before trusting them again.
