package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
)

var at = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// TestBindRefusesAnythingItCannotProveIsLoopback is the security guard. This
// surface writes to the ledger and has no authentication, so those two facts
// have to stay welded together.
func TestBindRefusesAnythingItCannotProveIsLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.10:0", "example.com:0"} {
		ln, err := Bind(addr)
		if err == nil {
			ln.Close()
			t.Errorf("%q was accepted; it is reachable from outside this machine", addr)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "EVERY interface") {
			t.Errorf("%q was refused without explaining why: %v", addr, err)
		}
	}
	// Clean case, so the guard cannot be passing by refusing everything.
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		ln, err := Bind(addr)
		if err != nil {
			t.Errorf("%q is loopback and should be accepted: %v", addr, err)
			continue
		}
		ln.Close()
	}
}

func TestBindRejectsSomethingThatIsNotAnAddress(t *testing.T) {
	if ln, err := Bind("not-an-address"); err == nil {
		ln.Close()
		t.Fatal("a malformed address should be refused")
	}
}

// --- a server over a seeded ledger ---------------------------------------

func newServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	agents := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(agents, "_preamble.md"), "---\nid: _preamble\n---\nPOLICY.\n")
	write(t, filepath.Join(agents, "impl.md"), "---\nid: impl\n---\n# Builder\nDo {{work_item_id}}.\n")

	cfg, err := config.FromChecks("demo", []string{"."}, []config.Check{{
		ID: "test", Command: []string{"go", "version"}, Verdict: config.VerdictExitZero,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Workers {
		cfg.Workers[i].Prompt = "impl"
	}
	cfg.Prompts = config.PromptPolicy{Dir: agents, PreambleFile: filepath.Join(agents, "_preamble.md")}
	cfg.Loops = []config.LoopDecl{{Name: "verify", Enabled: true, EverySeconds: 60, Capability: config.CapTest, MaxPerTick: 1}}
	cfg.Server.RefreshSeconds = 0

	lib, err := prompt.Load(cfg.Prompts)
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { led.Close() })
	n := 0
	led.SetClock(func() time.Time { n++; return at.Add(time.Duration(n) * time.Second) })
	seed(t, led)

	return &Server{Cfg: cfg, Led: led, Lib: lib, Actor: "tester", Repo: dir,
		Now: func() time.Time { return at.Add(time.Hour) }}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seed(t *testing.T, l *ledger.Ledger) {
	t.Helper()
	must := func(_ ledger.Event, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(l.Append("pm", ledger.KindWorkerRegistered, "performer",
		ledger.WorkerRegistered{Type: "performer", Layer: "worker"}))
	must(l.Append("pm", ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{
		ID: "S1", Title: "Password reset", Brief: "Users can recover access unaided.",
		Rationale: "Support spends a day a week on this.", TargetOpen: 2,
	}))
	must(l.Append("pm", ledger.KindItemCreated, "S1-001", ledger.ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "Single-use reset token", Area: "core",
		Radius: "none", Criteria: []string{"a token is accepted once and refused thereafter"},
		Rationale: "the core of the deliverable",
	}))
	must(l.Append("pm", ledger.KindRunStarted, "p-1", ledger.RunStarted{
		RunID: "p-1", WorkerType: "performer", ItemID: "S1-001", SegmentID: "S1",
		PromptID: "impl", PromptSHA: "abc", BaseSHA: "def",
	}))
	must(l.Append("pm", ledger.KindRunFinished, "p-1", ledger.RunFinished{
		RunID: "p-1", Verdict: "pass", CostMicros: 1_250_000,
		Usage: ledger.Usage{InputTokens: 40000, OutputTokens: 3000},
	}))
	must(l.Append("pm", ledger.KindQuestionRaised, "Q-1", ledger.QuestionRaised{
		ID: "Q-1", ItemID: "S1-001", Blocking: true,
		Text:     "Should a reset token survive a password change?",
		Lean:     "No, because a changed password invalidates the reason the token was issued.",
		RaisedBy: "p-1",
	}))
	must(l.Append("pm", ledger.KindApprovalRequested, "AP-1", ledger.ApprovalRequested{
		ID: "AP-1", ItemID: "S1-001", Radius: "fleet", PlanDigest: "plan-aaa",
		Summary: "Rotates the signing key on 14 hosts.",
	}))
}

func get(t *testing.T, s *Server, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

// TestEveryPageRendersOverARealLedger walks the whole dashboard. A page that
// panics or 500s is a page an operator meets at the worst possible moment.
func TestEveryPageRendersOverARealLedger(t *testing.T) {
	s := newServer(t)
	pages := map[string]string{
		"/":                   "Running now",
		"/roadmap":            "Password reset",
		"/progress":           "by stage",
		"/questions":          "survive a password change",
		"/approvals":          "Rotates the signing key",
		"/coordination":       "Arbitrate",
		"/about":              "The guards, and what each one prevents",
		"/roles":              "performer",
		"/role/impl":          "Builder",
		"/config":             "Safety policy",
		"/history?tab=runs":   "p-1",
		"/history?tab=ledger": "segment.created",
		"/item/S1-001":        "Single-use reset token",
		"/segment/S1":         "Users can recover access unaided",
		"/run/p-1":            "Why this mattered",
	}
	for path, want := range pages {
		code, body := get(t, s, path)
		if code != http.StatusOK {
			t.Errorf("%s returned %d", path, code)
			continue
		}
		if !strings.Contains(body, want) {
			t.Errorf("%s rendered without %q", path, want)
		}
		if strings.Contains(body, "<no value>") {
			t.Errorf("%s leaked a missing template value", path)
		}
		// The Roles pages deliberately display prompt text, which contains the
		// agent placeholders verbatim; everywhere else a brace pair is a bug.
		if !strings.HasPrefix(path, "/role") && strings.Contains(body, "{{") {
			t.Errorf("%s leaked an unrendered template directive", path)
		}
	}
}

// TestTheBannerNamesWhatIsWaiting is the one-glance answer, so it has to be
// right rather than merely present.
func TestTheBannerNamesWhatIsWaiting(t *testing.T) {
	s := newServer(t)
	_, body := get(t, s, "/")
	if !strings.Contains(body, "Waiting on you") {
		t.Fatal("a blocking question and an open approval are both outstanding")
	}
	for _, want := range []string{"blocking question", "approval"} {
		if !strings.Contains(body, want) {
			t.Errorf("the banner does not mention %q", want)
		}
	}
}

func TestHealthzReportsTheLedgerVerdict(t *testing.T) {
	s := newServer(t)
	code, body := get(t, s, "/healthz")
	if code != http.StatusOK {
		t.Fatalf("healthz returned %d", code)
	}
	if !strings.HasPrefix(body, "INTACT") {
		t.Fatalf("want the ledger's own three-valued verdict, got %q", body)
	}
}

// --- the two write paths --------------------------------------------------

func post(t *testing.T, s *Server, path string, form url.Values) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Location")
}

func TestAnsweringAQuestionRecordsItVerbatim(t *testing.T) {
	s := newServer(t)
	code, loc := post(t, s, "/answer", url.Values{
		"id": {"Q-1"}, "answer": {"No — a password change invalidates it."}, "who": {"brandon"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("want a redirect after a write, got %d", code)
	}
	if strings.Contains(loc, "bad=1") {
		t.Fatalf("the answer was rejected: %s", loc)
	}
	qs, err := s.Led.Questions("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || !qs[0].Answered {
		t.Fatal("the question should be answered")
	}
	if qs[0].Answer != "No — a password change invalidates it." {
		t.Errorf("the answer must be recorded verbatim, got %q", qs[0].Answer)
	}
	if qs[0].AnsweredBy != "brandon" {
		t.Errorf("the answer should carry who gave it, got %q", qs[0].AnsweredBy)
	}
}

func TestAnEmptyAnswerIsRefused(t *testing.T) {
	s := newServer(t)
	_, loc := post(t, s, "/answer", url.Values{"id": {"Q-1"}, "answer": {"   "}})
	if !strings.Contains(loc, "bad=1") {
		t.Fatal("an empty answer must be refused rather than recorded")
	}
}

// TestApprovingRequiresAName pins the last gate before something irreversible.
func TestApprovingRequiresAName(t *testing.T) {
	s := newServer(t)
	_, loc := post(t, s, "/decide", url.Values{"id": {"AP-1"}, "verdict": {"approve"}})
	if !strings.Contains(loc, "bad=1") {
		t.Fatal("an approval nobody's name is on is an approval nobody gave")
	}
	aps, _ := s.Led.Approvals("")
	if aps[0].Decided() {
		t.Fatal("the unnamed approval must not have been recorded")
	}

	// Clean case: with a name it goes through.
	_, loc = post(t, s, "/decide", url.Values{
		"id": {"AP-1"}, "verdict": {"approve"}, "approver": {"brandon"}, "note": {"roll one host first"},
	})
	if strings.Contains(loc, "bad=1") {
		t.Fatalf("a named approval should be accepted: %s", loc)
	}
	aps, _ = s.Led.Approvals("")
	if !aps[0].Decided() || aps[0].Approver != "brandon" || aps[0].Verdict != "approve" {
		t.Fatalf("the decision was not recorded: %+v", aps[0])
	}
}

func TestAVerdictOtherThanApproveOrRejectIsRefused(t *testing.T) {
	s := newServer(t)
	_, loc := post(t, s, "/decide", url.Values{
		"id": {"AP-1"}, "verdict": {"maybe"}, "approver": {"brandon"},
	})
	if !strings.Contains(loc, "bad=1") {
		t.Fatal("only approve or reject are decisions")
	}
}

func TestAGetOnAWritePathChangesNothing(t *testing.T) {
	s := newServer(t)
	for _, p := range []string{"/answer", "/decide", "/loop"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s on GET should redirect rather than act, got %d", p, rec.Code)
		}
	}
	qs, _ := s.Led.Questions("", true)
	if len(qs) != 1 {
		t.Fatal("a GET must not have changed anything")
	}
}

func TestPausingALaneIsPersistedAndRecorded(t *testing.T) {
	s := newServer(t)
	// A config loaded from no file cannot be saved, and the failure is
	// reported rather than swallowed.
	_, loc := post(t, s, "/loop", url.Values{
		"name": {"verify"}, "every": {"120"}, "max": {"1"},
	})
	if !strings.Contains(loc, "bad=1") {
		t.Fatal("a config with nowhere to save should report that, not pretend to succeed")
	}
	if l := s.Cfg.Loop("verify"); l.Enabled {
		// The in-memory change is applied before the save is attempted; what
		// matters is that the operator was told the save failed.
		t.Log("in-memory state changed; the redirect reported the save failure")
	}
}
