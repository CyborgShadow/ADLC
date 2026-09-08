# internal/prompt

The versioned prompt library. The file in `agents/` **is** the definition: the dispatcher reads it
at dispatch time and carries no prompt of its own, so changing a worker is an ordinary reviewable
edit and every past run can still say exactly which bytes it was given.

- `prompt.go` — `Load`, `Assemble`, `CheckClauses`, the digest.
- `write.go` — writing a role prompt or the preamble back from the dashboard.
- `survey.go` — the non-failing inventory `adlc prompt list` reports from.

## What goes wrong here

**Duplicating fleet policy into a role.** The shared preamble is stored once and joined above the
role prompt at assembly, separated by a visible `---`. Copy policy into role files and it drifts
between them immediately, and a fleet-wide change becomes N edits with no way to tell which roles
got it.

**Losing a mandatory clause.** The clauses declared in `adlc.json` must appear verbatim, and
`CheckClauses` runs in the gate rather than at dispatch: checking at dispatch refuses the run that
noticed, while checking in the gate refuses the commit that removed the clause, which is where the
defect is. Note that the preamble is concatenated in front of every prompt for this test, so a
clause carried by the preamble satisfies it for all of them — removing it from the preamble fails
every prompt at once, which is the intended blast radius.

**Making `Survey` strict.** `Load` refuses a library it cannot assemble from, including one with no
preamble — a dispatch with no fleet policy is not a dispatch. That strictness is exactly why
`Survey` exists and must never fail: on a project that has not written its prompts yet, the command
that is supposed to enumerate the gap has to answer rather than error on one file.

**Hashing bytes other than the ones stored.** `SHA` doubles as the content address of the retained
prompt. `Assembled.Bytes` normalises line endings the same way the digest does; store raw text
under a hash of normalised text and retrieval silently fails, and the run then reports that its own
prompt was never kept.

**Writing without replacing the in-memory copy.** The dispatcher assembles from memory. A write
that only touched disk leaves every run in this process using the old text while the dashboard shows
the new one. The write is also atomic, because a prompt truncated halfway dispatches an agent with
half its instructions and no error anywhere.

**Reading the map without the lock.** One process holds one library and both the scheduler and the
dashboard are given it — `adlc schedule run --serve` runs them side by side. A Go map read during a
write is not a stale read, it is a crash.

## Tests

`prompt_test.go` pins front matter parsing, two prompts refusing to share an id, the preamble being
assembled above every role, variable substitution, the digest addressing exactly the bytes that are
stored, assembly being stable, a missing clause being reported, and a BOM not changing a prompt's
identity. `survey_test.go` pins that a project with no prompts still gets an answer and that the
survey separates what is there from what is named.
