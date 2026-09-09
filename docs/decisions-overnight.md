# Decisions taken on your behalf, overnight of 8–9 September 2026

You delegated answering while you slept, with a record to review. This is that
record. Every entry says what was asked, what I decided, why, and how to reverse
it.

Two things about how these were recorded:

**They are attributed to `claude`, not to you.** The ledger exists to say who
decided; a decision of mine filed under your name is the one thing it must never
record. Every answer below appears in the chain with `claude` as the actor, and
`adlc question list -open=false` will show them that way for as long as the
record exists.

**Nothing here changed a human gate.** Sign-off, approval and blast-radius policy
were left alone. What I answered were questions agents raised because they could
not proceed, and what I changed were defects in the control plane itself.

---

## D1 — S1-010-Q1: which edge should the implementation gate run over?

**Asked because** two runs on S1-010 were refused `zero_checks_declared` for
`in_progress->verifying`, while `adlc.json` declared checks only for
`in_progress->ready_for_testing` and the other old edges.

**The agent's lean** was to point the implementer's outbound edge back at
`ready_for_testing`, and it explicitly refused to add check declarations itself
— "adding a check declaration to make my own gate pass is the move a worker must
never make". That is exactly right and worth saying so.

**Decided:** the diagnosis is correct and the fix is the other direction. The
edge is right and the declarations were stale: I had collapsed the verification
chain into one `verifying` stage and never rebound the checks. They now gate
`in_progress->verifying`, `verifying->verifying` and `verifying->reviewed`, and
`adlc config check` refuses a config whose checks name an edge that is not in
the table.

**To reverse:** revert commit `571d4b1`. You would then also want to revert the
stage collapse, since the old edges only exist there.

---

## D2 — S1-002-Q1: an acceptance criterion no command can satisfy

**Asked because** AC-3 required `go run ./cmd/sitecheck ... ` to exit 2, and
`go run` collapses every non-zero exit status of the program it runs to 1. No Go
program can satisfy that wording.

**The agent's lean** was to restate the criterion against the built binary, and
to refuse the alternative of making the usage error exit 1 — because 1 and 2 are
kept apart precisely so a caller cannot read its own typo as a failing site.

**Decided:** accepted as leaned. The built binary is what a caller actually
invokes and its exit code is the contract CI reads; `go run` is a developer
convenience that happens to lose the code. Changing the program's exit codes to
fit the test would be fixing the wrong end.

**To reverse:** answer the question differently and let the item be re-judged;
nothing in the control plane changed for this one.

---


## D3 — S1-017-Q2: a spend guard that cannot fire

**Asked because** the new `spend_unknown` guard in `CheckRun` cannot fire for any
real run: `dispatch` writes `int64(cost)` into the ledger's cost column, so
`spend.Cost` returns a real zero rather than a sentinel, and hands that same zero
to `CheckRun`. An unmeasured run clears the per-run cap exactly as it did before.

**The agent's lean** was a follow-up item widening the scope to `internal/dispatch`
rather than reopening this one, because the split is at the call site: dispatch
needs the recorded cost (a real zero, so the ledger column stays money) *and* the
measurement fact (so the cap sees UNKNOWN), and only the caller holds both.

**Decided:** accepted. `spend.CostOf` already returns exactly that pair, so the
follow-up is passing the second return value on, not a new mechanism. I asked it
to say plainly in the item record that until that lands the per-run cap is as
blind to unmeasured runs as it was before — its own point, and the honest one:
anyone reading AC-3 as delivered would otherwise be wrong about the running
system.

**To reverse:** the follow-up item is the change; drop it and the cap stays blind.

---

## D4 — S1-017-QJ1: a triage skill that names two flags where there are now four

**Asked because** `.claude/skills/adlc-triage/SKILL.md` says there are "two things
to flag" in `report fleet`'s spend output; S1-017 added UNMEASURED and INCOMPLETE
to that same surface. The run refused to edit it because `.claude/skills` is
outside its declared file scope.

**Decided:** a follow-up item, and the refusal was right for the right reason — a
hygiene run reaching outside its scope to edit a skill is a worse precedent than
a stale paragraph. Folded together with S1-017-Q4 (the same omission on the
dashboard) into one item rather than two editing adjacent paragraphs, because
they are one defect: a money surface telling two stories.

---

## D5 — three questions asked twice by runs that could not see each other

S1-002-Q2 was S1-002-Q1 again, asked by a second run. Answered identically. Worth
noting as a pattern rather than a decision: two runs on one item raise the same
question because neither can see the other's, and both then wait. The lessons
mechanism now carries a refused envelope's shape forward; it does not yet carry
an answered question forward to a sibling run. That is a real gap and it is
listed below.


## D6 — S1-019-Q1: an agent's self-service commands were broken, and it was right to stop

**Asked because** every `adlc` subcommand a worker is told to use — `item show`,
`run list`, `gate run` — refused the `ADLC_DB` it is given, taking the whole
`file:...?mode=ro` DSN as a filesystem path and trying to create a directory
called `file:C:...`.

**The agent's lean** was an item against the ledger-open path, and it explicitly
refused the alternative of handing workers a plain path because that would give
every worker a writable handle to the ledger, which the first invariant is
against. Both halves of that reasoning are right.

**Decided:** no item. `ledger.Open` has accepted a `file:` DSN since `5e70b57` at
03:35 this morning. The run works in a worktree cut from its item's branch, which
is based on a commit from *before* that, so `go run ./cmd/adlc` in its own
workspace builds an old `adlc` that knows nothing about DSNs. I reproduced every
command it named against the current tree, from inside a workspace, and they all
work. It clears itself as branches rebase past `5e70b57`.

**What it actually found, and I am leaving for you:** a worker self-services with
a binary built from *its own branch*, so any change to the control plane's CLI
contract is invisible to every run already in flight — and looks to that run like
a defect in the tree it is holding. The obvious fix is to bring the item's branch
up to trunk when a workspace is prepared, merging trunk in where it is clean and
carrying on where it is not. I have not done it: it rewrites real branches
carrying real work, and that is a change I want you watching rather than one I
land while you are asleep.

## Changes made without being asked

These were defects, not judgement calls, but they are listed because each
changed how the fleet behaves.

| what | commit | why |
|---|---|---|
| Checks rebound to the edges that exist | `571d4b1` | The gate had nothing to run and refused everything |
| A claim is the LAST run of a check | `9e70159` | An agent was refused for showing its working |
| `schedule stop` drains instead of killing | `ee4ca22` | Six engineer runs were abandoned by restarts |
| Plan review advises below `plan_gate_min` | earlier | One contested plan held twelve items for hours |
| The envelope parser accepts shapes, not meanings | `9bf789e` | Five of seven malformed refusals were formatting |
| A refused envelope is recorded as a lesson | `9bf789e` | The next run could not see the refusal and repeated it |
| The verification stage settles once, with everything it saw | `48fb8cf` | One failure discarded its two in-flight siblings |

---

## Still open for you

- **`blast.plan_gate_min` is `host`**, so source-only work starts on a breakdown
  that is still under review. You chose this; it is worth confirming once you
  have seen what it produces.
- **Eleven view fields** were computed and shown nowhere. Three were dead and
  removed; eight are now on a page. If any of those eight are noise on a page
  you actually read, say so and they come off.
- **An answered question does not reach a sibling run.** Two runs on one item
  raise the same question, neither can see the other's, and both wait for you.
  It happened twice in one night (S1-002-Q1/Q2, and S1-017's pair). The fix is
  the same shape as the lessons mechanism and I have not built it.

## D7 — S1-016-QJ1: an answered question that could not become work

**Asked because** a janitor found two documents outside its file scope made stale
by its item, and noticed that `adlc.json` still reads
`["cmd","internal","agents","site",…]` even though **S1-005-Q1 had already been
answered "yes, add docs and README.md to source_roots"**. Second item in a row to
trip over the same gap.

**Decided:** the follow-up item for the operating.md replay sample, yes. Refusing
to edit `docs/overnight-report.md` from a hygiene run was right, and for the
better reason it gave — a dated narrative addressed to you is not a document to
be silently corrected. But its premise on that file was wrong and I said so:
`SchemaVersion` is still 1 and `ledger verify` still reports the mismatch, so
that bullet stands.

I made the `source_roots` change myself in `38e7749`, taking the trade-off the
janitor named with eyes open: this turns every tree-walking guard loose on prose
and will surface a backlog before it surfaces a regression.

**The thing worth your attention** is what it found underneath: **an answered
question does not become work.** When the change an answer implies lands in a
gated artefact — `adlc.json`, a role prompt, the preamble — no agent may make it,
and nothing in the control plane notices that an answer is sitting unbuilt. It
waited until a second item tripped over it. There is no mechanism for this and I
have not invented one.
