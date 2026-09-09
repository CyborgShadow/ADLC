# internal/gate

Runs the declared checks and reports what it observed. `gate.go` executes and judges; `tree.go`
answers what commit a workspace is on and whether the declared source roots are dirty.

This package exists because a transition authority that reads its gate result out of the worker's
own envelope is checking that the worker *claimed* a command ran, never that one did.

## What goes wrong here

**Treating the envelope as evidence.** The control plane runs the commands itself, in the tree the
work is in. The worker's account survives only as a claim, and `CompareClaims` reports a
disagreement as a `discrepancy` and a silence about a failing check as an `omission`. Silence about
a check that passed is not dishonesty; silence about one that failed is the thing the matcher is
for. A claim explicitly marked `not_run` is ignored entirely — that is what makes honesty free.

**Reading the exit code of a command that does not use it.** Some commands report their verdict in
their **output** and exit 0 either way (`gofmt -l` is the canonical one). `CompareClaims` re-judges
the envelope's own output through the *same* `config.VerdictRule` the observation was judged by;
judging a claim in a different channel from the observation is how an honest envelope came to be
refused while a doctored one was admitted.

**Letting zero discovered units read as green.** Zero tests, zero hosts, zero rules evaluated is a
failure. A filter that silently matches nothing converts "I ran nothing" into "everything passed".
That is what `go_test_json` and `count_min` are for.

**Collapsing three values into two.** A missing tool or directory is `UNKNOWN` and satisfies
nothing. A hang is `RED` — a check nobody can wait for is a check that gets skipped, and a skipped
check is an absent one. A behavioural check with no artifact digest is `UNKNOWN`, because evidence
about a running artifact means nothing without naming what it ran against. A check the run's own
budget expired before is `UNKNOWN` and not the `RED` a hang earns: the check's context inherits
the run's expiry, so reporting "timed out after 15m0s" there blames a check that was never given
a second of those fifteen minutes, and sends whoever reads it hunting a hang that never happened.

**Walking the tree unscoped.** `TreeState` refuses to guess: with no `source_roots` it returns an
error rather than reporting clean. An unscoped walk counts vendored dependencies and build output
as uncommitted work and refuses runs over a tree nobody edited.

**Passing a command through a shell.** Commands are argv. A shell would make the recorded command
and the executed command two different strings.

## Tests

`gate_test.go` pins each judging rule against the defect it closes: the output is the verdict when
the rule says so, a run that discovered nothing is a failure, a counting check refuses a vacuous
scan, an absent tool is `UNKNOWN` not green, a behavioural check with no artifact is `UNKNOWN`, a
fabricated exit code is a discrepancy, an honest `not_run` never is, a claim is judged in the
channel the observation was, silence about a failing check is an omission but silence about a
passing one is not, a gate with no declared checks is not green, and a run that ran out of its own
budget says so rather than blaming a check it never started — while a check that really does
outlive its own budget is still `RED`.
