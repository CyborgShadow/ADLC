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

**Writing without replacing the in-memory copy.** A write that only touched disk leaves the
library's own copy stale until the next read refreshes it, and the dashboard's view of what it just
wrote is then whatever it read last. The write is also atomic, because a prompt truncated halfway
dispatches an agent with half its instructions and no error anywhere.

**Serving the snapshot `Load` took.** One process holds one library for hours — `adlc schedule run
--serve` builds it at startup and dispatches from it all day. So `Get`, `Assemble`, `CheckClauses`
and the preamble accessors re-read their files first: a prompt change merged onto the trunk at noon
would otherwise reach no run until somebody restarted the scheduler, reviewed and committed and
live everywhere except in the fleet that is actually reading it. A file that cannot be read now
keeps the copy already loaded, because refusing to dispatch during a checkout trades a slightly
stale prompt for no prompt at all. A file that appears in the directory after `Load` is not picked
up: nothing dispatches through it until the config that names it is reloaded, which is a restart
either way.

**Taking a fresh read that succeeded but is half a file.** Re-reading at dispatch means the library
trusts a writer that gives it none of the guarantees `writeFile` does — `git merge` rewrites
`agents/` in the tree a serving library is reading, and truncates a file before it fills it. A read
that returns nothing is not an error to fall through on: an empty role body dispatches an agent
with the preamble and no job, which is what `SetPrompt` refuses in those words, and an empty
preamble makes `Assemble` drop fleet policy and the seam with it, so the run carries none of the
mandatory clauses and nothing reports it. So a fresh read is only taken when it is a whole file:
a blank body, or front matter the loaded copy had and the fresh read has lost — an opened fence not
yet closed — keeps the copy already loaded. **Both guards apply on both paths.** The preamble is
never parsed, so a fragment reaching it is served as the entire fleet policy rather than as one
role's body; guarding only the role file leaves the wider failure open on the narrower path. Note
that the fence test keys on what the loaded copy had, not on what a file ought to look like: key it
to the fresh read alone and a preamble carrying no front matter is frozen at its startup copy for
the life of the process. A truncation landing after the front matter still reads as a short file
and cannot be told from one; that window is narrowed, not closed, and the gate's clause check is
what catches a committed tree.

**Reading the map without the lock.** One process holds one library and both the scheduler and the
dashboard are given it — `adlc schedule run --serve` runs them side by side. A Go map read during a
write is not a stale read, it is a crash.

## Tests

`prompt_test.go` pins front matter parsing, two prompts refusing to share an id, the preamble being
assembled above every role, variable substitution, the digest addressing exactly the bytes that are
stored, assembly being stable, a missing clause being reported, and a BOM not changing a prompt's
identity. It also pins the refresh from three sides: a role prompt and the preamble edited on disk
by something other than `SetPrompt` reaching the next assembly, an unchanged file still assembling
byte for byte the same, and a vanished file keeping the copy already loaded rather than stopping
the dispatch. Three more pin the mid-write guard from every side: a role prompt and a preamble
each caught truncated and each caught half-fenced keep the loaded copy, clauses and seam intact; a
whole merged file still reaches the next assembly, so the guard cannot pass by refusing everything;
and a preamble that never had front matter still refreshes, so it cannot pass by freezing one.
`survey_test.go` pins that a project with no prompts still gets an answer and that
the survey separates what is there from what is named.
