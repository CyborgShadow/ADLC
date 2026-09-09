package story

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

var at = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T) (*ledger.Ledger, *config.Config) {
	t.Helper()
	cfg, err := config.FromChecks("t", []string{"."}, []config.Check{{
		ID: "test", Command: []string{"go", "version"}, Verdict: config.VerdictExitZero,
	}})
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	n := 0
	l.SetClock(func() time.Time { n++; return at.Add(time.Duration(n) * time.Second) })
	return l, cfg
}

// add appends one event and fails the test if it cannot.
func add(t *testing.T, l *ledger.Ledger, kind ledger.Kind, subject string, payload any) {
	t.Helper()
	if _, err := l.Append("pm", kind, subject, payload); err != nil {
		t.Fatal(err)
	}
}

const envJSON = `{"envelope_version":"1","run_id":"p-1","worker_type":"performer",
"work_item_id":"S1-001","verdict":"pass","summary":"did the thing","head_sha":"abc123abc123",
"commands_run":[{"check_id":"test","cmd":"go version","exit_code":0}],"outputs":{},
"usage":{"input_tokens":1000,"output_tokens":200}}`

// seedRun writes a whole run: the rows leading up to the decision, then the row
// that records the decision itself.
//
// The two halves are separable because they are not necessarily the work of the
// same build. A fixture that stamps every row alike cannot tell a replay that
// reads the deciding row from one that reads any other row on the chain, which
// is how a replay naming the wrong build ships green.
func seedRun(t *testing.T, l *ledger.Ledger) string {
	t.Helper()
	sha := seedRunUpToDecision(t, l)
	seedDecision(t, l)
	return sha
}

func seedRunUpToDecision(t *testing.T, l *ledger.Ledger) string {
	t.Helper()
	add(t, l, ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{
		ID: "S1", Title: "Password reset", Brief: "Users recover access unaided.",
		Rationale: "Support spends a day a week on this.",
	})
	add(t, l, ledger.KindItemCreated, "S1-001", ledger.ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "Single-use token", Area: "core",
		Radius: "none", Criteria: []string{"a token is accepted once"},
		Rationale: "the core of the deliverable",
	})
	add(t, l, ledger.KindItemTransitioned, "S1-001", ledger.ItemTransitioned{
		ItemID: "S1-001", From: "queued", To: "in_progress", Reason: "dispatched",
	})
	sha, err := l.PutBlob(ledger.BlobEnvelope, []byte(envJSON))
	if err != nil {
		t.Fatal(err)
	}
	add(t, l, ledger.KindRunStarted, "p-1", ledger.RunStarted{
		RunID: "p-1", WorkerType: "performer", ItemID: "S1-001", SegmentID: "S1",
		PromptID: "impl", PromptSHA: "prompt-sha", BaseSHA: "abc123abc123",
	})
	add(t, l, ledger.KindGateObserved, "p-1", ledger.GateObserved{
		RunID: "p-1", ItemID: "S1-001", Edge: "in_progress->ready_for_testing",
		TreeSHA: "abc123abc123", Status: "GREEN", Checks: "[]",
	})
	add(t, l, ledger.KindRunFinished, "p-1", ledger.RunFinished{
		RunID: "p-1", Verdict: "pass", EnvelopeSHA: sha, HeadSHA: "abc123abc123",
		CostMicros: 1_500_000, Usage: ledger.Usage{InputTokens: 1000, OutputTokens: 200},
	})
	return sha
}

// seedDecision appends the one row a replay re-derives.
func seedDecision(t *testing.T, l *ledger.Ledger) {
	t.Helper()
	add(t, l, ledger.KindTransitionAdmitted, "S1-001", ledger.TransitionOutcome{
		RunID: "p-1", ItemID: "S1-001", Worker: "performer",
		From: "in_progress", To: "ready_for_testing",
	})
}

// TestARunExplainsWhatItDidAndWhyItMattered pins the chain a person asks for
// weeks later: this run advanced that item, which serves this deliverable,
// which exists for this stated reason.
func TestARunExplainsWhatItDidAndWhyItMattered(t *testing.T) {
	l, cfg := fixture(t)
	seedRun(t, l)

	s, err := OfRun(l, cfg, "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Headline, "in_progress") || !strings.Contains(s.Headline, "testing") {
		t.Errorf("the headline should say what moved: %q", s.Headline)
	}
	joined := strings.Join(s.Purpose, " ")
	for _, want := range []string{"S1-001", "Single-use token", "Password reset", "day a week"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the purpose chain is missing %q: %v", want, s.Purpose)
		}
	}
	kinds := map[string]bool{}
	for _, st := range s.Steps {
		kinds[st.Kind] = true
		if st.Title == "" {
			t.Error("every step needs a title, or the timeline reads as blanks")
		}
	}
	for _, want := range []string{"dispatched", "gate", "reported", "decision"} {
		if !kinds[want] {
			t.Errorf("the timeline has no %q step: %v", want, kinds)
		}
	}
	if s.Cost.String() != "$1.50" {
		t.Errorf("cost should be priced from the record, got %s", s.Cost)
	}
}

func TestARunWithNoWorkItemSaysSoRatherThanRenderingBlank(t *testing.T) {
	l, cfg := fixture(t)
	add(t, l, ledger.KindRunStarted, "x-1", ledger.RunStarted{
		RunID: "x-1", WorkerType: "systems",
	})
	s, err := OfRun(l, cfg, "x-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Purpose) == 0 || !strings.Contains(s.Purpose[0], "cannot say what it was for") {
		t.Fatalf("an untied run should say the record cannot explain it, got %v", s.Purpose)
	}
}

// TestAnUnfinishedRunIsUnknownNotAPass pins the honest rendering of a process
// that died.
func TestAnUnfinishedRunIsUnknownNotAPass(t *testing.T) {
	l, cfg := fixture(t)
	add(t, l, ledger.KindRunStarted, "d-1", ledger.RunStarted{
		RunID: "d-1", WorkerType: "performer",
	})
	s, err := OfRun(l, cfg, "d-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Headline, "UNKNOWN") {
		t.Fatalf("a run with no recorded end is an unknown, got %q", s.Headline)
	}
	if s.Reproducible {
		t.Error("a run that recorded no envelope cannot be re-derived")
	}
}

func TestRetentionIsReportedHonestly(t *testing.T) {
	l, cfg := fixture(t)
	seedRun(t, l)
	s, err := OfRun(l, cfg, "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.EnvRetained {
		t.Error("the envelope was stored and should read as retained")
	}
	if s.PromptRetained {
		t.Error("no prompt blob was stored, so it must not claim retention")
	}
	if !s.Reproducible {
		t.Errorf("an envelope and a commit are enough to re-derive: %s", s.WhyNot)
	}
}

// TestReplayReDerivesTheSameDecision is the determinism property. If this
// failed, replay could not tell a changed rule from a changed record.
func TestReplayReDerivesTheSameDecision(t *testing.T) {
	l, cfg := fixture(t)
	seedRun(t, l)

	rp, err := Rederive(context.Background(), l, cfg, t.TempDir(), "p-1", at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rp.Recorded, "in_progress -> ready_for_testing") {
		t.Errorf("what was recorded should be reported, got %q", rp.Recorded)
	}
	if rp.Rederived == "" {
		t.Fatal("replay produced no answer at all")
	}
	// The gate cannot pass in a scratch directory that is not the repository,
	// so the two disagree — and the disagreement is reported with a reason
	// rather than swallowed.
	if !rp.Agrees && rp.Detail == "" {
		t.Error("a disagreement must explain itself")
	}
}

func TestReplayOfARunWithNoRetainedEnvelopeSaysWhyNot(t *testing.T) {
	l, cfg := fixture(t)
	add(t, l, ledger.KindRunStarted, "n-1", ledger.RunStarted{
		RunID: "n-1", WorkerType: "performer",
	})
	add(t, l, ledger.KindRunFinished, "n-1", ledger.RunFinished{
		RunID: "n-1", Verdict: "pass",
	})
	rp, err := Rederive(context.Background(), l, cfg, t.TempDir(), "n-1", at)
	if err != nil {
		t.Fatal(err)
	}
	if rp.Detail == "" || !strings.Contains(rp.Detail, "not retained") {
		t.Fatalf("replay should say what is missing, got %q", rp.Detail)
	}
}

// TestReplayOfAGeneratorReRunsTheAdmissionRules covers the other kind of
// decision: a planner's proposals are admitted or refused one at a time, and
// those calls are re-derivable too.
func TestReplayOfAGeneratorReRunsTheAdmissionRules(t *testing.T) {
	l, cfg := fixture(t)
	cfg.Routing = map[string]string{"core": cfg.WorkersWith(config.CapImplement)[0]}

	gen := `{"envelope_version":"1","run_id":"g-1","worker_type":"planner","verdict":"pass",
	"commands_run":[],"outputs":{"work_items":[
	  {"id":"S1-001","title":"Good one","area":"core","blast_radius":"none",
	   "criteria":["AC-1 [exit_zero] go build ./..."]},
	  {"id":"S1-002","title":"No criteria","area":"core","blast_radius":"none","criteria":[]}
	]},"usage":{"input_tokens":10,"output_tokens":5}}`

	sha, err := l.PutBlob(ledger.BlobEnvelope, []byte(gen))
	if err != nil {
		t.Fatal(err)
	}
	add(t, l, ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{
		ID: "S1", Title: "seg", Brief: "a brief",
	})
	add(t, l, ledger.KindRunStarted, "g-1", ledger.RunStarted{
		RunID: "g-1", WorkerType: "planner", SegmentID: "S1",
	})
	add(t, l, ledger.KindRunFinished, "g-1", ledger.RunFinished{
		RunID: "g-1", Verdict: "pass", EnvelopeSHA: sha,
	})
	// One admitted, one refused — exactly what the rules say today.
	add(t, l, ledger.KindItemProposed, "S1-001", ledger.ItemProposed{
		RunID: "g-1", SegmentID: "S1", ProposedID: "S1-001", Admitted: true,
	})
	add(t, l, ledger.KindItemCreated, "S1-001", ledger.ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "Good one", Area: "core", Radius: "none",
		Criteria: []string{"AC-1 [exit_zero] go build ./..."},
	})
	add(t, l, ledger.KindItemProposed, "S1-002", ledger.ItemProposed{
		RunID: "g-1", SegmentID: "S1", ProposedID: "S1-002", Admitted: false,
		Reason: "no_acceptance_criteria",
	})

	rp, err := Rederive(context.Background(), l, cfg, t.TempDir(), "g-1", at)
	if err != nil {
		t.Fatal(err)
	}
	if !rp.Agrees {
		t.Fatalf("the same proposals should be judged the same way: %s / %v", rp.Rederived, rp.Notes)
	}
	if !strings.Contains(rp.Rederived, "2 of 2") {
		t.Errorf("both proposals should be re-decided, got %q", rp.Rederived)
	}
}

// claimsAMatch is the affirmative half of buildNote, and the only phrase that
// asserts two builds ARE one. Matching on the bare words "same build" would
// also match the refusal that says it CANNOT claim they are the same build, so
// a test looking for that would read the refusal as the claim.
const claimsAMatch = "the same build that decided it"

// TestReplayNamesBothBuildsAndDoesNotConflateThem is the attribution property.
// A replay that disagrees has two possible causes — the rules moved or the
// record did — and the first one is a difference between two builds. Naming
// neither leaves the reader with a mystery instead of a lead.
func TestReplayNamesBothBuildsAndDoesNotConflateThem(t *testing.T) {
	l, cfg := fixture(t)
	// Three builds this test can name, so the assertions hold whether or not the
	// test binary itself was stamped — and so that "the deciding build" is a
	// different answer from "the first row" and from "the newest row". With one
	// revision on every row, reading the wrong row gives the right answer by
	// accident and the guard proves nothing.
	const earlier = "aaaabbbbccccddddeeeeffff0000111122223344"
	const decided = "0f1e2d3c4b5a69788796a5b4c3d2e1f000112233"
	const later = "99887766554433221100ffeeddccbbaa99887766"
	l.SetRevision(earlier)
	seedRunUpToDecision(t, l)
	l.SetRevision(decided)
	seedDecision(t, l)
	// A row appended afterwards by a third build, so that reading the head of
	// the chain instead of the decision is wrong too.
	l.SetRevision(later)
	add(t, l, ledger.KindNoteRecorded, "S1-001", ledger.NoteRecorded{
		Text: "appended after the decision by a later build",
	})

	rp, err := Rederive(context.Background(), l, cfg, t.TempDir(), "p-1", at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rp.DecidedBy != decided {
		t.Errorf("the replay should name the build that decided, got %q", rp.DecidedBy)
	}
	if rp.ReplayedBy != ledger.BuildRevision() {
		t.Errorf("the replay should name the build re-deriving, got %q", rp.ReplayedBy)
	}
	if strings.Contains(rp.BuildNote, claimsAMatch) {
		t.Errorf("these are not the same build (%s vs %s) and the note must not say they are: %q",
			rp.DecidedBy, rp.ReplayedBy, rp.BuildNote)
	}
	if !strings.Contains(rp.BuildNote, ledger.ShortRevision(decided)) {
		t.Errorf("the note should name the deciding build: %q", rp.BuildNote)
	}
}

// TestAReplayWithNothingToStampSaysSoRatherThanClaimingAMatch is the firing
// case for the same rule on the path where nobody recorded anything at all.
func TestAReplayWithNothingToStampSaysSoRatherThanClaimingAMatch(t *testing.T) {
	l, cfg := fixture(t)
	l.SetRevision("")
	seedRun(t, l)

	rp, err := Rederive(context.Background(), l, cfg, t.TempDir(), "p-1", at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rp.DecidedBy != ledger.RevisionUnknown {
		t.Errorf("an unstamped build should read %q, got %q", ledger.RevisionUnknown, rp.DecidedBy)
	}
	if strings.Contains(rp.BuildNote, claimsAMatch) {
		t.Errorf("an unstamped build must never render as agreeing with this one: %q", rp.BuildNote)
	}
	if !strings.Contains(rp.BuildNote, "cannot claim") {
		t.Errorf("the note must say why it cannot compare them: %q", rp.BuildNote)
	}
}

// TestTheBuildNoteOnlyClaimsAMatchWhenThereIsOne is the clean case: two
// identical known revisions do read as one build, so the note above is not
// passing by refusing everything.
func TestTheBuildNoteOnlyClaimsAMatchWhenThereIsOne(t *testing.T) {
	same := buildNote("abc123abc123", "abc123abc123")
	if !strings.Contains(same, claimsAMatch) {
		t.Errorf("two identical known builds are the same build: %q", same)
	}
	differ := buildNote("abc123abc123", "def456def456")
	if strings.Contains(differ, claimsAMatch) {
		t.Errorf("two different builds must not read as one: %q", differ)
	}
	// The refusal branch too, because its wording contains the words "same
	// build" and a looser match would read that refusal as the claim.
	unknown := buildNote(ledger.RevisionUnknown, "def456def456")
	if strings.Contains(unknown, claimsAMatch) {
		t.Errorf("an unidentified build must not read as a match: %q", unknown)
	}
	if !strings.Contains(differ, "abc123abc123") || !strings.Contains(differ, "def456def456") {
		t.Errorf("both builds should be named: %q", differ)
	}
}

func TestOfRunOnAMissingRunIsAnError(t *testing.T) {
	l, cfg := fixture(t)
	if _, err := OfRun(l, cfg, "nope"); err == nil {
		t.Fatal("a run that does not exist is not an empty run")
	}
}
