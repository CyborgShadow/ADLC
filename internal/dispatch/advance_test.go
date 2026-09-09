package dispatch

import (
	"context"
	"testing"

	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// blockItem drives an item into blocked the way a run does, so the record says
// where it came from — which is what resuming depends on.
func blockItem(t *testing.T, h *harness, itemID string, from authority.State, q ledger.QuestionRaised) {
	t.Helper()
	if _, err := h.Led.Append("t", ledger.KindTransitionAdmitted, itemID, ledger.TransitionOutcome{
		ItemID: itemID, From: string(from), To: string(authority.StateBlocked),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Led.Append("t", ledger.KindItemTransitioned, itemID, ledger.ItemTransitioned{
		ItemID: itemID, From: string(from), To: string(authority.StateBlocked), Reason: "stopped on a question",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Led.Append("t", ledger.KindQuestionRaised, q.ID, q); err != nil {
		t.Fatal(err)
	}
}

// TestAnsweringTheLastQuestionResumesTheItem closes the gap the Questions page
// was advertising and not delivering: the button says "Answer and unblock", and
// answering used to append the answer and leave the item blocked forever.
func TestAnsweringTheLastQuestionResumesTheItem(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	blockItem(t, h, "S1-001", authority.StateInProgress, ledger.QuestionRaised{
		ID: "Q-1", ItemID: "S1-001", Blocking: true, Text: "which way?", Lean: "A",
	})

	// While the question stands, nothing moves. A pass that resumed an item
	// with an open blocking question would defeat the gate entirely.
	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	if it, _ := h.Led.Item("S1-001"); it.State != string(authority.StateBlocked) {
		t.Fatalf("an unanswered question must keep the item blocked, got %s", it.State)
	}

	if _, err := h.Led.Append("brandon", ledger.KindQuestionAnswered, "Q-1", ledger.QuestionAnswered{
		ID: "Q-1", Answer: "A, because it is reversible", AnsweredBy: "brandon",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateInProgress) {
		t.Fatalf("answering the last blocking question must resume the item to where it stopped, got %s", it.State)
	}
}

// TestAnItemBlockedFromNowhereIsReportedNotGuessed covers the case where the
// record does not say what the item was doing. Resuming it into an invented
// state would be worse than leaving it: it would look like progress.
func TestAnItemBlockedFromNowhereIsReportedNotGuessed(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "blocked")

	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	if it, _ := h.Led.Item("S1-001"); it.State != string(authority.StateBlocked) {
		t.Fatalf("with no recorded arrival there is nowhere to resume to, got %s", it.State)
	}
	if !h.logged("STUCK") {
		t.Error("an item nobody can place must be reported, not silently left")
	}
}

// TestRejectedWorkIsRoutedBackWhileItHasAttemptsLeft closes the other gap.
// Without it the attempt limit is decorative, because no rework ever happened
// automatically and every rejection waited for a person.
func TestRejectedWorkIsRoutedBackWhileItHasAttemptsLeft(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "rejected")

	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateInProgress) {
		t.Fatalf("a rejection with attempts left goes back to a builder, got %s", it.State)
	}
	// And the reason travels with it, so the next run knows why it is here.
	props, err := h.Led.Proposals("S1-001", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) == 0 {
		t.Fatal("the move must be on the record like every other")
	}
}

// TestReworkStopsAtTheLimitRatherThanLooping is the clean case for the same
// pass: past the budget the item stays put and genuinely does need a person.
func TestReworkStopsAtTheLimitRatherThanLooping(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "rejected")
	// Round-trip it through rework until the budget is spent, using the same
	// pass that does it in production rather than a hand-written shortcut.
	for i := 0; i < h.Cfg.Dispatch.MaxAttempts; i++ {
		if _, err := h.D.Refresh(); err != nil {
			t.Fatal(err)
		}
		if it, _ := h.Led.Item("S1-001"); it.State != string(authority.StateInProgress) {
			t.Fatalf("round %d: expected rework to in_progress, got %s", i, it.State)
		}
		if _, err := h.Led.Append("t", ledger.KindItemTransitioned, "S1-001", ledger.ItemTransitioned{
			ItemID: "S1-001", From: "validating", To: "rejected", Reason: "blocker found again",
		}); err != nil {
			t.Fatal(err)
		}
	}
	spent, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if spent.Attempts != h.Cfg.Dispatch.MaxAttempts {
		t.Fatalf("each rework must spend an attempt, or the limit refuses nothing: %d after %d rounds",
			spent.Attempts, h.Cfg.Dispatch.MaxAttempts)
	}
	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	// Past the budget it takes the edge the transition table has declared all
	// along — rejected -> blocked, "the attempt limit is reached; a person
	// decides" — instead of staying rejected with nothing proposing anything.
	// Two items sat in that dead end for a whole night, invisible to every lane
	// because the selection pass skipped anything at the limit, and escalated to
	// nobody.
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateBlocked) {
		t.Fatalf("past the attempt limit an item must reach a person, got %s", it.State)
	}
	qs, err := h.Led.Questions("S1-001", true)
	if err != nil {
		t.Fatal(err)
	}
	blocking := 0
	for _, q := range qs {
		if q.Blocking {
			blocking++
		}
	}
	if blocking != 1 {
		t.Fatalf("%d open blocking question(s); an item parked with no stated reason is indistinguishable from one nobody has got to", blocking)
	}

	// And it is asked ONCE. A pass that re-raised it every thirty seconds would
	// bury the question it exists to make visible.
	for i := 0; i < 3; i++ {
		if _, err := h.D.Refresh(); err != nil {
			t.Fatal(err)
		}
	}
	again, err := h.Led.Questions("S1-001", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(qs) {
		t.Fatalf("the escalation was raised again on a later pass: %d questions, want %d", len(again), len(qs))
	}
}

// TestTheReworkLimitDoesNotBlindEveryLane is the firing case's opposite number.
//
// The limit used to be applied to the whole item before any capability was
// considered, so an item at 3 of 3 vanished from test, judge, validate, curate,
// arbitrate and improve as well as implement. Work that had already been paid
// for could not even be reviewed.
func TestTheReworkLimitDoesNotBlindEveryLane(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	// One item mid-build and one under review, both out of attempts.
	h.item(t, "S1-001", "S1", "ui", "verifying")
	h.item(t, "S1-002", "S1", "ui", "verifying")
	for _, id := range []string{"S1-001", "S1-002"} {
		for i := 0; i < h.Cfg.Dispatch.MaxAttempts; i++ {
			if _, err := h.Led.Append("t", ledger.KindItemTransitioned, id, ledger.ItemTransitioned{
				ItemID: id, From: "verifying", To: "verifying",
				Reason: "spend an attempt", BumpAttempt: true,
			}); err != nil {
				t.Fatal(err)
			}
		}
		spent, err := h.Led.Item(id)
		if err != nil {
			t.Fatal(err)
		}
		if spent.Attempts < h.Cfg.Dispatch.MaxAttempts {
			t.Fatalf("%s is at %d attempts, so this proves nothing", id, spent.Attempts)
		}
	}
	// The first goes back to a builder — which is where the two real items were
	// stranded: in_progress at 3 of 3, seen by nothing.
	if _, err := h.Led.Append("t", ledger.KindItemTransitioned, "S1-001", ledger.ItemTransitioned{
		ItemID: "S1-001", From: "verifying", To: "in_progress", Reason: "a verification task failed",
	}); err != nil {
		t.Fatal(err)
	}

	// Firing case: no builder is dispatched, and it is said out loud rather
	// than skipped in silence.
	build, err := h.D.Candidates(Filter{Capability: config.CapImplement})
	if err != nil {
		t.Fatal(err)
	}
	if len(build) != 0 {
		t.Fatalf("%d builder candidate(s) past the rework limit; the budget refuses nothing", len(build))
	}
	if !h.logged("AT LIMIT S1-001") {
		t.Error("an item excluded by the rework limit must say so, like UNREACHABLE and HELD beside it")
	}

	// Clean case: the verification lanes can still see it, because reviewing
	// work already done spends no further attempt. Read from the lifecycle
	// rather than listed by hand, so that changing the shape of the stage
	// cannot leave this asserting about a lane nothing dispatches to.
	for _, capability := range authority.VerificationCapabilities() {
		got, cerr := h.D.Candidates(Filter{Capability: capability})
		if cerr != nil {
			t.Fatal(cerr)
		}
		if len(got) == 0 {
			t.Errorf("the %s lane cannot see an item at the rework limit; work already done became unreviewable", capability)
		}
	}
}

// TestAnImproversProposalsAreActuallyCreated closes a gap that was silent in
// the worst way: the improver prompt asks for self-improvements as work items,
// the agent produced them, they were parsed and stored in the envelope — and
// nothing ever looked at them, because item admission ran only for planning
// runs. Work that looks done and is not is the thing this system exists to
// make impossible.
func TestAnImproversProposalsAreActuallyCreated(t *testing.T) {
	ws, routing := specialists()
	ws = append(ws, config.WorkerDecl{Type: "improver", Layer: "stewardship",
		Prompt: "implementer", Capabilities: []string{config.CapImprove}})
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "merged")

	h.Run.envelope = `{"envelope_version":"1","run_id":"{{run_id}}","worker_type":"improver",
		"work_item_id":"S1-001","verdict":"pass","summary":"recorded what it taught",
		"commands_run":[],"outputs":{"work_items":[
		  {"id":"S1-900","title":"Make the refusal message name the failing check",
		   "area":"ui","blast_radius":"none",
		   "criteria":["AC-1 [exit_zero] go build ./internal/authority/",
		     "a refused transition names which check was red"]},
		  {"id":"S1-901","title":"Nice to have","area":"kernel","blast_radius":"none",
		   "criteria":["the kernel is rewritten"]}
		]},"usage":{"input_tokens":1,"output_tokens":1}}`

	if _, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapImprove}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Led.Item("S1-900"); err != nil {
		t.Fatalf("an improver's admissible proposal must be created: %v", err)
	}
	// And it faces the same rules a planner's proposals face — an area nobody
	// owns is refused however it was raised.
	if _, err := h.Led.Item("S1-901"); err == nil {
		t.Error("an item in an area nobody owns must be refused, whoever proposed it")
	}
	created, err := h.Led.Item("S1-900")
	if err != nil {
		t.Fatal(err)
	}
	// Not the deliverable that provoked it. Filing it there made it dispatchable
	// without the sign-off every other idea passes, and counted it against that
	// deliverable's open-item target — so SegmentNeedsWork returned zero and the
	// planner stopped being dispatched. A five-item plan became forty-four that
	// way and the deliverable was starved by its own byproduct.
	if created.SegmentID != selfImprovementSegmentID {
		t.Errorf("a self-improvement belongs to the fleet's own deliverable, got %q", created.SegmentID)
	}
	// The reason it exists survives the move, or nothing can get back to the run
	// that hit the problem.
	if !strings.Contains(created.Rationale, "S1-001") {
		t.Errorf("the provoking item is not named in the rationale (%q); moving the work must not lose why it was raised", created.Rationale)
	}
}

// The other half, and the one that matters: raising a self-improvement must not
// be the same act as deciding to build it.
//
// Nothing is dropped — the item exists and is on the chain, which is what the
// direct-creation path was added to fix. What changed is that it lands on a
// deliverable waiting for a person, so Candidates skips it with a stated reason
// until somebody signs it off. Work nobody asked for is exactly the work that
// needs asking about.
func TestASelfImprovementIsNotDispatchableUntilSomebodySignsItOff(t *testing.T) {
	ws, routing := specialists()
	ws = append(ws, config.WorkerDecl{Type: "improver", Layer: "stewardship",
		Prompt: "implementer", Capabilities: []string{config.CapImprove}})
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "merged")
	h.Run.envelope = `{"envelope_version":"1","run_id":"{{run_id}}","worker_type":"improver",
		"work_item_id":"S1-001","verdict":"pass","summary":"recorded what it taught",
		"commands_run":[],"outputs":{"work_items":[
		  {"id":"S1-900","title":"Make the refusal message name the failing check",
		   "area":"ui","blast_radius":"none",
		   "criteria":["AC-1 [exit_zero] go build ./internal/authority/"]}
		]},"usage":{"input_tokens":1,"output_tokens":1}}`
	if _, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapImprove}); err != nil {
		t.Fatal(err)
	}

	seg, err := h.Led.Segment(selfImprovementSegmentID)
	if err != nil {
		t.Fatalf("the fleet's own deliverable must exist once something is raised into it: %v", err)
	}
	if authority.SegmentState(seg.State).OpenForWork() {
		t.Fatalf("the fleet's own deliverable is %s, which is open for work — a self-improvement would be built without anybody agreeing to it", seg.State)
	}
	// The clean case that stops the guard passing vacuously: it is not that
	// nothing is dispatchable, it is that THIS is not. The provoking
	// deliverable's own work is still offered.
	cands, err := h.D.Candidates(Filter{Capability: config.CapImplement})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Item.ID == "S1-900" {
			t.Error("a self-improvement was offered for building before anybody signed it off")
		}
	}
}
