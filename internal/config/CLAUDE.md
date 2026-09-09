# internal/config

The declared surface that makes this ADLC generic: checks, workers, routing, lanes, blast policy,
budget, leases, prompts, the dashboard and the console. `adlc.json` in the repository root is this
project's own instance of it.

- `config.go` — the types, `Load` (strict) and `validate`.
- `setters.go` — the settings the dashboard may change, and the reasoning for the line.
- `describe.go` — the plain-language explanation of each declared value.
- `pricing.go` — the built-in price table, an editable default rather than a fact.
- `console.go` — the console authority levels.
- `scaffold.go` — what `adlc config init` builds from a handful of answers.

## What goes wrong here

**Two definitions of one verdict.** `VerdictRule` names the channel a check's verdict is read from.
The gate and the claim matcher must both read a result through the same rule; when they did not,
an honest envelope was refused while a doctored one was admitted. Add a rule once, here, and let
both callers ask.

**Making a check editable from a form.** Checks, workers and routing are file-only, and not for
want of effort. A check is a command line: a form that writes arbitrary argv into something the
control plane will execute is a remote shell wearing a hat, and "it is only bound to loopback" is
reasoning that ages badly. Workers and routing are structure and belong in a reviewed commit next
to the prompt files they name. `setters.go` is the whole list of what may move at runtime — policy
calls somebody revises during an incident.

**A half-applied policy.** `SetBlast` validates every value before it writes any. A safety policy
applied halfway is worse than either the old one or the new one, because nobody can say which rules
were in force.

**Loosening `Load`.** Unknown fields are refused rather than ignored — a typo in a safety setting
that loads cleanly is a setting nobody applied. A `_comment` field exists so a config can carry its
own explanation without that strictness pushing the note somewhere nobody looks. BOMs are stripped:
anything an agent writes or a Windows editor touches may carry one.

**A validation that lets the fleet stall.** `validate` refuses the configurations that produce
silent stalls: an area routed to an undeclared worker, a lane draining a capability no worker
holds, no worker holding `validate`, a behavioural check with no `binds_artifact`, a config with no
`source_roots` (an unscoped tree-walking guard refuses work on files nobody edited). Keep new rules
in that shape — refuse the thing that would look like an empty backlog.

**Reaching for zero as a sentinel.** A cap of zero means unlimited and reports as unlimited. "No
budget configured" and "budget exhausted" are opposite facts. A model with no price entry costs
`UNKNOWN`, never zero.

## Tests

`config_test.go` pins the loader's strictness (an unknown field is refused, a comment is not one, a
BOM changes nothing), the stall-preventing validations, `OwnerFor` preferring the area then the
generalist, `KnownArea` being stricter than `OwnerFor`, radius failing closed, checks being selected
by edge, the verdict channel being explicit, a `count_pattern` anchored to its line finding that
line while the unanchored ones count exactly what they counted before, and `SetLoop` refusing a
thrashing cadence.
`cmd/adlc/commands_config_test.go` covers the CLI that writes this file.
