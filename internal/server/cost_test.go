package server

import (
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// finishRun appends one finished run with whatever usage the caller names, so a
// test can put an unmeasured run beside a measured one in the same window.
func finishRun(t *testing.T, l *ledger.Ledger, id string, worker string, u ledger.Usage) {
	t.Helper()
	if _, err := l.Append("pm", ledger.KindRunStarted, id, ledger.RunStarted{
		RunID: id, WorkerType: worker, ItemID: "S1-001", SegmentID: "S1",
		PromptID: "impl", PromptSHA: "abc", BaseSHA: "def",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append("pm", ledger.KindRunFinished, id, ledger.RunFinished{
		RunID: id, Verdict: "pass", Usage: u,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAnUnmeasuredRunIsDeclaredBesideTheMoneyFigures is the firing case.
//
// A finished run whose four usage counters are zero was not free, it was not
// counted. Dropping it left the Day, Week and per-role figures on the page as
// sums over a set of runs nobody was told about — a partial total wearing the
// label of a complete one.
func TestAnUnmeasuredRunIsDeclaredBesideTheMoneyFigures(t *testing.T) {
	s := newServer(t)
	// A model the built-in table prices, so the measured run produces a real
	// figure and the caveat is qualifying money rather than another zero.
	s.Cfg.Budget.DefaultModel = "claude-sonnet-5"
	// The seeded ledger already holds one measured finished run, p-1.
	finishRun(t, s.Led, "p-2", "performer", ledger.Usage{})

	v := s.CostView()
	if v.Unmeasured != 1 {
		t.Fatalf("one finished run reported no usage; CostView counted %d", v.Unmeasured)
	}
	if v.UnmeasuredDay != 1 {
		t.Errorf("the unmeasured run started inside the 24 hours Day covers; UnmeasuredDay = %d",
			v.UnmeasuredDay)
	}
	if v.UnmeasuredRun != "p-2" {
		t.Errorf("the first unmeasured run should be nameable; got %q", v.UnmeasuredRun)
	}
	if v.Runs != 1 {
		t.Errorf("Runs counts the runs the figures cover, which is the measured one only; got %d", v.Runs)
	}
	if v.Day == 0 || v.Week == 0 {
		t.Fatalf("the measured run should still be priced: day=%v week=%v", v.Day, v.Week)
	}

	note := v.UnmeasuredNote()
	if note == "" {
		t.Fatal("the figures exclude a run and say nothing about it")
	}
	for _, want := range []string{"no token usage", "unknown rather than zero", "p-2"} {
		if !strings.Contains(note, want) {
			t.Errorf("the caveat does not say %q: %s", want, note)
		}
	}
	// Basis is the line printed under the Day and Week tiles, so the caveat has
	// to reach it: a count held only in a struct field is a count on no surface.
	if !strings.Contains(v.Basis, note) {
		t.Errorf("the caveat never reaches the line rendered beside the figures: %s", v.Basis)
	}

	// The per-role figure carries it too, since it is the same partial sum
	// broken down.
	top := s.TopSpendingRole()
	if !strings.Contains(top, "unmeasured") {
		t.Errorf("the per-role figure presents itself as complete: %s", top)
	}

	// And it is actually on the page an operator reads, not only in the view.
	code, body := get(t, s, "/overview")
	if code != 200 {
		t.Fatalf("/overview returned %d", code)
	}
	if !strings.Contains(body, "no token usage") {
		t.Error("the Overview renders the money figures without declaring the run they exclude")
	}
}

// TestAFullyMeasuredLedgerCarriesNoCaveat is the clean case. A caveat that
// fires on every ledger is one nobody reads on the page where it matters.
func TestAFullyMeasuredLedgerCarriesNoCaveat(t *testing.T) {
	s := newServer(t)
	s.Cfg.Budget.DefaultModel = "claude-sonnet-5"
	finishRun(t, s.Led, "p-2", "performer", ledger.Usage{InputTokens: 1000, OutputTokens: 200})

	v := s.CostView()
	if v.Unmeasured != 0 {
		t.Fatalf("every finished run reported usage; CostView counted %d as unmeasured", v.Unmeasured)
	}
	if note := v.UnmeasuredNote(); note != "" {
		t.Errorf("nothing is missing from these figures, yet they are captioned: %s", note)
	}
	if v.Basis != s.PricingBasis() {
		t.Errorf("the basis line grew a caveat over a complete ledger: %s", v.Basis)
	}
	if strings.Contains(s.TopSpendingRole(), "unmeasured") {
		t.Errorf("the per-role figure is complete and should not be hedged: %s", s.TopSpendingRole())
	}
	if _, body := get(t, s, "/overview"); strings.Contains(body, "no token usage") {
		t.Error("the Overview declares an incompleteness it does not have")
	}
}

// TestAnUnmeasuredRunOutsideTheWindowDoesNotCaveatIt keeps the count describing
// the same runs the figures do. A run from last month cannot make this week's
// total incomplete, and a caveat that cannot be reconciled with the figure
// beside it teaches people to ignore the caveat.
func TestAnUnmeasuredRunOutsideTheWindowDoesNotCaveatIt(t *testing.T) {
	s := newServer(t)
	old := at.AddDate(0, 0, -30)
	n := 0
	s.Led.SetClock(func() time.Time { n++; return old.Add(time.Duration(n) * time.Second) })
	finishRun(t, s.Led, "p-old", "performer", ledger.Usage{})

	v := s.CostView()
	if v.Unmeasured != 0 {
		t.Fatalf("the unmeasured run is 30 days old and outside every figure here; counted %d", v.Unmeasured)
	}
	if v.UnmeasuredNote() != "" {
		t.Errorf("this week's figures are captioned about a run from last month: %s", v.UnmeasuredNote())
	}
}
