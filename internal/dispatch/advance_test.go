package dispatch

import (
	"testing"

	"github.com/CyborgShadow/ADLC/internal/authority"
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
	if it, _ := h.Led.Item("S1-001"); it.State != string(authority.StateRejected) {
		t.Fatalf("past the attempt limit an item escalates rather than looping, got %s", it.State)
	}
}
