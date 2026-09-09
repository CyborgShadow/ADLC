package dispatch

import (
	"strings"
	"testing"

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
	q, err := d.Led.Question("Q-S1-plan-loop")
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
		if x.ID == "Q-S1-plan-loop" {
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
