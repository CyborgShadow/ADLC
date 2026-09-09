package report

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

var at = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T) (*ledger.Ledger, *config.Config) {
	t.Helper()
	cfg, err := config.FromChecks("demo", []string{"."}, []config.Check{{
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

func add(t *testing.T, l *ledger.Ledger, kind ledger.Kind, subject string, payload any) {
	t.Helper()
	if _, err := l.Append("pm", kind, subject, payload); err != nil {
		t.Fatal(err)
	}
}

// TestTheFleetReportNamesWorkersThatHaveProducedNothing is the roll call. A
// worker with no runs contributes no rows, so it has to be counted from the
// registry or its silence renders as an absence rather than an alarm.
func TestTheFleetReportNamesWorkersThatHaveProducedNothing(t *testing.T) {
	l, cfg := fixture(t)
	for _, w := range cfg.Workers {
		add(t, l, ledger.KindWorkerRegistered, w.Type,
			ledger.WorkerRegistered{Type: w.Type, Layer: w.Layer, LowCadence: w.LowCadence})
	}
	add(t, l, ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{ID: "S1", Title: "First"})
	add(t, l, ledger.KindItemCreated, "S1-001", ledger.ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "An item", Radius: "none",
		Criteria: []string{"it works"},
	})
	add(t, l, ledger.KindRunStarted, "p-1", ledger.RunStarted{
		RunID: "p-1", WorkerType: "performer", ItemID: "S1-001", SegmentID: "S1",
	})
	add(t, l, ledger.KindRunFinished, "p-1", ledger.RunFinished{RunID: "p-1", Verdict: "pass"})

	out, err := Fleet(l, cfg, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NEVER RUN") {
		t.Fatal("the roll call must appear")
	}
	if !strings.Contains(out, "This is absence, not health") {
		t.Error("the report should say what a zero means, not just print one")
	}
	for _, w := range []string{"tester", "validator"} {
		if !strings.Contains(out, w) {
			t.Errorf("%s has produced nothing and must be named", w)
		}
	}
	if !strings.Contains(out, "not self-reported") {
		t.Error("the activity table should say where its numbers come from")
	}
}

func TestAnUnpricedRunIsReportedAsUnknownNotFolded(t *testing.T) {
	l, cfg := fixture(t)
	add(t, l, ledger.KindWorkerRegistered, "performer", ledger.WorkerRegistered{Type: "performer"})
	add(t, l, ledger.KindRunStarted, "p-1", ledger.RunStarted{RunID: "p-1", WorkerType: "performer"})
	add(t, l, ledger.KindRunFinished, "p-1", ledger.RunFinished{
		RunID: "p-1", Verdict: "pass", Model: "unpriced-model",
		Usage: ledger.Usage{InputTokens: 100000, OutputTokens: 5000},
	})
	out, err := Fleet(l, cfg, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "UNPRICED") {
		t.Fatal("a run under a model with no price must be reported, not silently counted as zero")
	}
	if !strings.Contains(out, "unknown, not zero") {
		t.Error("the report should say what an unpriced run means")
	}
}

// unmeasuredFixture records one worker with two finished runs in the same
// segment: one that reported its tokens, and one that reported none at all.
// Both surfaces have to tell the two apart.
func unmeasuredFixture(t *testing.T) (*ledger.Ledger, *config.Config) {
	t.Helper()
	l, cfg := fixture(t)
	add(t, l, ledger.KindWorkerRegistered, "performer", ledger.WorkerRegistered{Type: "performer"})
	add(t, l, ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{ID: "S1", Title: "First"})
	add(t, l, ledger.KindRunStarted, "p-1", ledger.RunStarted{
		RunID: "p-1", WorkerType: "performer", SegmentID: "S1", Model: "claude-sonnet-4-5"})
	add(t, l, ledger.KindRunFinished, "p-1", ledger.RunFinished{
		RunID: "p-1", Verdict: "pass", Model: "claude-sonnet-4-5",
		Usage: ledger.Usage{InputTokens: 1_000_000}, CostMicros: 3_000_000})
	add(t, l, ledger.KindRunStarted, "p-2", ledger.RunStarted{
		RunID: "p-2", WorkerType: "performer", SegmentID: "S1", Model: "claude-sonnet-4-5"})
	// The defect in one record: a finished run whose envelope carried no usage
	// block, which the old cost column priced at a confident $0.00.
	add(t, l, ledger.KindRunFinished, "p-2", ledger.RunFinished{
		RunID: "p-2", Verdict: "pass", Model: "claude-sonnet-4-5"})
	return l, cfg
}

// TestAnUnmeasuredRunRendersAsUnknownNotZero covers both money surfaces. A run
// that reported no usage contributes zero to every sum, so an unqualified
// figure is arithmetically right and factually a lie — and $0.00 is exactly
// what a cost column nobody wired up prints.
func TestAnUnmeasuredRunRendersAsUnknownNotZero(t *testing.T) {
	l, cfg := unmeasuredFixture(t)

	fleet, err := Fleet(l, cfg, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	seg, err := Segment(l, cfg, "S1")
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"fleet": fleet, "segment": seg} {
		if !strings.Contains(out, "UNKNOWN") {
			t.Errorf("the %s report prices an unmeasured run rather than admitting it cannot:\n%s", name, out)
		}
		if !strings.Contains(out, "reported no token usage") {
			t.Errorf("the %s report should say why the figure is UNKNOWN", name)
		}
		// The measured part is still worth showing; what it must not do is stand
		// in the cost column as though it were the whole cost.
		if !strings.Contains(out, "$3.00") {
			t.Errorf("the %s report should still show what it could measure:\n%s", name, out)
		}
	}

	// A total containing an unmeasured run is a lower bound, and says so.
	if !strings.Contains(fleet, "INCOMPLETE") {
		t.Errorf("a total built partly on unmeasured runs must not be presented as a total:\n%s", fleet)
	}
	if !strings.Contains(fleet, "UNMEASURED") || !strings.Contains(fleet, "p-2") {
		t.Errorf("the fleet report should name the run whose cost is unknown:\n%s", fleet)
	}
}

// The clean case. Every run measured means no caveat anywhere: a report that
// cries INCOMPLETE over a complete record is one nobody reads the second time.
func TestAFullyMeasuredFleetIsNotLabelledIncomplete(t *testing.T) {
	l, cfg := fixture(t)
	add(t, l, ledger.KindWorkerRegistered, "performer", ledger.WorkerRegistered{Type: "performer"})
	add(t, l, ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{ID: "S1", Title: "First"})
	add(t, l, ledger.KindRunStarted, "p-1", ledger.RunStarted{
		RunID: "p-1", WorkerType: "performer", SegmentID: "S1", Model: "claude-sonnet-4-5"})
	add(t, l, ledger.KindRunFinished, "p-1", ledger.RunFinished{
		RunID: "p-1", Verdict: "pass", Model: "claude-sonnet-4-5",
		Usage: ledger.Usage{InputTokens: 1_000_000}, CostMicros: 3_000_000})
	// A run still in flight has not reported usage yet, which is a different
	// fact from having reported none, and must not raise the caveat either.
	add(t, l, ledger.KindRunStarted, "p-2", ledger.RunStarted{
		RunID: "p-2", WorkerType: "performer", SegmentID: "S1", Model: "claude-sonnet-4-5"})

	fleet, err := Fleet(l, cfg, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	seg, err := Segment(l, cfg, "S1")
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"fleet": fleet, "segment": seg} {
		if strings.Contains(out, "INCOMPLETE") || strings.Contains(out, "UNMEASURED") {
			t.Errorf("the %s report flags a record with nothing missing:\n%s", name, out)
		}
		if !strings.Contains(out, "$3.00") {
			t.Errorf("the %s report should show the measured cost:\n%s", name, out)
		}
	}
	if !strings.Contains(fleet, "spend (total)   $3.00\n") {
		t.Errorf("a complete total carries no caveat:\n%s", fleet)
	}
}

func TestAnUnsetCapReportsAsUnlimited(t *testing.T) {
	l, cfg := fixture(t)
	out, err := Fleet(l, cfg, at)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no daily cap configured") {
		t.Fatal("no budget configured and budget exhausted are opposite facts")
	}
}

func TestASegmentReportShowsEveryItemAndItsState(t *testing.T) {
	l, cfg := fixture(t)
	add(t, l, ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{ID: "S1", Title: "First"})
	add(t, l, ledger.KindItemCreated, "S1-001", ledger.ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "An item", Radius: "none",
		Criteria: []string{"it works"},
	})
	add(t, l, ledger.KindItemTransitioned, "S1-001", ledger.ItemTransitioned{
		ItemID: "S1-001", From: "queued", To: "blocked",
		BlockedWhy: "waiting on a decision about the token lifetime",
	})
	out, err := Segment(l, cfg, "S1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"S1-001", "blocked", "token lifetime"} {
		if !strings.Contains(out, want) {
			t.Errorf("the segment report is missing %q", want)
		}
	}
}

// dispatched is the set of commands cmd/adlc's run() switches on.
//
// It is written out here rather than derived because the report is a different
// package from the CLI. Every `adlc <word>` this package prints has to be in
// it: an operator reading a stopped fleet's report is the least able to work
// out that the command they were handed does not exist.
var dispatched = map[string]bool{
	"help": true, "init": true, "segment": true, "item": true, "run": true,
	"gate": true, "transition": true, "lease": true, "question": true,
	"approval": true, "report": true, "ledger": true, "prompt": true,
	"dispatch": true, "serve": true, "schedule": true, "config": true,
}

var printedCommand = regexp.MustCompile(`adlc ([a-z]+)`)

func awaitingFixture(t *testing.T) string {
	t.Helper()
	l, cfg := fixture(t)
	add(t, l, ledger.KindSegmentCreated, "S1", ledger.SegmentCreated{ID: "S1", Title: "First"})
	add(t, l, ledger.KindItemCreated, "S1-001", ledger.ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "Rotate the signing key", Radius: "host",
		Criteria: []string{"the old key still verifies"},
	})
	add(t, l, ledger.KindItemTransitioned, "S1-001", ledger.ItemTransitioned{
		ItemID: "S1-001", From: "ready_for_apply", To: string(authority.StateAwaitingApproval),
	})
	add(t, l, ledger.KindApprovalRequested, "AP-1", ledger.ApprovalRequested{
		ID: "AP-1", ItemID: "S1-001", Radius: "host", PlanDigest: "d0d0d0", Summary: "rotates the key",
	})
	out, err := Fleet(l, cfg, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestTheApprovalInstructionIsACommandThatExists. This printed `adlc approve
// <item> --approver ...`, which is not a command, does not use this binary's
// flag style, and named the item rather than the approval request that
// `approval decide` actually answers.
func TestTheApprovalInstructionIsACommandThatExists(t *testing.T) {
	out := awaitingFixture(t)
	want := `adlc approval decide -id AP-1 -verdict approve -approver <you> -note "..."`
	if !strings.Contains(out, want) {
		t.Fatalf("the report should hand the operator %q\n\n%s", want, out)
	}
	if strings.Contains(out, "adlc approve ") {
		t.Error("`adlc approve` is not a command this binary dispatches")
	}
}

// TestEveryCommandTheReportPrintsIsDispatched is the general form of the same
// defect: a report is read when something is stopped, and a command that does
// not resolve sends the reader to the documentation instead of to the fix.
func TestEveryCommandTheReportPrintsIsDispatched(t *testing.T) {
	for _, m := range printedCommand.FindAllStringSubmatch(awaitingFixture(t), -1) {
		if !dispatched[m[1]] {
			t.Errorf("the report prints %q and cmd/adlc has no %q command", m[0], m[1])
		}
	}
}

func TestReportingAMissingSegmentIsAnError(t *testing.T) {
	l, cfg := fixture(t)
	if _, err := Segment(l, cfg, "nope"); err == nil {
		t.Fatal("a segment that does not exist is not an empty one")
	}
}
