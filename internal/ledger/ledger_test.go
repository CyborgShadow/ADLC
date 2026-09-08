package ledger

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Ledger {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	n := 0
	l.SetClock(func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) })
	return l
}

func seed(t *testing.T, l *Ledger) {
	t.Helper()
	must := func(_ Event, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	must(l.Append("pm", KindSegmentCreated, "S1", SegmentCreated{ID: "S1", Title: "Foundation"}))
	must(l.Append("pm", KindItemCreated, "S1-001", ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "First item", Radius: "none",
		Criteria: []string{"it works"},
	}))
	must(l.Append("pm", KindRunStarted, "wp-1", RunStarted{RunID: "wp-1", WorkerType: "performer", ItemID: "S1-001", SegmentID: "S1"}))
	must(l.Append("pm", KindRunFinished, "wp-1", RunFinished{
		RunID: "wp-1", Verdict: "pass", CostMicros: 1_500_000,
		Usage: Usage{InputTokens: 1000, OutputTokens: 500},
	}))
}

func TestACleanLedgerVerifiesIntact(t *testing.T) {
	l := open(t)
	seed(t, l)

	rep, err := l.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Verdict != VerdictIntact {
		t.Fatalf("want INTACT, got %s: %v", rep.Verdict, rep.Findings)
	}
	// The clean case is asserted deliberately. A test that only proves a guard
	// fires passes vacuously the day the guard starts flagging everything.
	for _, c := range rep.Checks {
		if !c.Pass {
			t.Errorf("check %s failed on a clean ledger: %s", c.Name, c.Detail)
		}
	}
	if rep.Events != 4 {
		t.Errorf("want 4 events, got %d", rep.Events)
	}
}

func TestAnEditedPayloadReadsAsTampered(t *testing.T) {
	l := open(t)
	seed(t, l)

	// Go around the API the way an attacker would: drop the trigger, then rewrite
	// history. If verification depended on the writer behaving, it would be
	// worthless.
	if _, err := l.db.Exec(`DROP TRIGGER adlc_event_no_update`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := l.db.Exec(`UPDATE adlc_event SET payload=replace(payload,'Foundation','Rewritten') WHERE seq=1`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Verdict != VerdictTampered {
		t.Fatalf("want TAMPERED, got %s", rep.Verdict)
	}
	if !hasFailing(rep, "event_hashes") {
		t.Errorf("the rewritten row should fail event_hashes; checks: %v", rep.Checks)
	}
	if !hasFailing(rep, "guards_present") {
		t.Errorf("a removed append-only guard should be reported as missing")
	}
}

func TestTruncatingTheChainIsCaughtByTheAnchor(t *testing.T) {
	l := open(t)
	seed(t, l)

	if _, err := l.db.Exec(`DROP TRIGGER adlc_event_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`DELETE FROM adlc_event WHERE seq=4`); err != nil {
		t.Fatal(err)
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictTampered {
		t.Fatalf("want TAMPERED after truncation, got %s", rep.Verdict)
	}
	// Truncation leaves a chain that still walks cleanly. The anchor is the only
	// thing that notices, which is why it lives outside the event table.
	if !hasFailing(rep, "head_anchor") {
		t.Fatalf("truncation should fail head_anchor, got checks %v", rep.Checks)
	}
}

// A tamper detector that accuses at its own obsolescence gets ignored, and an
// ignored detector is not a control.
func TestAnEventKindFromANewerBuildIsUnknownNotTampered(t *testing.T) {
	l := open(t)
	seed(t, l)

	// Append a well-formed event of a kind this build does not know, through the
	// real Append path so its hash and linkage are correct.
	if _, err := l.Append("pm", Kind("segment.dependencies_amended"), "S1", map[string]string{"note": "from a newer build"}); err != nil {
		t.Fatalf("append future kind: %v", err)
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictUnknown {
		t.Fatalf("want UNKNOWN, got %s — an unrecognised kind must never read as tampering", rep.Verdict)
	}
	for _, c := range rep.Checks {
		if c.Tier == TierIntegrity && !c.Pass {
			t.Errorf("integrity check %s failed, but nothing was tampered with: %s", c.Name, c.Detail)
		}
	}
	if !hasFailing(rep, "known_kinds") {
		t.Errorf("the unknown kind should be reported under knowledge")
	}
}

func TestTheAppendOnlyGuardsActuallyRefuse(t *testing.T) {
	l := open(t)
	seed(t, l)

	if _, err := l.db.Exec(`UPDATE adlc_event SET actor='someone else' WHERE seq=1`); err == nil {
		t.Fatal("UPDATE on the chain was accepted; the append-only guard is vacuous")
	}
	if _, err := l.db.Exec(`DELETE FROM adlc_event WHERE seq=1`); err == nil {
		t.Fatal("DELETE on the chain was accepted; the append-only guard is vacuous")
	}
	// And the clean case: an ordinary append still works, so the guard is
	// refusing the right thing rather than everything.
	if _, err := l.Append("pm", KindNoteRecorded, "", NoteRecorded{Text: "still writable"}); err != nil {
		t.Fatalf("a normal append should still succeed: %v", err)
	}
}

func TestProjectionsAreRebuildableFromTheChain(t *testing.T) {
	l := open(t)
	seed(t, l)
	if _, err := l.Append("pm", KindItemTransitioned, "S1-001", ItemTransitioned{
		ItemID: "S1-001", From: "queued", To: "ready", RunID: "wp-1",
	}); err != nil {
		t.Fatal(err)
	}
	it, err := l.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != "ready" {
		t.Fatalf("state %q, want ready", it.State)
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !passed(rep, "projection_replay") {
		t.Fatalf("projections should replay identically: %v", rep.Findings)
	}

	// Firing case: corrupt a projection and the replay diff must notice. This is
	// what makes "the reports agree with the record" checked rather than hoped
	// for.
	if _, err := l.db.Exec(`UPDATE adlc_item SET state='done' WHERE id='S1-001'`); err != nil {
		t.Fatal(err)
	}
	rep2, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if passed(rep2, "projection_replay") {
		t.Fatal("a projection edited behind the chain's back was not detected")
	}
}

func TestAnAppendWithNoActorIsRefused(t *testing.T) {
	l := open(t)
	if _, err := l.Append("", KindNoteRecorded, "", NoteRecorded{Text: "who wrote this?"}); err == nil {
		t.Fatal("an unattributable row was accepted onto an append-only chain")
	}
}

func TestWorkerStatsCountAWorkerWithNoRuns(t *testing.T) {
	l := open(t)
	for _, w := range []string{"performer", "verifier"} {
		if _, err := l.Append("pm", KindWorkerRegistered, w, WorkerRegistered{Type: w, Layer: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Append("pm", KindRunStarted, "p-1", RunStarted{RunID: "p-1", WorkerType: "performer"}); err != nil {
		t.Fatal(err)
	}
	stats, err := l.WorkerStats("")
	if err != nil {
		t.Fatal(err)
	}
	// The whole mechanism: the dark worker has to appear as a row of zeroes.
	// Reading activity from the runs alone renders its absence as silence.
	var sawVerifier bool
	for _, s := range stats {
		if s.Type == "verifier" {
			sawVerifier = true
			if s.Runs != 0 {
				t.Errorf("verifier should have 0 runs, got %d", s.Runs)
			}
		}
	}
	if !sawVerifier {
		t.Fatal("a registered worker with zero runs did not appear in the stats at all — this is exactly the failure that hid a dark verification layer for a whole project")
	}
}

func TestAStartedRunWithNoEndIsUnknownNotAPass(t *testing.T) {
	l := open(t)
	if _, err := l.Append("pm", KindWorkerRegistered, "performer", WorkerRegistered{Type: "performer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append("pm", KindRunStarted, "p-1", RunStarted{RunID: "p-1", WorkerType: "performer"}); err != nil {
		t.Fatal(err)
	}
	stats, _ := l.WorkerStats("")
	for _, s := range stats {
		if s.Type != "performer" {
			continue
		}
		if s.Unfinished != 1 {
			t.Errorf("want 1 unfinished run, got %d", s.Unfinished)
		}
		if s.Pass != 0 {
			t.Errorf("a run with no recorded end must not count as a pass; got %d", s.Pass)
		}
	}
}

func TestSchemaVersionNewerThanTheBuildReadsUnknown(t *testing.T) {
	l := open(t)
	seed(t, l)
	if _, err := l.db.Exec(`UPDATE adlc_meta SET value=? WHERE key='schema_version'`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictUnknown {
		t.Fatalf("a newer schema should read UNKNOWN, got %s", rep.Verdict)
	}
}

func hasFailing(rep *Report, name string) bool {
	for _, c := range rep.Checks {
		if c.Name == name && !c.Pass {
			return true
		}
	}
	return false
}

func passed(rep *Report, name string) bool {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c.Pass
		}
	}
	return false
}

var _ = sql.ErrNoRows

// TestAMigratedTableIsNotTampering pins a defect this verifier found in
// itself: a table created by an early build and then migrated carries its new
// columns appended at the end, while the same table created fresh carries them
// in the middle. The rows are identical; only the physical layout differs. A
// fingerprint that walked columns in returned order therefore made every
// migrated ledger report TAMPERED — which is the worst thing a tamper
// detector can say when nothing is wrong.
func TestAMigratedTableIsNotTampering(t *testing.T) {
	l := open(t)
	if _, err := l.Append("pm", KindSegmentCreated, "S1", SegmentCreated{
		ID: "S1", Title: "Foundation", Brief: "do the thing", TargetOpen: 3,
	}); err != nil {
		t.Fatal(err)
	}
	// Rebuild the table the way an older build left it — the added columns at
	// the end rather than in the middle — with byte-identical contents.
	stmts := []string{
		`CREATE TABLE seg_old (id TEXT PRIMARY KEY, title TEXT NOT NULL,
			depends_on TEXT NOT NULL DEFAULT '[]', created_seq INTEGER NOT NULL,
			updated_seq INTEGER NOT NULL, brief TEXT NOT NULL DEFAULT '',
			target_open INTEGER NOT NULL DEFAULT 0, rationale TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'drafted', rank INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO seg_old SELECT id,title,depends_on,created_seq,updated_seq,brief,
			target_open,rationale,state,rank FROM adlc_segment`,
		`DROP TABLE adlc_segment`,
		`ALTER TABLE seg_old RENAME TO adlc_segment`,
	}
	for _, s := range stmts {
		if _, err := l.db.Exec(s); err != nil {
			t.Fatalf("reshape: %v", err)
		}
	}
	rep, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictIntact {
		t.Fatalf("a migrated table holding identical rows must verify INTACT, got %s: %v", rep.Verdict, rep.Findings)
	}

	// Clean case: the guard still fires on a genuine content difference, so this
	// fix did not buy compatibility by going blind.
	if _, err := l.db.Exec(`UPDATE adlc_segment SET brief='something else' WHERE id='S1'`); err != nil {
		t.Fatal(err)
	}
	rep2, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Verdict != VerdictTampered {
		t.Fatal("a projection edited behind the chain's back must still be caught")
	}
}
