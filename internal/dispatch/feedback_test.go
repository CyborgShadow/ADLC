package dispatch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/authority"
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

// notes_md is split at every newline, and nothing joins the lines back up.
//
// The improver's prompt now says so, and this is what it says. An improver that
// hard-wraps one sentence over three lines does not record that sentence: it
// records three lessons, two of them fragments, and spends three of the eight a
// run is allowed. The prompt is only worth writing if the splitter still works
// this way, so pin both halves of it.
func TestNotesAreSplitPerLineAndNotPerSentence(t *testing.T) {
	c := Candidate{Worker: "improver", Item: ledger.Item{
		ID: "S1-001", SegmentID: "S1", Area: "ui",
	}}
	// Each call gets its own ledger, so a count is the lessons this notes_md
	// earned rather than the total of everything the test has recorded so far.
	lessons := func(runID, notes string) []ledger.Lesson {
		t.Helper()
		d := newHarness(t, nil, nil).D
		d.recordLessons(runID, c, &envelope.Envelope{
			Outputs: envelope.Outputs{Notes: notes},
		})
		got, err := d.Led.Lessons("improver", "ui", 0)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// Firing case: one sentence, hard-wrapped. Each line clears the 20-character
	// floor on its own, so all three are recorded and none of them is the rule.
	wrapped := lessons("r-wrapped",
		"A lease TTL shorter than the dispatch timeout lets a\n"+
			"second run take an item that the first one is still\n"+
			"holding, so the two of them collide over its tree.")
	if len(wrapped) != 3 {
		t.Fatalf("a sentence wrapped over three lines should record three lessons, got %d: %+v", len(wrapped), wrapped)
	}
	for _, l := range wrapped {
		if strings.Contains(l.Lesson, "\n") {
			t.Errorf("a recorded lesson carries a newline, so the split did not happen: %q", l.Lesson)
		}
	}

	// Clean case: three lessons, one line each, recorded as three. Without it
	// the assertion above passes just as well the day the splitter starts
	// cutting somewhere else entirely.
	unwrapped := lessons("r-unwrapped",
		"A lease TTL shorter than the dispatch timeout lets a second run take a held item.\n"+
			"A cached test PASS reports on an earlier tree, so CI has to run with -count=1.\n"+
			"An envelope claiming exit 0 where the gate saw exit 1 is refused as a discrepancy.")
	if len(unwrapped) != 3 {
		t.Fatalf("three single-line lessons should record exactly three, got %d: %+v", len(unwrapped), unwrapped)
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

// An unreviewed breakdown advises on source-only work and holds everything else.
//
// The end-to-end half of the rule: the authority test pins the decision, this
// pins that the dispatcher acts on it. One contested plan for a static page had
// kept twelve items unstartable across four rejections.
func TestAnUnreviewedPlanAdvisesSourceOnlyWorkAndHoldsTheRest(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	d.Cfg.Blast.PlanGateMin = config.RadiusHost

	// A deliverable whose breakdown is still under review.
	h.segmentAt(t, "S1", "seg", "Build the thing", 3, string(authority.SegValidating))
	h.item(t, "S1-001", "S1", "ui", "ready")

	cands, err := d.Candidates(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 {
		t.Fatal("source-only work was held by a breakdown still under review; the review advises here")
	}

	// The firing case: the same unreviewed breakdown still holds work that
	// reaches a machine.
	h.itemAt(t, "S1-002", "S1", "ui", "ready", string(config.RadiusHost))
	held, err := d.Candidates(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range held {
		if c.Item.ID == "S1-002" {
			t.Fatal("host-radius work started on a breakdown nobody had reviewed")
		}
	}
}

// A verification lane is offered its own task, and no lane is offered one the
// stage does not hold.
//
// The lane filter was applied before the stage was expanded, against the first
// of its capabilities — so a judge lane asked for judge work, was compared
// against test, and skipped every item in verification. It ticked 269 times and
// dispatched nothing while three items sat waiting for a judge, and every
// liveness figure stayed green the whole time.
//
// The stage held three tasks then and holds one now, so the roles that left it
// are asserted here by name rather than left out. An absence is invisible: a
// lane offered nothing looks the same as a lane nobody declared, and this is
// the surface where `test` would quietly come back.
func TestAVerificationLaneSeesOnlyItsOwnTask(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")

	for _, cap := range authority.VerificationCapabilities() {
		got, err := d.Candidates(Filter{Capability: cap})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("the %s lane saw %d candidates in verification, want 1", cap, len(got))
		}
		if got[0].Capability != cap {
			t.Errorf("the %s lane was offered %s work", cap, got[0].Capability)
		}
	}
	// The roles whose tasks left. Both workers are still on the roster, so this
	// is the routing refusing them rather than the roster lacking anybody.
	for _, cap := range []string{config.CapTest, config.CapValidate} {
		gone, err := d.Candidates(Filter{Capability: cap})
		if err != nil {
			t.Fatal(err)
		}
		if len(gone) != 0 {
			t.Errorf("the %s lane was offered verification work: %+v", cap, gone)
		}
	}

	// And a task already cleared is not offered again: the lane that did it
	// would otherwise pick the same item up for the rest of the stage.
	for _, cap := range authority.VerificationCapabilities() {
		if _, err := d.Led.Append("cli", ledger.KindVerificationPassed, "S1-001",
			ledger.VerificationPassed{ItemID: "S1-001", Capability: cap,
				RunID: "r-" + cap, Round: 0}); err != nil {
			t.Fatal(err)
		}
		done, err := d.Candidates(Filter{Capability: cap})
		if err != nil {
			t.Fatal(err)
		}
		if len(done) != 0 {
			t.Fatalf("a cleared %s task was offered again: %+v", cap, done)
		}
	}
}

// Refresh is called from every lane at once, and its moves must land once.
//
// Two concurrent passes both saw a verification stage complete, both appended
// the transition out of it, and the chain recorded a move FROM a state the item
// had already left. The item's state came out right, which is the dangerous
// part: the defect was visible only as a duplicated line in a log, and an
// append-only record cannot take a false entry back.
func TestConcurrentRefreshMovesAnItemOnce(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	for _, cap := range authority.VerificationCapabilities() {
		if _, err := d.Led.Append("cli", ledger.KindVerificationPassed, "S1-001",
			ledger.VerificationPassed{ItemID: "S1-001", Capability: cap,
				RunID: "r-" + cap, Round: 0}); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = d.Refresh()
		}()
	}
	wg.Wait()

	evs, err := d.Led.Events(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	moves := 0
	for _, e := range evs {
		if e.Kind == ledger.KindItemTransitioned && e.Subject == "S1-001" &&
			strings.Contains(string(e.Payload), string(authority.StateReviewed)) {
			moves++
		}
	}
	if moves != 1 {
		t.Fatalf("the item left verification %d times; a transition that did not happen is on the chain forever", moves)
	}
}

// A run on an item continues that item's work.
//
// Every workspace used to branch from HEAD. A judge, a tester and a validator
// each got a clean checkout of the trunk and were asked to verify work that was
// not in it, and a builder retrying after a rejection started again from
// nothing. The agents noticed before I did: the trunk carries commits titled
// "Graft S1-002's declared file scope from <sha> for the hygiene pass", which is
// a role hand-copying work into a workspace that should have contained it.
func TestAWorkspaceIsBasedOnTheItemsExistingWork(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")

	// No branch yet: the first run on an item has nothing to continue, and must
	// still get a workspace rather than failing.
	if b, err := d.branchFor("S1-001"); err != nil || b != "" {
		t.Fatalf("an item with no runs offered branch %q (%v)", b, err)
	}

	// Once a run has recorded a branch, later runs continue from it.
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, "e-1", ledger.RunStarted{
		RunID: "e-1", WorkerType: "frontend", ItemID: "S1-001", Branch: "adlc/e-1",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.branchFor("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if got != "adlc/e-1" {
		t.Fatalf("a later run would start from %q rather than the item's own work", got)
	}
}

// A refused envelope teaches the next run of that role.
//
// Without this the refusal is recorded and nobody reads it: the next run is a
// fresh agent that cannot see the ledger's refusals, so it writes the same
// shape and loses its work the same way. Three runs did exactly that in one
// night.
func TestAMalformedEnvelopeTeachesTheNextRun(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	c := Candidate{Worker: "validator", Item: ledger.Item{ID: "S1-001", Area: "ui"}}

	if got := d.lessons("validator", "ui"); got != "" {
		t.Fatalf("nothing has been learned yet, got %q", got)
	}
	d.learnFromMalformed("v-1", c, `malformed envelope: outputs.criteria[0].status "met" is not pass|fail|untested`)

	got := d.lessons("validator", "ui")
	if !strings.Contains(got, "not pass|fail|untested") {
		t.Errorf("the next validator run should be told what shape was refused, got %q", got)
	}
	if !strings.Contains(got, "discarded") {
		t.Errorf("and what it cost, got %q", got)
	}
	// A different role does not inherit it: a tester has no use for a
	// validator's mistake and every carried line is paid for in every prompt.
	if other := d.lessons("engineer", "ui"); strings.Contains(other, "not pass|fail|untested") {
		t.Errorf("a lesson must not leak across roles, got %q", other)
	}
}

// A failing verification task does not eject the item; the stage does, once
// every task has reported, and it carries what they observed back with it.
//
// Before this, a failure sent the item straight back to the builder the moment
// it landed. The stage held three tasks then, so its two siblings were still
// examining the same commit and both were refused as stale when they finished —
// two runs discarded, and the builder told one of the three things wrong with
// its work, learning the other two a whole round later.
//
// The stage holds one task now, which removes the sibling race rather than
// fixing it. What is left is the part that was never about siblings: the eject
// belongs to the control plane, computed from the record on a settled stage,
// and not to whichever run reported. Two things still turn on that and are
// asserted here — a task that has reported is not dispatched again against a
// tree that has not moved, and the round is bumped so this round's verdicts
// stop counting against the work that comes back.
func TestASettledStageSendsTheWorkBackWithWhatItObserved(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")

	if _, err := d.Led.Append("cli", ledger.KindVerificationFailed, "S1-001",
		ledger.VerificationFailed{
			ItemID: "S1-001", Capability: config.CapJudge, RunID: "j-1", Round: 0,
			Detail: "the cache key omits the locale, so the brief's second ask is unmet",
		}); err != nil {
		t.Fatal(err)
	}

	// The failed task is not offered again this round: re-dispatching it against
	// a tree that has not moved is a loop, not a retry.
	if again, _ := d.Candidates(Filter{Capability: config.CapJudge}); len(again) != 0 {
		t.Errorf("a task that already reported was offered again: %+v", again)
	}

	if _, err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := h.itemState(t, "S1-001"); got != "in_progress" {
		t.Fatalf("the settled stage did not send the work back: %s", got)
	}

	// The builder is told what the stage found, in one place.
	it, err := d.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.Attempts != 1 {
		t.Errorf("the round did not advance, so this round's verdicts would still count: %d", it.Attempts)
	}
	said := strings.Join(h.Logs, "|")
	if want := "the cache key omits the locale"; !strings.Contains(said, want) {
		t.Errorf("the stage did not carry back %q — the builder learns it a round later: %s", want, said)
	}
}

// The clean case, and the one that matters most: a stage that passed still
// leaves forward. Without it the guard above would pass on the day the stage
// stopped letting anything through at all.
func TestAStageThatAllPassesStillLeavesForward(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	for _, capability := range authority.VerificationCapabilities() {
		if _, err := d.Led.Append("cli", ledger.KindVerificationPassed, "S1-001",
			ledger.VerificationPassed{ItemID: "S1-001", Capability: capability,
				RunID: "r-" + capability, Round: 0}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := h.itemState(t, "S1-001"); got != "reviewed" {
		t.Fatalf("a fully passing stage must still clear: %s", got)
	}
}

// A blocker rejects the item, and an ordinary setback does not.
//
// A rejection is not a heavier setback: it costs the item an attempt off its
// rework budget and takes the rejected route rather than going straight back to
// the builder, so collapsing the two either spends a budget nobody meant to
// spend or loses the only verdict in the stage that stops a change.
//
// This is asserted through a real dispatch rather than by seeding events,
// because that is now the only way an item is rejected out of verification.
// Adversarial review used to be a second task in the stage, and the stage's
// settle read its recorded failure and overrode the destination; the judge is
// the stage's only task and carries the rejection itself, as a verdict, at the
// point the transition is decided.
func TestABlockerRejectsWhereAPlainSetbackDoesNot(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")

	h.Run.envelope = judgeRejectEnvelope("S1-001")
	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Admitted {
		t.Fatalf("a substantiated rejection was refused as %s: %s", res.Reason, res.Detail)
	}
	if got := h.itemState(t, "S1-001"); got != "rejected" {
		t.Fatalf("a blocker must reject the item: %s", got)
	}

	// The clean case, on a second item so the two runs do not share a record: a
	// judge that merely failed has not rejected anything. The stage settles and
	// the work goes back to the builder, which is a different route and a
	// different cost.
	h.item(t, "S1-002", "S1", "ui", "verifying")
	h.Run.envelope = judgeEnvelope("S1-002", "fail")
	if _, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := h.itemState(t, "S1-002"); got != "in_progress" {
		t.Fatalf("a setback took the rejection route: %s", got)
	}
}

func (h *harness) itemState(t *testing.T, id string) string {
	t.Helper()
	it, err := h.Led.Item(id)
	if err != nil {
		t.Fatal(err)
	}
	return it.State
}

// An answered question reaches the next run on that item.
//
// Before this, an answer unblocked the item and reached nobody: the run that
// picked it up was a fresh agent with no memory of it, so it re-derived the
// decision or asked it again. Two runs raised the same question about the same
// acceptance criterion in one night, neither able to see the other's, and both
// stopped for a person who had already answered it.
func TestAnAnsweredQuestionReachesTheNextRun(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	d := h.D
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "ready")

	// Clean case first: an item nobody has asked about carries no heading for a
	// run to reason about.
	if got := d.decisionsTaken("S1-001"); got != "" {
		t.Fatalf("nothing has been decided yet, got %q", got)
	}

	if _, err := d.Led.Append("engineer", ledger.KindQuestionRaised, "S1-001-Q1",
		ledger.QuestionRaised{ID: "S1-001-Q1", ItemID: "S1-001", Blocking: true,
			Text: "AC-3 requires go run to exit 2, which go run cannot do",
			Lean: "restate it against the built binary", RaisedBy: "engineer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Led.Append("claude", ledger.KindQuestionAnswered, "S1-001-Q1",
		ledger.QuestionAnswered{ID: "S1-001-Q1",
			Answer: "Reword it to name the built binary.", AnsweredBy: "claude"}); err != nil {
		t.Fatal(err)
	}

	got := d.decisionsTaken("S1-001")
	if !strings.Contains(got, "the built binary") {
		t.Errorf("the answer did not reach the next run: %q", got)
	}
	if !strings.Contains(got, "claude") {
		t.Errorf("a decision must name who took it: %q", got)
	}
	if !strings.Contains(got, "do not ask them again") {
		t.Errorf("a run given a settled decision must be told it is settled: %q", got)
	}

	// And it reaches the actual prompt, not just this function.
	vars := d.promptVars(Candidate{Kind: KindItem, Item: mustItem(t, d, "S1-001")}, "r-1", &Workspace{})
	if !strings.Contains(vars["what_went_wrong"], "the built binary") {
		t.Errorf("the decision never reached a prompt: %q", vars["what_went_wrong"])
	}
}

func mustItem(t *testing.T, d *Dispatcher, id string) ledger.Item {
	t.Helper()
	it, err := d.Led.Item(id)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// The count in the sentence is the number of refusals recorded, not the number
// the section prints.
//
// The list is capped at refusalsShown so the newest is not buried under an
// item's whole history; the sentence above it was counting that capped list. A
// run on its sixth refusal was told it had been refused three times — the
// display limit reported as the record, in the one figure of that section the
// run has no way to check for itself. It reads as an item going wrong less
// often than it is, which is the opposite of what this section is for.
func TestARefusalCountIsWhatWasRecordedAndNotWhatIsPrinted(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	h.segment(t, "S1", "seg", "Build the thing", 3)

	refuse := func(itemID string, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := d.Led.Append("cli", ledger.KindTransitionRefused, itemID, ledger.TransitionOutcome{
				ItemID: itemID, RunID: fmt.Sprintf("r-%s-%d", itemID, i),
				From: "in_progress", To: "ready_for_testing",
				Reason: "gate_red", Detail: fmt.Sprintf("attempt %d left the gate red", i+1),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A bullet per refusal listed; the sentence is above them and carries no
	// bullet, so this counts the list rather than the whole section.
	listed := func(s string) int { return strings.Count(s, "was refused:") }

	// Firing case: five recorded, three printed. The sentence names five.
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	refuse("S1-001", 5)
	got := d.whatWentWrong("S1-001", 5)
	if !strings.Contains(got, "refused 5 times") {
		t.Errorf("five recorded refusals are reported as something else: %q", got)
	}
	if n := listed(got); n != refusalsShown {
		t.Errorf("listed %d refusals, want the display limit of %d: %q", n, refusalsShown, got)
	}

	// Clean case: two recorded, two printed, and the sentence still agrees with
	// the list. Without it the count could be hard-wired to anything larger and
	// the assertion above would not notice.
	h.item(t, "S1-002", "S1", "ui", "in_progress")
	refuse("S1-002", 2)
	untruncated := d.whatWentWrong("S1-002", 2)
	if !strings.Contains(untruncated, "refused 2 times") {
		t.Errorf("two recorded refusals are reported as something else: %q", untruncated)
	}
	if n := listed(untruncated); n != 2 {
		t.Errorf("listed %d refusals of the 2 recorded: %q", n, untruncated)
	}
}

func TestARefusalWithNoRunRendersNoRunID(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	h.segment(t, "S1", "seg", "Build the thing", 3)
	h.item(t, "S1-001", "S1", "ui", "in_progress")

	// Firing case: exactly what the merge lane appends.
	if _, err := d.Led.Append("cli", ledger.KindTransitionRefused, "S1-001", ledger.TransitionOutcome{
		ItemID: "S1-001", From: "merging", To: "merged",
		Reason: "merge_gate_failed", Detail: "merging->merged came back RED — test (exit 1)",
	}); err != nil {
		t.Fatal(err)
	}
	got := d.whatWentWrong("S1-001", 1)
	if !strings.Contains(got, "merge_gate_failed") {
		t.Fatalf("the merge refusal is not in what the next run is told: %q", got)
	}
	if strings.Contains(got, "(run") {
		t.Errorf("a refusal no run produced was given a run id anyway: %q", got)
	}

	// Clean case: a refusal a run did produce still names it, or the fix above
	// would have removed the attribution from every refusal that has one.
	if _, err := d.Led.Append("cli", ledger.KindTransitionRefused, "S1-001", ledger.TransitionOutcome{
		ItemID: "S1-001", RunID: "e-2", From: "in_progress", To: "ready_for_testing",
		Reason: "gate_red", Detail: "two tests fail in internal/gate",
	}); err != nil {
		t.Fatal(err)
	}
	if got := d.whatWentWrong("S1-001", 2); !strings.Contains(got, "(run e-2)") {
		t.Errorf("a refusal a run produced must still name it: %q", got)
	}
}
