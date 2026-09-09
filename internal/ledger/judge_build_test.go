package ledger

import "testing"

// The criterion says EVERY event the control plane appends carries the build's
// revision, and these read the raw column rather than Event.Revision().
//
// Reading it through Revision() would hide the failure that matters: a row
// whose column was never written comes back from that accessor as "unknown",
// which is indistinguishable from a row an unstamped build deliberately
// stamped. Only the stored value tells those apart, and only one of them is a
// bug.
func rawRevisions(t *testing.T, l *Ledger) []string {
	t.Helper()
	rows, err := l.db.Query(`SELECT build_rev FROM adlc_event ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJudgeEveryRowOnTheChainNamesTheBuildThatAppendedIt(t *testing.T) {
	const rev = "1234567890abcdef1234567890abcdef12345678"
	l := open(t)
	l.SetRevision(rev)
	for _, k := range []Kind{KindNoteRecorded, KindWorkerRegistered, KindSegmentCreated, KindNoteRecorded} {
		if _, err := l.Append("pm", k, "S1-001", NoteRecorded{Text: string(k)}); err != nil {
			t.Fatal(err)
		}
	}
	got := rawRevisions(t, l)
	if len(got) != 4 {
		t.Fatalf("want 4 rows on the chain, got %d — a test over zero rows proves nothing", len(got))
	}
	for i, r := range got {
		if r != rev {
			t.Errorf("row %d stores %q, want %q: one unstamped row is enough to make attribution a guess", i+1, r, rev)
		}
	}
}

// The other half of the same rule, so the test above cannot pass by a build_rev
// column that simply echoes whatever it is handed: an unstamped build has to
// store the word, not the blank.
func TestJudgeAnUnstampedBuildStoresTheWordNotABlank(t *testing.T) {
	l := open(t)
	l.SetRevision("")
	if _, err := l.Append("pm", KindNoteRecorded, "", NoteRecorded{Text: "unstamped"}); err != nil {
		t.Fatal(err)
	}
	got := rawRevisions(t, l)
	if len(got) != 1 || got[0] != RevisionUnknown {
		t.Fatalf("an unstamped build must store %q, got %q — a blank column is how a row that was never stamped looks, and this one was", RevisionUnknown, got)
	}
}
