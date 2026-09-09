package dispatch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// A run that failed before is told what went wrong.
//
// The control plane recorded every refusal and read none of them back, so a
// builder on its third attempt got the identical prompt that produced the work
// rejected twice. The same mistake stayed available to be made again, at full
// price, for as long as the attempt limit allowed.
func TestARetryIsToldWhyTheLastAttemptWasRefused(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	h.segment(t, "S1", "seg", "Build the thing", 3)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	if _, err := d.Led.Append("cli", ledger.KindTransitionRefused, "S1-001", ledger.TransitionOutcome{
		ItemID: "S1-001", RunID: "r-1", From: "in_progress", To: "ready_for_testing",
		Reason: "gate_red", Detail: "two tests fail in internal/gate",
	}); err != nil {
		t.Fatal(err)
	}

	got := d.whatWentWrong("S1-001", 1)
	if !strings.Contains(got, "gate_red") {
		t.Errorf("the refusal reason is not in what the next run is told: %q", got)
	}
	if !strings.Contains(got, "two tests fail") {
		t.Errorf("the detail is missing, so the run knows it failed but not how: %q", got)
	}
	if !strings.Contains(got, "attempt 2") {
		t.Errorf("the run is not told which attempt it is: %q", got)
	}

	// Clean case: a first attempt has no history and is told nothing, rather
	// than being handed an empty heading to reason about.
	if fresh := d.whatWentWrong("S1-002", 0); fresh != "" {
		t.Errorf("a first attempt should carry no history, got %q", fresh)
	}
}

// A lesson an improver paid for is carried into later runs of that role.
//
// Lessons were retained, readable, and read by nothing.
func TestALessonReachesTheNextRunOfThatRole(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	if _, err := d.Led.Append("cli", ledger.KindLessonRecorded, "L-1", ledger.LessonRecorded{
		RunID: "r-9", Worker: "implementer", Area: "ui",
		Lesson: "A lease TTL shorter than the dispatch timeout lets a second run take an item the first still holds.",
	}); err != nil {
		t.Fatal(err)
	}
	got := d.lessons("implementer", "ui")
	if !strings.Contains(got, "lease TTL") {
		t.Fatalf("the lesson did not reach a later run of that role: %q", got)
	}
	if !strings.Contains(got, "r-9") {
		t.Errorf("a lesson must name the run that established it: %q", got)
	}
	// Clean case: an unrelated role is not handed somebody else's lessons, or
	// every prompt eventually costs more than the mistakes it prevents.
	if other := d.lessons("researcher", "docs"); other != "" {
		t.Errorf("an unrelated role was given the lesson: %q", other)
	}
}

// Plan and validate could pass a rejection back and forth forever. An item has
// max_attempts; a deliverable had nothing.
func TestAPlanThatKeepsBeingRejectedStopsAndAsks(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	for i, id := range []string{"v-1", "v-2"} {
		if _, err := d.Led.Append("cli", ledger.KindRunStarted, id, ledger.RunStarted{
			RunID: id, WorkerType: "validator", SegmentID: "S1",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Led.Append("cli", ledger.KindRunFinished, id, ledger.RunFinished{
			RunID: id, Verdict: "reject",
		}); err != nil {
			t.Fatal(err)
		}
		if n, looping := d.planIsLooping("S1"); looping {
			t.Fatalf("stopped after %d rejection(s); the first ones are normal rework", i+1)
		} else if n != i+1 {
			t.Fatalf("counted %d rejections, want %d", n, i+1)
		}
	}
	// The third is the one that says these two are not converging.
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, "v-3", ledger.RunStarted{
		RunID: "v-3", WorkerType: "validator", SegmentID: "S1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Led.Append("cli", ledger.KindRunFinished, "v-3", ledger.RunFinished{
		RunID: "v-3", Verdict: "reject",
	}); err != nil {
		t.Fatal(err)
	}
	n, looping := d.planIsLooping("S1")
	if !looping || n != 3 {
		t.Fatalf("n=%d looping=%v; three rejections is a disagreement, not rework", n, looping)
	}

	// And it asks a person rather than stopping silently: an item that stops
	// with no stated reason looks exactly like one nobody has got to yet.
	d.stopTheLoop(ledger.Segment{ID: "S1", Title: "seg"}, n)
	q, err := d.Led.Question(loopQuestionID("S1", 3))
	if err != nil {
		t.Fatalf("nothing was asked: %v", err)
	}
	if !q.Blocking {
		t.Error("the question must block, or the deliverable is parked invisibly")
	}
	if !strings.Contains(q.Text, "rejected 3 times") {
		t.Errorf("the question does not say what happened: %q", q.Text)
	}
	// Asked once, not once per tick.
	d.stopTheLoop(ledger.Segment{ID: "S1", Title: "seg"}, n)
	qs, _ := d.Led.Questions("", false)
	seen := 0
	for _, x := range qs {
		if x.ID == loopQuestionID("S1", 3) {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the question was raised %d times; asking again every tick is not asking harder", seen)
	}
}

// The rigour a run applies is proportional to what the work can break.
//
// Every role was written for a change that reaches production, and applied that
// to everything: a validator spent seventeen minutes, three times over, on a
// four-item plan for a static page that reaches nothing. Being thorough where
// thoroughness buys nothing is paid for in the time somebody is waiting.
func TestRigourScalesToTheBlastRadius(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D

	none := d.howMuchRigour(Candidate{Item: ledger.Item{Radius: "none"}})
	if !strings.Contains(none, "nothing here reaches a running system") {
		t.Errorf("work that breaks nothing is not told so: %q", none)
	}
	if !strings.Contains(none, "over-working it") {
		t.Error("nothing tells a run on trivial work that thoroughness has a cost")
	}

	// The firing case in the other direction: the ceremony must NOT be talked
	// down where it is the entire point.
	global := d.howMuchRigour(Candidate{Item: ledger.Item{Radius: "global"}})
	if !strings.Contains(global, "ceremony is the point") {
		t.Errorf("high-radius work was told to go easy: %q", global)
	}
	if strings.Contains(global, "over-working") {
		t.Error("a global-radius change was told it might be over-working")
	}

	// A deliverable takes the widest radius of the work under it, because the
	// plan for a set of items is as consequential as its worst item.
	h.segment(t, "S2", "seg", "brief", 3)
	h.item(t, "S2-001", "S2", "ui", "queued")
	seg := d.howMuchRigour(Candidate{Kind: KindSegment, Segment: ledger.Segment{ID: "S2"}})
	if seg == "" {
		t.Fatal("a deliverable was given no sense of what its work can break")
	}
}

// Answering the question releases the deliverable.
//
// The first version asked once and went on refusing to dispatch, because the
// rejection count only ever climbs. A person answered and the fleet stayed
// stopped — which is the definition of stuck, and worse than never asking.
func TestAnsweringThePlanLoopQuestionUnblocksTheDeliverable(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	for i := 0; i < maxPlanRejections; i++ {
		id := fmt.Sprintf("v-%d", i)
		if _, err := d.Led.Append("cli", ledger.KindRunStarted, id, ledger.RunStarted{
			RunID: id, WorkerType: "validator", SegmentID: "S1",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Led.Append("cli", ledger.KindRunFinished, id, ledger.RunFinished{
			RunID: id, Verdict: "reject",
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, looping := d.planIsLooping("S1")
	if !looping {
		t.Fatal("the fleet did not stop after the threshold")
	}
	d.stopTheLoop(ledger.Segment{ID: "S1", Title: "seg"}, n)

	// A person answers it.
	id := loopQuestionID("S1", maxPlanRejections)
	if _, err := d.Led.Append("brandon", ledger.KindQuestionAnswered, id, ledger.QuestionAnswered{
		ID: id, Answer: "try once more, the objections are getting smaller", AnsweredBy: "brandon",
	}); err != nil {
		t.Fatal(err)
	}
	if _, stillLooping := d.planIsLooping("S1"); stillLooping {
		t.Fatal("answering the question did not release the deliverable; the stop is a dead end")
	}
}

// An answered question releases the work it was blocking. Always, whichever
// kind of question it was.
//
// This kept coming back wearing different clothes: a question raised with no
// text so nobody could read what they were answering, a stop whose counter only
// climbed so answering could never clear it, and a page that reloaded away the
// answer as it was typed. Three different defects, one symptom — a question
// somebody answered and nothing moved.
//
// So the invariant is asserted directly, on both kinds of block there are: an
// item held by its own blocking question, and a deliverable held by the
// plan-loop stop. If either can be answered and still not dispatch, this fails.
func TestAnAnsweredQuestionAlwaysReleasesTheWork(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "ready")

	// The fixture has to be dispatchable BEFORE the question, or the first
	// assertion below passes for the wrong reason and the test proves nothing.
	if base, err := d.Candidates(Filter{}); err != nil || len(base) == 0 {
		t.Fatalf("the item is not dispatchable even with no question; this test would pass vacuously (err %v)", err)
	}

	if _, err := d.Led.Append("cli", ledger.KindQuestionRaised, "Q-block", ledger.QuestionRaised{
		ID: "Q-block", ItemID: "S1-001", Blocking: true, Text: "which way?", RaisedBy: "r-1",
	}); err != nil {
		t.Fatal(err)
	}
	cands, err := d.Candidates(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("an item with an unanswered blocking question was dispatched anyway: %+v", ids(cands))
	}

	if _, err := d.Led.Append("brandon", ledger.KindQuestionAnswered, "Q-block", ledger.QuestionAnswered{
		ID: "Q-block", Answer: "go left", AnsweredBy: "brandon",
	}); err != nil {
		t.Fatal(err)
	}
	after, err := d.Candidates(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 {
		t.Fatal("the question was answered and the item still cannot be picked up")
	}

	// And the answer has to reach the lanes without waiting for a timer, or
	// "resumed" means "resumed in a few minutes".
	if !wakesLanes(ledger.KindQuestionAnswered) {
		t.Error("answering a question does not wake the lanes, so work resumes on a timer instead")
	}
}

// A rejected plan teaches the fleet something, immediately.
//
// Lessons only ever came from an improver, and an improver runs after an item
// MERGES — so a deliverable that never got past planning could be rejected four
// times and the fifth planner would start with exactly what the first one had.
// The system got no smarter as it got more expensive, and the only escalation
// left was to stop and ask a person.
func TestARejectionBecomesALessonForTheNextPlanner(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	planner, ok := d.Cfg.OwnerFor("", config.CapPlan)
	if !ok {
		t.Skip("no planning worker declared in this fixture")
	}

	env := &envelope.Envelope{Verdict: "reject",
		Summary: "Every criterion is prose, so nothing can be checked by a command."}
	d.learnFromRejection("v-1", Candidate{Kind: KindSegment,
		Segment: ledger.Segment{ID: "S1"}, Worker: "validator"}, env)

	got := d.lessons(planner, "")
	if !strings.Contains(got, "Every criterion is prose") {
		t.Fatalf("the objection did not reach the next planner: %q", got)
	}
	// Filed against the role that has to ACT on it. A lesson filed against the
	// validator would be read by the one agent that already knows.
	if strings.Contains(d.lessons("validator", ""), "Every criterion is prose") {
		t.Error("the lesson was filed against the role that raised it, not the role that must act on it")
	}

	// The clean case: a rejection with nothing written down teaches nothing,
	// and must not fill the prompt with an empty rule.
	before := len(d.lessons(planner, ""))
	d.learnFromRejection("v-2", Candidate{Kind: KindSegment,
		Segment: ledger.Segment{ID: "S1"}}, &envelope.Envelope{Verdict: "reject"})
	if len(d.lessons(planner, "")) != before {
		t.Error("a rejection that said nothing was recorded as a lesson anyway")
	}
}
