# internal/ledger

The append-only, hash-chained record, and the only thing in this module that writes it.

- `ledger.go` — event kinds, payload structs, `Append`, and `apply()`: the **one** function that
  writes a projection table.
- `schema.go` — the SQL, the append-only triggers, the projection table list, the migrations.
- `verify.go` — the two-tier verification and the four verdicts.
- `rebuild.go` — re-derives every projection from the chain.
- `blob.go` — content-addressed prompt and envelope bytes, which is what makes a run reproducible
  rather than merely described.
- `read.go`, `console.go` — read paths for the reporting surfaces.
- `build.go` — which build wrote a row, and the rule that an absent revision is `unknown` rather
  than a value anything will treat as a match.

## What goes wrong here

**Confusing the two halves.** The chain (`adlc_event`) plus the head anchor is the record.
Everything in `projectionTables` is derived and disposable — `adlc ledger rebuild` restores it in a
second. So a projection may be re-derived freely; the chain may not be touched at all. The
append-only triggers live in SQL rather than in a code path because the code path is the thing
under audit.

**Changing `apply()`.** It runs on two paths — once per `Append`, and over an empty schema during
replay — and `Verify` diffs the two. Change how a row is derived and every existing ledger reports
`STALE PROJECTION` until it is rebuilt. That is correct behaviour, not a bug, but it means an
`apply()` change is a release note and a `rebuild` instruction, never a silent edit.

**Adding a column to `schemaSQL` alone.** `CREATE TABLE IF NOT EXISTS` does nothing to a table that
already exists, so the column never reaches a ledger created by an earlier build. Add the matching
additive `ALTER TABLE` to `migrations`. Migrations here never drop, rename or retype: the chain is
the record and a migration that can lose data eventually does.

**Making the verifier accuse when it is merely out of date.** Broken hashes are `TAMPERED`. An
event kind, item state or schema version this build does not recognise is `UNKNOWN`, and the
recovery is to upgrade the binary. A tamper detector that cries tamper at its own obsolescence gets
ignored, and an ignored detector is not a control. If you add an event kind, add it to `KnownKinds`
in the same change — and expect older binaries to read it as `UNKNOWN`, which is the point.

**Adding a field to the hash preimage.** `HashEvent` covers a fixed list, and `adlc_event.build_rev`
is deliberately not on it. An older binary recomputing a hash cannot know about a column it was
compiled before, so folding a new field in makes every row this build writes read as `TAMPERED` to
that binary — the one thing this verifier must never say when nothing is wrong. A new column on the
chain is protected by the append-only triggers, which is the same protection every other column has.

**Assuming a trigger that exists still fires.** `guardsFire` attempts the writes the guards exist to
refuse, inside a rolled-back transaction. Presence in `sqlite_master` is not evidence.

**Comparing projections by column order.** `fingerprint` reduces each row to a sorted set of
`name=value` pairs. A migrated table carries its new columns at the end and a fresh one carries them
in the middle; walking columns in returned order made every migrated ledger report `TAMPERED`,
which is the worst thing this verifier can say when nothing is wrong.

## Tests

`ledger_test.go` pins each verdict against a real failure: an edited payload reads `TAMPERED`, a
truncated chain is caught by the anchor, a newer build's event kind reads `UNKNOWN` and not
`TAMPERED`, a migrated table is not tampering, a schema version newer than the build reads
`UNKNOWN`. It also pins that the append-only guards actually refuse, that projections rebuild from
the chain, that a worker with no runs is still counted, and that a started run with no end is
`UNKNOWN` rather than a pass.

`build_test.go` pins the recorded build: a stamped build's revision reaches the chain and comes back
off it, a build with no VCS stamp records `unknown` and never matches anything, and a hand-built v1
ledger — the pre-`build_rev` table shape — opens, migrates, reads as `UNKNOWN` and verifies `INTACT`
with all its v1 hashes still correct. The schema stamp moves forward on migration and is never
lowered.

That last property is pinned by a **hard-coded digest**, not by `HashEvent`. Every other hash in
these tests is computed by the function under test, so a change to the preimage moves the
expectation with it and nothing goes red — including the one change this package forbids. The
constant in `TestALedgerFromBeforeRevisionsReadsUnknownNotTampered` is the only thing that fails
when the preimage moves. If you have to change it, you are also changing every chain any earlier
binary ever wrote from readable to `TAMPERED`.
