package ledger

import (
	"database/sql"
	"errors"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// TestAnAppendedEventNamesTheBuildThatWroteIt is the clean case: a build that
// knows its revision stamps it on every row.
func TestAnAppendedEventNamesTheBuildThatWroteIt(t *testing.T) {
	l := open(t)
	l.SetRevision("2f6a1c0d4e8b9a7c5d3f1e0b2a4c6d8e0f1a2b3c")
	ev, err := l.Append("pm", KindNoteRecorded, "", NoteRecorded{Text: "a note"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Revision() != "2f6a1c0d4e8b9a7c5d3f1e0b2a4c6d8e0f1a2b3c" {
		t.Fatalf("the appended event should carry the build's revision, got %q", ev.Revision())
	}
	// And it is on the chain, not just on the value the caller was handed.
	read, err := l.Events(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 1 || read[0].Revision() != ev.Revision() {
		t.Fatalf("the revision did not survive the round trip: %+v", read)
	}
	if got, err := l.RevisionAt(1); err != nil || got != ev.Revision() {
		t.Fatalf("RevisionAt(1) = %q, %v; want %q", got, err, ev.Revision())
	}
	if rep, err := l.Verify(); err != nil || rep.Verdict != VerdictIntact {
		t.Fatalf("stamping a revision must not disturb the chain: %v %v", rep.Verdict, err)
	}
}

// TestABuildWithNoVCSStampRecordsUnknown is the firing case for the rule that
// absence is never a known value. `go run`, a tarball build and a test binary
// all land here.
func TestABuildWithNoVCSStampRecordsUnknown(t *testing.T) {
	if got := revisionOf(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "GOARCH", Value: "amd64"},
	}}, true); got != RevisionUnknown {
		t.Errorf("build info with no vcs.revision should read %q, got %q", RevisionUnknown, got)
	}
	if got := revisionOf(nil, false); got != RevisionUnknown {
		t.Errorf("absent build info should read %q, got %q", RevisionUnknown, got)
	}
	// The clean case, so this does not pass vacuously the day revisionOf starts
	// answering "unknown" to everything.
	if got := revisionOf(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc123abc123abc123"},
	}}, true); got != "abc123abc123abc123" {
		t.Errorf("a stamped build should read its own revision, got %q", got)
	}
	// A modified tree names the commit and says the build is not identified by
	// it, because two binaries with the same sha and local edits are two
	// different programs.
	if got := revisionOf(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "true"},
	}}, true); got != "abc123"+dirtySuffix {
		t.Errorf("a dirty build should be marked, got %q", got)
	}

	l := open(t)
	l.SetRevision("")
	ev, err := l.Append("pm", KindNoteRecorded, "", NoteRecorded{Text: "unstamped"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Revision() != RevisionUnknown {
		t.Fatalf("an unstamped build must record %q, got %q", RevisionUnknown, ev.Revision())
	}
}

// TestAnUnstampedBuildNeverReadsAsAgreeingWithThisOne pins the rule every
// surface depends on: "I could not tell" is not a match.
func TestAnUnstampedBuildNeverReadsAsAgreeingWithThisOne(t *testing.T) {
	for _, c := range []struct {
		name, a, b string
		want       bool
	}{
		{"two unknowns are not the same build", RevisionUnknown, RevisionUnknown, false},
		{"an empty column is not the same build", "", "", false},
		{"unknown against a real build", RevisionUnknown, "abc123", false},
		{"two dirty trees at one commit are not one build", "abc123+dirty", "abc123+dirty", false},
		{"different builds", "abc123", "def456", false},
		{"the same build", "abc123", "abc123", true},
	} {
		if got := SameBuild(c.a, c.b); got != c.want {
			t.Errorf("%s: SameBuild(%q,%q) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
	if ShortRevision("") != RevisionUnknown {
		t.Errorf("an absent revision must render as %q, never blank: %q", RevisionUnknown, ShortRevision(""))
	}
	if got := ShortRevision("2f6a1c0d4e8b9a7c5d3f1e0b"); got != "2f6a1c0d4e8b" {
		t.Errorf("a known revision should render short and recognisable, got %q", got)
	}
}

// TestTheRecordedRevisionIsReadFromTheBinaryRatherThanNamedInCode closes the gap
// every other test here leaves open: they all inject a revision through
// SetRevision, so a BuildRevision that returned a constant satisfies all of
// them. Replacing its body with a hard-coded sha leaves this package, story and
// cmd/adlc green without it.
//
// That is the worst failure this whole change can have. A build that says
// "unknown" is useless; a build that names a revision it never read stamps a
// false attribution on every row it appends, and nothing downstream — replay,
// the events surface, verify — can tell that apart from the truth.
//
// The limit, stated rather than papered over: a test binary carries no vcs
// stamp, so `want` here is RevisionUnknown, and a BuildRevision hard-coded to
// RevisionUnknown would still pass. That is the harmless direction — a build
// that admits it does not know. The dangerous one, a named revision nobody
// read, is what this fires on, and the stamped build is checked outside the
// suite by running a `go build` binary, which prints its own sha.
func TestTheRecordedRevisionIsReadFromTheBinaryRatherThanNamedInCode(t *testing.T) {
	// The firing case: the recorded revision is whatever this binary's own build
	// info says, and is not decided anywhere else.
	want := revisionOf(debug.ReadBuildInfo())
	if got := BuildRevision(); got != want {
		t.Errorf("BuildRevision must be this binary's build info (%q), got %q — a revision named in code is a false attribution", want, got)
	}
	if BuildRevision() == "" {
		t.Error("BuildRevision must never be blank; an absence is recorded as RevisionUnknown")
	}
	// The clean case, so the assertion above cannot pass the day the reader
	// itself starts answering the same thing to everything: handed build info
	// that carries a stamp, it still returns the stamp.
	if got := revisionOf(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0f1e2d3c4b5a69788796a5b4c3d2e1f000112233"},
	}}, true); got != "0f1e2d3c4b5a69788796a5b4c3d2e1f000112233" {
		t.Errorf("the reader under test must still read a stamped revision, got %q", got)
	}
}

// TestALedgerFromBeforeRevisionsReadsUnknownNotTampered is the compatibility
// property, and it is the reason the revision is not part of the hash preimage.
//
// The chain here is built by hand in the pre-v2 shape — no revision column at
// all — the way a ledger written by last month's binary actually looks on disk.
// Opening it must migrate the column in, read the rows as UNKNOWN, and accuse
// nobody.
func TestALedgerFromBeforeRevisionsReadsUnknownNotTampered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The v1 shape of the chain, reproduced deliberately: this is what the
	// migration has to cope with, and generating it from today's schema would
	// test nothing.
	if _, err := old.Exec(`
CREATE TABLE adlc_event (
  seq        INTEGER PRIMARY KEY,
  ts_ms      INTEGER NOT NULL,
  kind       TEXT    NOT NULL,
  actor      TEXT    NOT NULL,
  subject    TEXT    NOT NULL DEFAULT '',
  payload    TEXT    NOT NULL,
  prev_hash  TEXT    NOT NULL,
  hash       TEXT    NOT NULL
);
CREATE TABLE adlc_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO adlc_meta(key,value) VALUES('schema_version','1');
CREATE TABLE adlc_head (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  seq INTEGER NOT NULL, hash TEXT NOT NULL, updated_ms INTEGER NOT NULL
);`); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC).UnixMilli()
	payload, err := canonical(NoteRecorded{Text: "written before revisions were recorded"})
	if err != nil {
		t.Fatal(err)
	}
	hash := HashEvent(GenesisHash, 1, ts, KindNoteRecorded, "pm", "", payload)
	// The preimage, pinned by a value rather than by the function that computes
	// it. Every other hash in these tests is produced by the HashEvent under
	// test, so any change to the preimage moves the expectation with it and
	// nothing goes red — including the change this whole file exists to forbid,
	// folding build_rev in. This constant was computed once from the v1
	// arithmetic. If it stops matching, the preimage moved, and every chain any
	// earlier binary ever wrote has just become unreadable to this one.
	const v1Digest = "8592af6221b9fd153c6ae66aef277e52e43e62f8262602bb6639ffd5de64b161"
	if hash != v1Digest {
		t.Fatalf("the event hash preimage moved: want %s, got %s — every existing chain now reads as TAMPERED", v1Digest, hash)
	}
	if _, err := old.Exec(
		`INSERT INTO adlc_event(seq,ts_ms,kind,actor,subject,payload,prev_hash,hash) VALUES(1,?,?,?,'',?,?,?)`,
		ts, string(KindNoteRecorded), "pm", string(payload), GenesisHash, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO adlc_head(id,seq,hash,updated_ms) VALUES(1,1,?,?)`, hash, ts); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	l, err := Open(path)
	if err != nil {
		t.Fatalf("a v1 ledger must still open: %v", err)
	}
	defer l.Close()
	// The projections did not exist in the hand-built file, so derive them from
	// the chain the way an upgraded build does.
	if _, _, err := l.Rebuild(); err != nil {
		t.Fatal(err)
	}
	evs, err := l.Events(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want the one pre-v2 event, got %d", len(evs))
	}
	if evs[0].Revision() != RevisionUnknown {
		t.Errorf("a row with no revision must read %q, got %q", RevisionUnknown, evs[0].Revision())
	}
	if SameBuild(evs[0].Revision(), BuildRevision()) {
		t.Error("a row nobody stamped must never read as this build's own work")
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict == VerdictTampered {
		t.Fatalf("an older ledger is not tampering: %v", rep.Findings)
	}
	if rep.Verdict != VerdictIntact {
		t.Fatalf("want INTACT over a migrated v1 ledger, got %s: %v", rep.Verdict, rep.Findings)
	}
	// The hash is what proves the preimage did not move: this row was hashed by
	// a build that had never heard of a revision column, and it still verifies.
	if !passed(rep, "event_hashes") {
		t.Error("adding the revision column changed the hash preimage, which breaks every existing chain")
	}
	// The migration ran, so the stamp has to say so. A file carrying v2 columns
	// while claiming v1 describes a file that no longer exists.
	if got, err := l.StoredSchemaVersion(); err != nil || got != SchemaVersion {
		t.Errorf("a migrated ledger should be stamped v%d, got v%d (%v)", SchemaVersion, got, err)
	}
}

// TestANewerLedgerIsNeverRestampedBackwards is the firing case for the other
// half of that rule. Lowering the stamp to something this build can write would
// let an out-of-date binary report a record it cannot fully read as INTACT,
// which is the failure the version number exists to prevent.
func TestANewerLedgerIsNeverRestampedBackwards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, l)
	if _, err := l.db.Exec(`UPDATE adlc_meta SET value=? WHERE key='schema_version'`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, _ := again.StoredSchemaVersion(); got != SchemaVersion+1 {
		t.Fatalf("a newer ledger was restamped to v%d — this build cannot claim a layout it does not write", got)
	}
	rep, err := again.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictUnknown {
		t.Fatalf("a newer schema should still read UNKNOWN after a reopen, got %s", rep.Verdict)
	}
}

// TestAReadOnlyHandleOnAnOlderLedgerRefusesReadablyRatherThanDying is the
// firing case for the refusal added beside the build_rev migration.
//
// Every dispatched agent is handed the real ledger through ReadOnlyDSN, and
// Open runs the migration list on every open. A migration already applied fails
// with "duplicate column" even read-only, so only a file genuinely older than
// the binary reaches the write — and there SQLite answered "attempt to write a
// readonly database (8)" and the open failed outright. No read at all, out of a
// command an agent is told it may run, with an error nobody can classify. That
// is the one thing this package must never do about a healthy record: a file
// older than the binary is UNKNOWN and a sentence, never an errno.
//
// The v1 shape is produced by taking the column back off a real ledger rather
// than hand-building the file, because everything else on disk — the projection
// tables, the append-only triggers — has to be present for the failure to land
// where it really lands, in the migration loop rather than in schemaSQL.
func TestAReadOnlyHandleOnAnOlderLedgerRefusesReadablyRatherThanDying(t *testing.T) {
	path := filepath.Join(t.TempDir(), "older.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append("pm", KindNoteRecorded, "", NoteRecorded{Text: "written by the older build"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`ALTER TABLE adlc_event DROP COLUMN build_rev`); err != nil {
		t.Fatalf("could not make the pre-v2 shape: %v", err)
	}
	if _, err := l.db.Exec(`UPDATE adlc_meta SET value='1' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// The firing case: this build, that file, the handle an agent is given.
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
	ro, err := Open(dsn)
	if err == nil {
		ro.Close()
		t.Fatal("a read-only handle cannot have migrated an older ledger; either it wrote, or it opened a file it cannot read")
	}
	if !errors.Is(err, ErrReadOnlyMigration) {
		t.Fatalf("the refusal must be classifiable, got %v", err)
	}
	msg := err.Error()
	// A refusal an agent cannot act on is one it works around. It has to say
	// what is wrong (the migration it still needs), what to do (open it read-write once),
	// and what is NOT wrong — an unexplained failure over the audit record
	// reads as an accusation, and this one is not.
	for _, want := range []string{"build_rev", "read-write", "not damaged", "not an accusation"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should say %q so a reader can act on it: %s", want, msg)
		}
	}
	if strings.Contains(msg, "readonly database") || strings.Contains(msg, "(8)") {
		t.Errorf("the SQLite errno is not an answer anybody can classify: %s", msg)
	}

	// Clean case 1: the same file, opened the way the refusal says to. It
	// migrates, reads, and the pre-v2 row is UNKNOWN rather than TAMPERED.
	rw, err := Open(path)
	if err != nil {
		t.Fatalf("the recovery the refusal names must work: %v", err)
	}
	defer rw.Close()
	evs, err := rw.Events(1, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("read-write open should read the older chain: %d event(s), %v", len(evs), err)
	}
	if evs[0].Revision() != RevisionUnknown {
		t.Errorf("a row from before the column must read %q, got %q", RevisionUnknown, evs[0].Revision())
	}
	rep, err := rw.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictIntact {
		t.Fatalf("an older ledger brought forward is intact, got %s: %v", rep.Verdict, rep.Findings)
	}

	// Clean case 2, and the one that stops this guard becoming a refusal of
	// everything: that same ledger, now current, still opens read-only and
	// still reads. Migrations that write nothing when there is nothing to do
	// must not be mistaken for a file that is behind.
	rw.Close()
	current, err := Open(dsn)
	if err != nil {
		t.Fatalf("a current ledger must still open read-only — that handle is how every agent reads the record: %v", err)
	}
	defer current.Close()
	if evs, rerr := current.Events(1, 0); rerr != nil || len(evs) != 1 {
		t.Fatalf("the read-only handle cannot read a current ledger: %d event(s), %v", len(evs), rerr)
	}
}

// TestTheRefusalConsultsTheFileRatherThanItsSchemaStamp pins the reason the
// refusal above does not gate on StoredSchemaVersion.
//
// A stamp is not evidence that the columns it implies are there. This fleet's
// own ledger is proof: it was stamped v2 by a stray write from an agent's test
// run while no merged code defined a 2. A refusal that only fired when the
// stamp was behind would wave that file through and the read would die one
// layer down, on `no such column: build_rev` — the same error nobody can
// classify, moved somewhere harder to find. What a file needs is the migration
// that was just attempted, not what it says about itself.
func TestTheRefusalConsultsTheFileRatherThanItsSchemaStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stamped-but-short.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append("pm", KindNoteRecorded, "", NoteRecorded{Text: "row"}); err != nil {
		t.Fatal(err)
	}
	// The column gone, the stamp left claiming this build's own layout.
	if _, err := l.db.Exec(`ALTER TABLE adlc_event DROP COLUMN build_rev`); err != nil {
		t.Fatal(err)
	}
	if got, err := l.StoredSchemaVersion(); err != nil || got != SchemaVersion {
		t.Fatalf("the fixture must claim to be current, got v%d (%v)", got, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
	ro, err := Open(dsn)
	if err == nil {
		defer ro.Close()
		_, rerr := ro.Events(1, 0)
		t.Fatalf("a file missing a column it claims to have must be refused at the open, not at the first read (%v)", rerr)
	}
	if !errors.Is(err, ErrReadOnlyMigration) {
		t.Fatalf("the refusal must be classifiable whatever the stamp says, got %v", err)
	}
}

// TestOnlyAMigrationTheHandleMayNotRunIsReportedAsOne is the clean case for the
// discrimination the refusal above rests on.
//
// Saying "open it read-write with this build" about a migration that failed for
// some other reason — a locked file, a statement this build got wrong — sends
// the reader to a recovery that cannot work and hides the real fault. So only
// the one error a read-only handle actually produces earns that sentence, and
// everything else still surfaces as itself.
func TestOnlyAMigrationTheHandleMayNotRunIsReportedAsOne(t *testing.T) {
	restore := migrations
	defer func() { migrations = restore }()
	migrations = append(append([]string{}, restore...), `ALTER TABLE adlc_event ADD COLUMN`)

	_, err := Open(filepath.Join(t.TempDir(), "broken.db"))
	if err == nil {
		t.Fatal("a malformed migration must not open silently")
	}
	if errors.Is(err, ErrReadOnlyMigration) {
		t.Errorf("a migration that failed for its own reasons must not be reported as one a handle may not run: %v", err)
	}
	if !strings.Contains(err.Error(), "migrate (") {
		t.Errorf("the failing statement should still surface as itself: %v", err)
	}
}
