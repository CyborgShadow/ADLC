package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/lease"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

var testNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// fakeRunner stands in for an agent. Everything the dispatcher does around the
// agent — selection, identity, isolation, admission, recording — is what
// these tests are about, and none of it should need a model to exercise.
type fakeRunner struct {
	envelope string
	err      error
	calls    int
	lastInv  Invocation
}

func (f *fakeRunner) Invoke(_ context.Context, in Invocation) (Result, error) {
	f.calls++
	f.lastInv = in
	if f.err != nil {
		return Result{}, f.err
	}
	body := strings.ReplaceAll(f.envelope, "{{run_id}}", in.RunID)
	return Result{Envelope: []byte(body)}, nil
}

type harness struct {
	D    *Dispatcher
	Led  *ledger.Ledger
	Cfg  *config.Config
	Run  *fakeRunner
	Logs []string
	mu   sync.Mutex
}

func newHarness(t *testing.T, workers []config.WorkerDecl, routing map[string]string) *harness {
	t.Helper()
	dir := t.TempDir()

	agents := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(agents, "_preamble.md"), "---\nid: _preamble\n---\nSHARED POLICY. You never write the ledger.\n")
	seen := map[string]bool{}
	for _, w := range workers {
		if seen[w.Prompt] {
			continue
		}
		seen[w.Prompt] = true
		write(t, filepath.Join(agents, w.Prompt+".md"),
			fmt.Sprintf("---\nid: %s\nversion: v1\n---\nrole %s. item={{work_item_id}} brief={{brief}} needed={{needed}}\n", w.Prompt, w.Prompt))
	}

	cfg, err := config.FromChecks("t", []string{"."}, []config.Check{{
		ID: "noop", Command: []string{"go", "version"}, Verdict: config.VerdictExitZero,
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workers = workers
	cfg.Routing = routing
	cfg.Prompts = config.PromptPolicy{Dir: agents, PreambleFile: filepath.Join(agents, "_preamble.md")}
	cfg.Dispatch.Isolation = "none"
	cfg.Dispatch.MaxAttempts = 3

	lib, err := prompt.Load(cfg.Prompts)
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { led.Close() })
	// Atomic because the ledger is appended to from every lane goroutine at once
	// and this counter is what orders the events. A plain n++ here is a data
	// race, and the damage is worse than a lost tick: two appends reading the
	// same n get the same timestamp, so a test that depends on event order gets
	// an order that depends on scheduling.
	var n int64
	led.SetClock(func() time.Time {
		return testNow.Add(time.Duration(atomic.AddInt64(&n, 1)) * time.Second)
	})
	for _, w := range workers {
		if _, err := led.Append("t", ledger.KindWorkerRegistered, w.Type, ledger.WorkerRegistered{
			Type: w.Type, Layer: w.Layer, LowCadence: w.LowCadence,
		}); err != nil {
			t.Fatal(err)
		}
	}

	h := &harness{Led: led, Cfg: cfg, Run: &fakeRunner{}}
	h.D = &Dispatcher{
		Cfg: cfg, Led: led, Lib: lib,
		Leases: lease.New(filepath.Join(dir, "leases"), 60, nil),
		Runner: h.Run, Repo: dir, Actor: "test",
		Now: func() time.Time { return testNow },
		// Under the lock because lanes genuinely run at once: fireParallel starts
		// max_per_tick goroutines and every one of them calls d.log. An unguarded
		// append here is a real data race, and the race detector says so the
		// moment a concurrency test runs — but the reason it matters is quieter
		// than that. `logged` reads this slice and several tests assert on what
		// it contains, so a lost or torn append makes those tests pass and fail
		// on timing rather than on behaviour. Production is not affected: it logs
		// through fmt.Println, whose writer holds its own lock.
		Log: func(s string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.Logs = append(h.Logs, s)
		},
	}
	return h
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// segment registers a deliverable and walks it to the point where a planner
// would pick it up. The states before that are a person's to pass — a segment
// created with a brief starts as a theory and stays there until somebody signs
// it off — so a test about decomposition seeds past them rather than
// re-testing the gate on every case.
func (h *harness) segment(t *testing.T, id, title, brief string, target int) {
	t.Helper()
	// approach_agreed, not researched: a deliverable at `researched` is waiting
	// on the second human gate, where somebody reads what the approach commits
	// to before a planner turns it into work. That gate has its own tests; a
	// test about decomposition seeds past it exactly as it seeds past sign-off.
	state := "approach_agreed"
	if brief == "" {
		// No brief means no agent decomposition to research or validate; the
		// items were written by hand and open for work immediately.
		state = "ready"
	}
	h.segmentAt(t, id, title, brief, target, state)
}

func (h *harness) segmentAt(t *testing.T, id, title, brief string, target int, state string) {
	t.Helper()
	if _, err := h.Led.Append("t", ledger.KindSegmentCreated, id, ledger.SegmentCreated{
		ID: id, Title: title, Brief: brief, TargetOpen: target,
	}); err != nil {
		t.Fatal(err)
	}
	if state == "" || state == "theory" {
		return
	}
	if _, err := h.Led.Append("t", ledger.KindSegmentAdvanced, id, ledger.SegmentAdvanced{
		SegmentID: id, From: "theory", To: state, Why: "seeded by the test",
	}); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) item(t *testing.T, id, seg, area, state string) {
	t.Helper()
	if _, err := h.Led.Append("t", ledger.KindItemCreated, id, ledger.ItemCreated{
		ID: id, SegmentID: seg, Title: "item " + id, Area: area,
		Radius: "none", Criteria: []string{"it does the thing"},
	}); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		if _, err := h.Led.Append("t", ledger.KindItemTransitioned, id, ledger.ItemTransitioned{
			ItemID: id, From: "queued", To: state, RunID: "seed",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *harness) logged(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.Logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func specialists() ([]config.WorkerDecl, map[string]string) {
	workers := []config.WorkerDecl{
		{Type: "backend", Layer: "worker", Prompt: "implementer", Capabilities: []string{config.CapImplement}, Areas: []string{"api"}},
		{Type: "frontend", Layer: "worker", Prompt: "implementer", Capabilities: []string{config.CapImplement}, Areas: []string{"ui"}},
		{Type: "generalist", Layer: "worker", Prompt: "implementer", Capabilities: []string{config.CapImplement}},
		// The roster still declares a tester and a validator, deliberately. Their
		// tasks left the verification stage, and a roster that dropped them too
		// could not tell "nothing routes verification work to a tester" from
		// "there is no tester to route it to" — which is the whole question
		// TestAVerificationLaneSeesOnlyItsOwnTask asks.
		{Type: "verifier", Layer: "verification", Prompt: "verifier", Capabilities: []string{config.CapTest}},
		{Type: "judge", Layer: "verification", Prompt: "validator", Capabilities: []string{config.CapJudge}},
		{Type: "security", Layer: "verification", Prompt: "validator", Capabilities: []string{config.CapJudge}, Areas: []string{"auth"}},
		{Type: "validator", Layer: "verification", Prompt: "validator", Capabilities: []string{config.CapValidate}},
		{Type: "planner", Layer: "direction", Prompt: "generator", Capabilities: []string{config.CapPlan}},
	}
	routing := map[string]string{
		"api": "backend", "ui": "frontend", "auth": "security",
	}
	return workers, routing
}

// TestTheAreaDecidesTheWorkerNotTheAlphabet pins routing. A specialist roster
// is decorative if the picker takes whichever worker with the right capability
// sorts first — a security judge and a general judge both hold the stage's one
// verification capability, so the area has to decide between them.
func TestTheAreaDecidesTheWorkerNotTheAlphabet(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	h.item(t, "S1-002", "S1", "api", "in_progress")
	h.item(t, "S1-003", "S1", "auth", "verifying")
	h.item(t, "S1-004", "S1", "docs", "in_progress") // area nobody owns

	cands, err := h.D.Candidates(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cands {
		got[c.Item.ID] = c.Worker
	}
	for id, want := range map[string]string{
		"S1-001": "frontend",
		"S1-002": "backend",
		"S1-003": "security",
		// An area with no owner still has to reach somebody, and it goes to the
		// generalist rather than to whichever specialist sorts first.
		"S1-004": "generalist",
	} {
		if got[id] != want {
			t.Errorf("%s went to %q, want %q", id, got[id], want)
		}
	}
}

// TestVerificationIsPickedBeforeNewImplementation pins the ordering that makes
// a dark verification layer structurally impossible.
func TestVerificationIsPickedBeforeNewImplementation(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "ready")     // new work
	h.item(t, "S1-002", "S1", "ui", "verifying") // work waiting to be checked

	cands, err := h.D.Candidates(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) < 2 {
		t.Fatalf("want both items, got %d", len(cands))
	}
	if cands[0].Item.ID != "S1-002" {
		t.Fatalf("work waiting on verification must be picked first, got %s", cands[0].Item.ID)
	}
}

func TestALaneOnlySeesItsOwnWork(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	h.item(t, "S1-002", "S1", "api", "in_progress")
	h.item(t, "S1-003", "S1", "ui", "verifying")

	only, err := h.D.Candidates(Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Item.ID != "S1-003" {
		t.Fatalf("a verify lane should see only verification work, got %+v", ids(only))
	}
	// And a lane for a capability the stage no longer holds is offered nothing,
	// rather than being handed verification work under a different name. A
	// tester picking this item up would re-run, as a claim, the suite the gate
	// already executes on this very edge as an observation.
	for _, capability := range []string{config.CapTest, config.CapValidate} {
		gone, gerr := h.D.Candidates(Filter{Capability: capability})
		if gerr != nil {
			t.Fatal(gerr)
		}
		if len(gone) != 0 {
			t.Errorf("the %s lane was offered %+v; that task is the control plane's now", capability, ids(gone))
		}
	}
	byArea, err := h.D.Candidates(Filter{Areas: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byArea) != 1 || byArea[0].Item.ID != "S1-002" {
		t.Fatalf("an area lane should see only that area, got %+v", ids(byArea))
	}
}

func TestALeasedItemIsSkippedNotStolen(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")

	if _, err := h.D.Leases.Acquire(lease.Lease{Key: "S1-001", RunID: "sibling", Worker: "frontend"}); err != nil {
		t.Fatal(err)
	}
	res, err := h.D.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Dispatched {
		t.Fatal("an item held by a live sibling run must not be dispatched")
	}
	if h.Run.calls != 0 {
		t.Fatal("no agent should have been invoked")
	}
	if !strings.Contains(res.Idle, "leased") {
		t.Errorf("the idle reason should say why: %q", res.Idle)
	}
}

// --- generation -----------------------------------------------------------

func genEnvelope(items ...map[string]any) string {
	env := map[string]any{
		"envelope_version": "1",
		"run_id":           "{{run_id}}",
		"worker_type":      "planner",
		"verdict":          "pass",
		"summary":          "decomposed the brief",
		"commands_run":     []any{},
		"outputs":          map[string]any{"work_items": items},
		"usage":            map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func TestASegmentUnderItsTargetAsksForWork(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "Build the thing", 3)

	cands, err := h.D.Candidates(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Kind != KindSegment || cands[0].Need != 3 {
		t.Fatalf("want one generate candidate needing 3, got %+v", cands)
	}
	if cands[0].Worker != "planner" {
		t.Errorf("generation should route to the generate-capable worker, got %q", cands[0].Worker)
	}
	// Clean case: a deliverable whose plan has been ACCEPTED and is at its
	// target asks for nothing, so a planner on a timer cannot invent work to
	// justify its own cadence.
	//
	// The backlog is not what protects that — the state machine is. A
	// deliverable past validation is not selected for planning at all, and one
	// that has NOT got an accepted plan needs a plan whatever its backlog says.
	// Keying the guard on the backlog alone deadlocked a real deliverable: a
	// validator rejected its breakdown, it went back to researched with the
	// rejected items still counting as open work, and no planner was ever
	// allowed near the plan that had just been rejected.
	h.item(t, "S1-001", "S1", "ui", "queued")
	h.item(t, "S1-002", "S1", "ui", "queued")
	h.item(t, "S1-003", "S1", "ui", "queued")
	h.moveSegment(t, "S1", string(authority.SegReady))
	cands2, err := h.D.Candidates(Filter{Capability: config.CapPlan})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands2) != 0 {
		t.Fatalf("a deliverable with an accepted plan at its target should not ask for more, got %+v", cands2)
	}

	// Firing case: the same full backlog, but the plan was rejected and the
	// deliverable is back at researched. It needs a planner, and refusing one
	// is the deadlock.
	h.moveSegment(t, "S1", string(authority.SegApproachAgreed))
	cands3, err := h.D.Candidates(Filter{Capability: config.CapPlan})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands3) != 1 {
		t.Fatalf("a rejected breakdown must be re-plannable however many items it left behind, got %+v", cands3)
	}
}

func TestGeneratedItemsAreAdmittedOrRefusedAndBothAreRecorded(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "Build the login flow", 4)

	h.Run.envelope = genEnvelope(
		map[string]any{ // good
			"id": "S1-001", "title": "Session cookie is httponly", "area": "auth",
			"blast_radius": "none", "criteria": []string{
				// An admissible item carries at least one criterion the control
				// plane can run itself; prose alongside it is still allowed.
				"AC-1 [output_matches: HttpOnly] go run ./cmd/adlc config check",
				"the Set-Cookie header carries HttpOnly and Secure"},
		},
		map[string]any{ // no criteria — nobody could ever verify it
			"id": "S1-002", "title": "Make login nicer", "area": "ui",
			"blast_radius": "none", "criteria": []string{},
		},
		map[string]any{ // an area nobody can implement
			"id": "S1-003", "title": "Rewrite the kernel", "area": "kernel",
			"blast_radius": "none", "criteria": []string{"the kernel is rewritten and boots"},
		},
		map[string]any{ // reaches a machine but names none
			"id": "S1-004", "title": "Patch the bastions", "area": "ui",
			"blast_radius": "fleet", "criteria": []string{"every bastion reports zero pending security updates"},
		},
	)

	res, err := h.D.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatalf("generation should have been dispatched: %s", res.Idle)
	}
	if res.Created != 1 {
		t.Fatalf("exactly one of the four proposals is admissible, got %d created", res.Created)
	}
	items, err := h.Led.Items("S1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "S1-001" {
		t.Fatalf("want only S1-001 created, got %v", itemIDs(items))
	}

	// The refusals are recorded, with reasons, in the same place transition
	// refusals live — so one report section covers every refusal in the system.
	props, err := h.Led.Proposals("", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, p := range props {
		byID[p.ItemID] = p.Reason
	}
	for id, want := range map[string]string{
		"S1-002": "no_acceptance_criteria",
		"S1-003": "area_has_no_owner",
		"S1-004": "resource_not_named",
	} {
		if byID[id] != want {
			t.Errorf("%s refused as %q, want %q", id, byID[id], want)
		}
	}
}

func TestAGeneratorCannotOverwriteAnExistingItem(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "brief", 4)
	h.item(t, "S1-001", "S1", "ui", "in_progress")

	h.Run.envelope = genEnvelope(map[string]any{
		"id": "S1-001", "title": "Something else entirely", "area": "ui",
		"blast_radius": "none", "criteria": []string{"a criterion long enough to be usable"},
	})
	if _, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapPlan}); err != nil {
		t.Fatal(err)
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.Title == "Something else entirely" {
		t.Fatal("a generator overwrote a live item; two items under one id give two runs one lease key")
	}
	if !h.logged("duplicate_item_id") {
		t.Errorf("the duplicate should be refused by name, logs: %v", h.Logs)
	}
}

// --- the scheduled lanes --------------------------------------------------

func TestAnIdleLoopStillWritesATick(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	s := &Scheduler{D: h.D, Cfg: h.Cfg, Grace: 3}
	loop := config.LoopDecl{Name: "verify-lane", Enabled: true, EverySeconds: 60, Capability: config.CapTest, MaxPerTick: 1}
	h.Cfg.Loops = []config.LoopDecl{loop}

	// Nothing to do at all.
	if _, err := s.FireOnce(context.Background(), loop); err != nil {
		t.Fatal(err)
	}
	h2 := laneHealth(t, s, "verify-lane")
	if h2.Ticks != 1 {
		t.Fatalf("an idle firing must still be recorded — that record is the only evidence the lane is alive; got %+v", h2)
	}
	if h2.Dispatches != 0 {
		t.Errorf("nothing was dispatched, got %d", h2.Dispatches)
	}
	// The built-in merge lane is reported alongside the declared ones, so a
	// merge queue that quietly stopped draining is a stale row rather than a
	// row nobody renders.
	if laneHealth(t, s, MergeLaneName).EverySecond == 0 {
		t.Error("the merge queue must appear in lane health; it is the one lane nobody declares")
	}
}

// laneHealth pulls one lane out of the health report by name.
func laneHealth(t *testing.T, s *Scheduler, name string) ledger.LoopHealth {
	t.Helper()
	health, err := s.Health(testNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range health {
		if h.Loop == name {
			return h
		}
	}
	t.Fatalf("no lane called %q in %+v", name, health)
	return ledger.LoopHealth{}
}

func TestALoopThatNeverFiredIsNotSilent(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.Cfg.Loops = []config.LoopDecl{
		{Name: "never", Enabled: true, EverySeconds: 60},
		{Name: "fired", Enabled: true, EverySeconds: 60},
	}
	s := &Scheduler{D: h.D, Cfg: h.Cfg, Grace: 3}
	if _, err := s.FireOnce(context.Background(), h.Cfg.Loops[1]); err != nil {
		t.Fatal(err)
	}
	health, err := s.Health(testNow)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ledger.LoopHealth{}
	for _, x := range health {
		byName[x.Loop] = x
	}
	if byName["never"].Fresh(testNow, 3) {
		t.Fatal("a loop that has never fired must not read as fresh — this is exactly the state that hid a dead verification layer for a whole project")
	}
	if byName["never"].Ticks != 0 {
		t.Errorf("want zero ticks, got %d", byName["never"].Ticks)
	}
	if !byName["fired"].Fresh(testNow, 3) {
		t.Error("a loop that just fired should read as fresh")
	}
}

func TestALoopGoesStaleOnceItStopsFiring(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	loop := config.LoopDecl{Name: "lane", Enabled: true, EverySeconds: 60}
	h.Cfg.Loops = []config.LoopDecl{loop}
	s := &Scheduler{D: h.D, Cfg: h.Cfg, Grace: 3}
	if _, err := s.FireOnce(context.Background(), loop); err != nil {
		t.Fatal(err)
	}
	health, _ := s.Health(testNow)
	if !health[0].Fresh(testNow, 3) {
		t.Fatal("should be fresh immediately after firing")
	}
	// Three cadences later with no further tick, it is stale rather than fine.
	later := testNow.Add(10 * time.Minute)
	if health[0].Fresh(later, 3) {
		t.Fatal("a lane that stopped firing must go stale rather than reading as healthy")
	}
}

// TestAKilledRunLeavesAnUnknownNotAPass is the restart-safety property.
//
// Everything a run does is written to the ledger as it happens, so a process
// killed mid-flight leaves a run with a start and no end. That reads as
// UNKNOWN — never as a pass — and the next dispatch continues from the
// recorded state rather than from anything held in memory.
func TestAKilledRunLeavesAnUnknownNotAPass(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")

	h.Run.err = fmt.Errorf("process killed")
	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats, err := h.Led.WorkerStats("")
	if err != nil {
		t.Fatal(err)
	}
	var fe ledger.WorkerStat
	for _, s := range stats {
		if s.Type == "frontend" {
			fe = s
		}
	}
	if fe.Runs != 1 {
		t.Fatalf("the run must be recorded even though the agent died, got %d", fe.Runs)
	}
	if fe.Pass != 0 {
		t.Fatal("a run whose agent died must never count as a pass")
	}

	// And the item is untouched, so a restart picks it up again cleanly.
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != "in_progress" {
		t.Fatalf("a failed run must not move the item, got %s", it.State)
	}
	if _, err := h.D.Leases.Acquire(lease.Lease{Key: "S1-001", RunID: "next"}); err != nil {
		t.Fatalf("the dead run's lease must have been released: %v", err)
	}
}

func TestTheRunIdTheWorkspaceAndTheLeaseAreOneString(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "brief", 2)
	h.Run.envelope = genEnvelope()

	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	inv := h.Run.lastInv
	if inv.RunID == "" {
		t.Fatal("no run id reached the agent")
	}
	if !strings.Contains(inv.EnvelopePath, inv.RunID) {
		t.Errorf("the envelope path %q does not carry the run id %q; a run whose names diverge is invisible to every guard that keys on either", inv.EnvelopePath, inv.RunID)
	}
	run, err := h.Led.Run(inv.RunID)
	if err != nil {
		t.Fatalf("the run the agent was told about is not in the ledger: %v", err)
	}
	if run.PromptSHA == "" {
		t.Error("the run records no prompt pin, so nobody can say later what it was told")
	}
}

func TestTheAgentSeesTheAssembledPromptWithItsVariablesFilled(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "Harden the estate", 2)
	h.Run.envelope = genEnvelope()

	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := h.Run.lastInv.PromptText
	if !strings.Contains(got, "SHARED POLICY") {
		t.Error("the shared preamble was not assembled into the prompt")
	}
	if !strings.Contains(got, "Harden the estate") {
		t.Error("the segment brief did not reach the generator")
	}
	if strings.Contains(got, "{{") {
		t.Errorf("an unfilled placeholder reached the agent as literal text: %q", firstBrace(got))
	}
}

func ids(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key())
	}
	return out
}

func itemIDs(items []ledger.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func firstBrace(s string) string {
	i := strings.Index(s, "{{")
	if i < 0 {
		return ""
	}
	end := i + 40
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}

// moveSegment records a roadmap move, so a test can put a deliverable where it
// needs it without re-creating it.
func (h *harness) moveSegment(t *testing.T, id, to string) {
	t.Helper()
	segs, err := h.Led.Segments()
	if err != nil {
		t.Fatal(err)
	}
	from := ""
	for _, s := range segs {
		if s.ID == id {
			from = s.State
		}
	}
	if _, err := h.Led.Append("t", ledger.KindSegmentAdvanced, id, ledger.SegmentAdvanced{
		SegmentID: id, From: from, To: to, Why: "moved by the test",
	}); err != nil {
		t.Fatal(err)
	}
}

// itemAt seeds an item with a blast radius other than none, for the rules that
// turn on how far work reaches.
func (h *harness) itemAt(t *testing.T, id, seg, area, state, radius string) {
	t.Helper()
	if _, err := h.Led.Append("t", ledger.KindItemCreated, id, ledger.ItemCreated{
		ID: id, SegmentID: seg, Title: "item " + id, Area: area,
		Radius: radius, Resources: []string{"a-machine"},
		Criteria: []string{"it does the thing"},
	}); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		if _, err := h.Led.Append("t", ledger.KindItemTransitioned, id, ledger.ItemTransitioned{
			ItemID: id, From: "queued", To: state, RunID: "seed",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// questionEnvelope is a worker's report carrying questions and nothing else of
// interest, so a test about what happens to a question is not also a test about
// verdicts, gates or generated items.
func questionEnvelope(qs ...map[string]any) string {
	env := map[string]any{
		"envelope_version": "1",
		"run_id":           "{{run_id}}",
		"worker_type":      "frontend",
		"verdict":          "fail",
		"summary":          "could not decide",
		"commands_run":     []any{},
		"questions":        qs,
		"usage":            map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func questionByID(t *testing.T, h *harness, id string) ledger.Question {
	t.Helper()
	// The read path `adlc question list` uses, so what this asserts is what a
	// person running that command would see.
	qs, err := h.Led.Questions("", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if q.ID == id {
			return q
		}
	}
	var ids []string
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	t.Fatalf("no question %q in the record; it lists %v", id, ids)
	return ledger.Question{}
}

// TestAQuestionWhoseIdIsTakenIsRecordedNotDestroyed is the firing case.
//
// An agent composes the question id, and nothing makes it unique — the
// preamble's worked example handed every run the same one. `adlc_question.id`
// is a PRIMARY KEY, so the second claim on an id was refused by the chain and
// the question was then dropped with a log line: a fully-formed question, with
// text, lean and evidence, that nobody was ever asked to answer. It is now
// re-raised under the control plane's own id, which cannot collide.
func TestAQuestionWhoseIdIsTakenIsRecordedNotDestroyed(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")

	// An earlier run already took the id this run is about to claim.
	if _, err := h.Led.Append("t", ledger.KindQuestionRaised, "S1-001-Q1", ledger.QuestionRaised{
		ID: "S1-001-Q1", ItemID: "S1-001", Text: "An earlier question",
		Lean: "the earlier lean", RaisedBy: "earlier-run",
	}); err != nil {
		t.Fatal(err)
	}

	h.Run.envelope = questionEnvelope(map[string]any{
		"id": "S1-001-Q1", "blocking": false,
		"text":     "Should the id be scoped to the item or to the run?",
		"lean":     "to the run, because run ids are already unique",
		"evidence": "the preamble's example handed every run the same id",
	})
	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	runID := h.Run.lastInv.RunID
	if runID == "" {
		t.Fatal("no run was dispatched")
	}

	got := questionByID(t, h, "Q-"+runID+"-1")
	if got.Text != "Should the id be scoped to the item or to the run?" {
		t.Errorf("the agent's question did not survive the rename: %q", got.Text)
	}
	if got.Lean != "to the run, because run ids are already unique" {
		t.Errorf("the lean did not survive the rename: %q", got.Lean)
	}
	if got.Evidence != "the preamble's example handed every run the same id" {
		t.Errorf("the evidence did not survive the rename: %q", got.Evidence)
	}
	if got.ItemID != "S1-001" {
		t.Errorf("the question is not attached to the item it was raised against, got %q", got.ItemID)
	}
	if got.RaisedBy != runID {
		t.Errorf("the question does not name the run that raised it, got %q", got.RaisedBy)
	}

	// The question already on file is untouched: this recovers the second
	// question, it does not overwrite the first.
	first := questionByID(t, h, "S1-001-Q1")
	if first.Text != "An earlier question" {
		t.Errorf("the earlier question was overwritten: %q", first.Text)
	}
	if !h.logged("QUESTION ID TAKEN") {
		t.Error("the collision was recovered silently; a question filed under a name its author did not choose has to be said out loud")
	}
}

// TestAQuestionWithAFreeIdKeepsTheIdTheAgentGave is the clean case: without it
// the test above passes on the day every question is quietly renamed.
func TestAQuestionWithAFreeIdKeepsTheIdTheAgentGave(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")

	h.Run.envelope = questionEnvelope(map[string]any{
		"id": "S1-001-Q7", "blocking": true,
		"text":     "Which store backs the ledger?",
		"lean":     "sqlite",
		"evidence": "the fleet is single-host today",
	})
	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := questionByID(t, h, "S1-001-Q7")
	if got.Text != "Which store backs the ledger?" || !got.Blocking {
		t.Errorf("the question was altered on the way in: %+v", got)
	}
	qs, err := h.Led.Questions("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 {
		t.Fatalf("one question was raised; the record holds %d", len(qs))
	}
	if h.logged("QUESTION ID TAKEN") {
		t.Error("a free id was treated as a collision")
	}
}

// spendEnvelope is a worker's report whose only interesting property is what it
// says about tokens. The verdict is a fail in both spend tests below, so the
// only thing that differs between them is the usage block and neither is also a
// test about gates, transitions or generated items.
func spendEnvelope(usage map[string]any) string {
	env := map[string]any{
		"envelope_version": "1",
		"run_id":           "{{run_id}}",
		"worker_type":      "frontend",
		"verdict":          "fail",
		"summary":          "did the work and reported it",
		"commands_run":     []any{},
	}
	if usage != nil {
		env["usage"] = usage
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// pricedBudget is a real per-run cap over a model the default table prices, so
// a test asserting a run was cleared by the cap is not asserting that against a
// cap of zero, which means unlimited and clears everything.
func pricedBudget() config.Budget {
	return config.Budget{DefaultModel: "claude-sonnet-5", PerRunMicros: 1_000_000}
}

// TestARunNobodyMeasuredIsUnknownNotFree is the firing case.
//
// Pricing an unmeasured envelope at the going rate produced a confident $0.00,
// which the per-run cap then cleared, every time, for exactly the runs whose
// cost nobody could bound — so the cap was never reached however many of them
// there were. ledger.Usage.Measured is where four zeros are told from a count.
func TestARunNobodyMeasuredIsUnknownNotFree(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.Cfg.Budget = pricedBudget()
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	h.Run.envelope = spendEnvelope(nil)

	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := h.Led.Run(h.Run.lastInv.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Usage.Measured() {
		t.Fatalf("the envelope carried no usage block, so nothing was measured: %+v", run.Usage)
	}

	// The two halves of the figure, which disagree on purpose.
	recorded, forCap, unpriced := runCost(h.Cfg.Budget, "claude-sonnet-5", run.Usage)
	if forCap.Known() {
		t.Errorf("an unmeasured run's cost is UNKNOWN, not a number the cap can compare; got %s", forCap)
	}
	if v := spend.CheckRun(h.Cfg.Budget, forCap); v.OK || v.Reason != "spend_unknown" {
		t.Errorf("the per-run cap must refuse a run it cannot price, naming spend_unknown; got %+v", v)
	}
	if recorded != 0 || int64(run.CostMicros) != int64(recorded) {
		t.Errorf("the ledger's cost column is read back as money and must hold zero rather than the sentinel; recorded %d, ledger %d", recorded, run.CostMicros)
	}
	if !h.logged("spend_unknown") {
		t.Errorf("the refusal left no trace in the log: %v", h.Logs)
	}
	// The model is in the default price table, so blaming the price table here
	// would send an operator to add a price that is already there.
	if unpriced || h.logged("UNPRICED") {
		t.Errorf("a priced model was reported as having no price entry: %v", h.Logs)
	}
}

// TestAMeasuredRunUnderTheCapIsStillAdmitted is the clean case: without it the
// test above passes on the day the cap refuses every run there is.
func TestAMeasuredRunUnderTheCapIsStillAdmitted(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.Cfg.Budget = pricedBudget()
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	h.Run.envelope = spendEnvelope(map[string]any{"input_tokens": 1000, "output_tokens": 500})

	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := h.Led.Run(h.Run.lastInv.RunID)
	if err != nil {
		t.Fatal(err)
	}

	// 1000 input at $3/Mtok and 500 output at $15/Mtok, in micros.
	const want = 3_000 + 7_500
	if run.CostMicros != want {
		t.Errorf("the ledger's cost column must hold what the run actually cost; want %d, got %d", want, run.CostMicros)
	}
	if !spend.Micros(run.CostMicros).Known() {
		t.Errorf("a counted run's cost was written as the absent-cost sentinel: %d", run.CostMicros)
	}
	recorded, forCap, unpriced := runCost(h.Cfg.Budget, "claude-sonnet-5", run.Usage)
	if int64(recorded) != want || forCap != recorded {
		t.Errorf("a measured run's two figures are one number; recorded %s, for the cap %s", recorded, forCap)
	}
	if unpriced {
		t.Error("a model in the default price table was reported as unpriced")
	}
	if v := spend.CheckRun(h.Cfg.Budget, forCap); !v.OK {
		t.Errorf("a run well under the per-run cap must be admitted; got %+v", v)
	}
	if h.logged("SPEND REFUSED") {
		t.Errorf("a run under the cap was refused: %v", h.Logs)
	}
}

// --- a verification task clears on the gate's answer -----------------------

// judgeEnvelope is the verification stage's one task reporting.
//
// It carries a per-criterion verdict citing a command the envelope says was
// run, because the judge's self-edge requires exactly that: a criterion passed
// on inspection alone is not passed. The check id is deliberately not one the
// gate runs here, so the claim matcher has nothing to compare and these tests
// stay about the verdict and the gate's own observation rather than about
// claim matching, which has its own tests.
//
// The verdict is a parameter, and the pass/fail pair is not symmetric evidence.
// A judge now rules on whether the work serves the brief — a question no
// command answers — so "every criterion held" alongside "this is not what was
// asked for" is a coherent envelope, and it is the shape the fleet's actual
// failure arrives in: a coherent, well-tested deliverable nobody wanted.
func judgeEnvelope(itemID, verdict string) string {
	env := map[string]any{
		"envelope_version": "1",
		"run_id":           "{{run_id}}",
		"worker_type":      "judge",
		"work_item_id":     itemID,
		"verdict":          verdict,
		"summary":          "read the work against the brief and reported " + verdict,
		"commands_run":     []any{map[string]any{"check_id": "AC-1", "cmd": "go version", "exit_code": 0}},
		"outputs": map[string]any{"criteria": []any{map[string]any{
			"id": "AC-1", "status": "pass", "command_index": 0, "evidence": "ran it",
		}}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// judgeRejectEnvelope is the judge stopping a change: verdict reject, with the
// blocker finding the rejecting edge requires — a location, what was observed,
// and the smallest change that would clear it. Without all three a rejection is
// taste, and the edge refuses it.
func judgeRejectEnvelope(itemID string) string {
	env := map[string]any{
		"envelope_version": "1",
		"run_id":           "{{run_id}}",
		"worker_type":      "judge",
		"work_item_id":     itemID,
		"verdict":          "reject",
		"summary":          "the work does not serve the brief",
		"commands_run":     []any{},
		"outputs": map[string]any{"findings": []any{map[string]any{
			"severity": "blocker", "location": "site/cats/index.html:14",
			"evidence":        "the sources section names no licence",
			"required_change": "name the licence beside each photograph",
		}}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// edgeCheck declares one check over the verification self-edge, and decides
// what the gate will observe there. `go version` is the command either way: it
// exits 0 and prints a line, so under exit_zero the gate sees GREEN, and under
// output_empty — the rule `gofmt -l` is read with, where the output is the
// verdict — the same command is RED. One binary, present wherever these tests
// run, and no dependence on a command that fails for reasons of its own.
func (h *harness) edgeCheck(rule config.VerdictRule) {
	h.Cfg.Checks = []config.Check{{
		ID: "noop", Kind: config.KindSource, Command: []string{"go", "version"},
		Verdict: rule, RequiredFor: []string{"verifying->verifying"},
	}}
}

// verificationEvents is every verification.passed recorded against an item, so
// a test can count them rather than only ask whether one exists:
// VerificationsFor answers with a map, and a map cannot tell one append from
// three.
func verificationEvents(t *testing.T, h *harness, itemID string) []ledger.Event {
	t.Helper()
	evs, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindVerificationPassed}, itemID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// TestAVerificationTaskTheGateRefusedClearsNothing is the firing case.
//
// The task was once cleared from the envelope's verdict alone, before the gate
// had run the checks and before the authority had decided anything; the comment
// on the guard in dispatchOne records what that cost. This is the assertion
// that holds it there.
func TestAVerificationTaskTheGateRefusedClearsNothing(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	h.edgeCheck(config.VerdictOutputEmpty) // the gate will observe RED here
	h.Run.envelope = judgeEnvelope("S1-001", "pass")

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatalf("the judge task was not dispatched: %s", res.Idle)
	}
	if res.Admitted {
		t.Fatalf("a claim the gate contradicted was admitted: %+v", res)
	}
	if res.Reason != "gate_failed" {
		t.Errorf("refused as %q, want gate_failed — the checks were run here and came back RED", res.Reason)
	}

	passed, err := h.Led.VerificationsFor("S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if passed[config.CapJudge] {
		t.Fatal("a refused run cleared its verification task: the stage counts a claim the gate itself contradicted")
	}
	if len(passed) != 0 {
		t.Fatalf("the stage cleared %v on a refused run", passed)
	}
	if n := len(verificationEvents(t, h, "S1-001")); n != 0 {
		t.Fatalf("%d verification.passed recorded for a refused run", n)
	}
}

// TestAVerificationTaskTheGateConfirmsClearsExactlyOnce is the clean case:
// without it the test above passes on the day nothing clears a task at all and
// no item ever completes the stage.
func TestAVerificationTaskTheGateConfirmsClearsExactlyOnce(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	h.edgeCheck(config.VerdictExitZero) // the gate will observe GREEN here
	h.Run.envelope = judgeEnvelope("S1-001", "pass")

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatalf("the judge task was not dispatched: %s", res.Idle)
	}
	if !res.Admitted {
		t.Fatalf("a claim the gate confirmed was refused as %s: %s", res.Reason, res.Detail)
	}

	passed, err := h.Led.VerificationsFor("S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !passed[config.CapJudge] {
		t.Fatalf("the confirmed task did not clear; the stage holds %v", passed)
	}
	if n := len(verificationEvents(t, h, "S1-001")); n != 1 {
		t.Fatalf("%d verification.passed recorded for one cleared task, want exactly 1", n)
	}

	// Clearing a task and leaving the stage are two acts, and they stay two
	// acts now the stage holds one task. The run cleared its own task and moved
	// nothing; the move is Refresh's, derived from the record. Collapsing them
	// would put the destination back in the hands of whichever run reported,
	// which is the property the whole lifecycle rests on.
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateVerifying) {
		t.Errorf("the run that cleared the task also moved the item, to %s", it.State)
	}
	// And then it does move, on the pass that computes it. Without this half the
	// assertion above passes on the day nothing ever leaves verification.
	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := h.itemState(t, "S1-001"); got != string(authority.StateReviewed) {
		t.Fatalf("the stage's only task cleared and the item is still %s", got)
	}
}

// reviewedMoves counts the recorded transitions out of verification for an
// item, which is the move a stage cleared on unearned claims would make.
func reviewedMoves(t *testing.T, h *harness, itemID string) int {
	t.Helper()
	evs, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindItemTransitioned}, itemID, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		var p ledger.ItemTransitioned
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		if p.To == string(authority.StateReviewed) {
			n++
		}
	}
	return n
}

// TestAStageDoesNotLeaveVerificationOnARefusedClaim is what the two tests
// above are for, asserted where the defect was actually paid out.
//
// The stage's task claims a pass and the gate contradicts it: the task does not
// clear, so the stage has nothing cleared and the item stays where it is.
// Cleared from the claim, it left verification as reviewed with its
// verification never performed — and the refusal saying so sits in the same
// chain, two lines above the move.
//
// The stage held three tasks when this was written, and the arithmetic was
// two-of-three. Narrowing it to one made the failure worse rather than safer:
// with three, two genuine passes still sat on the record beside the false
// clear, and with one there is nothing at all between the refused claim and a
// merge.
func TestAStageDoesNotLeaveVerificationOnARefusedClaim(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")

	h.edgeCheck(config.VerdictOutputEmpty) // the run's gate observes RED
	h.Run.envelope = judgeEnvelope("S1-001", "pass")
	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if res.Admitted {
		t.Fatalf("the claim was admitted over a RED gate: %+v", res)
	}

	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateVerifying) {
		t.Fatalf("the item left verification as %s on a refused claim alone", it.State)
	}
	if n := reviewedMoves(t, h, "S1-001"); n != 0 {
		t.Fatalf("%d transition(s) to reviewed recorded while the task was still outstanding", n)
	}

	// The clean case for the same arithmetic: once the task genuinely clears,
	// the stage completes and the item moves. Without it the assertion above
	// passes on the day nothing ever leaves verification.
	h.edgeCheck(config.VerdictExitZero)
	h.Run.envelope = judgeEnvelope("S1-001", "pass")
	res, err = h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Admitted {
		t.Fatalf("the task was refused over a GREEN gate as %s: %s", res.Reason, res.Detail)
	}
	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	it, err = h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateReviewed) {
		t.Fatalf("every task cleared and the item is still %s", it.State)
	}
	if n := reviewedMoves(t, h, "S1-001"); n != 1 {
		t.Fatalf("%d transitions to reviewed for one completed stage", n)
	}
}

// TestJudgeS1023RefusedVerificationRecordsRedAndClearsNothing settles by
// observation what AC-1 otherwise leaves to inference: that the run refused in
// the firing case was refused because the control plane RAN the edge's checks
// and observed RED, and not because the check could not be run here at all —
// an UNKNOWN gate refuses too, and a test that cannot tell the two apart would
// pass on the day the declared command stopped existing.
//
// It reads the recorded gate.observed status for the run, then asserts the same
// thing the criterion turns on: no verification.passed for that item.
func TestJudgeS1023RefusedVerificationRecordsRedAndClearsNothing(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	h.edgeCheck(config.VerdictOutputEmpty)
	h.Run.envelope = judgeEnvelope("S1-001", "pass")

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched || res.RunID == "" {
		t.Fatalf("nothing was dispatched: %s", res.Idle)
	}

	evs, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindGateObserved}, res.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("%d gate.observed events for run %s, want 1", len(evs), res.RunID)
	}
	var g ledger.GateObserved
	if err := json.Unmarshal(evs[0].Payload, &g); err != nil {
		t.Fatal(err)
	}
	if g.Status != "RED" {
		t.Fatalf("the gate recorded %q on %s, not RED: the refusal this test relies on was not the checks failing (checks: %s)",
			g.Status, g.Edge, g.Checks)
	}
	if g.Edge != "verifying->verifying" {
		t.Errorf("the checks were run over edge %q, not the verification self-edge", g.Edge)
	}

	// The criterion itself: nothing the gate contradicted clears a task.
	passed, err := h.Led.VerificationsFor("S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(passed) != 0 {
		t.Fatalf("a run refused over a RED gate cleared %v", passed)
	}
	evs, err = h.Led.EventsOfKind([]ledger.Kind{ledger.KindVerificationPassed}, "S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("%d verification.passed appended for a run the gate refused", len(evs))
	}
}

// TestAFailedVerificationTaskIsRecordedOnTheClaim is the other half of the
// asymmetry the guard above rests on, and the half a merge resolution can drop
// in silence. The comment on the verification.failed append in dispatchOne
// states why that half is recorded on the claim instead; this is the assertion
// that holds it there.
//
// The case it needs is the awkward one: the gate refuses the transition off
// the back of the failing report as well, and the report still has to land.
//
// The refusal has to be the checks coming back RED. This test was first written
// declaring its check over verifying->in_progress, on the belief that a failing
// task is ejected from the stage; a setback takes the same self-edge a pass does
// (authority/advance.go), so the declared check was required for an edge nobody
// travelled and the run was refused as zero_checks_declared instead — the gate
// with nothing to run, not the gate that ran and said no. The append it guards
// was still pinned, because the run was refused either way. What went untested
// was the case the item is actually about: a gate that RAN, observed RED, and
// refused the move, with the failing report still reaching the record. So the
// reason is asserted too, and a check declared over the edge that is travelled
// is what makes it RED.
func TestAFailedVerificationTaskIsRecordedOnTheClaim(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	h.edgeCheck(config.VerdictOutputEmpty) // the gate will observe RED here
	h.Run.envelope = judgeEnvelope("S1-001", "fail")

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatalf("the judge task was not dispatched: %s", res.Idle)
	}
	if res.Admitted {
		t.Fatalf("the move out of verification was admitted over a RED gate: %+v", res)
	}
	if res.Reason != "gate_failed" {
		t.Fatalf("refused as %q, want gate_failed — this test needs the checks to have RUN and failed, "+
			"because a gate with nothing to run refuses too and proves nothing about the append below", res.Reason)
	}

	evs, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindVerificationFailed}, "S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("%d verification.failed recorded for one failing task, want exactly 1", len(evs))
	}
	var p ledger.VerificationFailed
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Capability != config.CapJudge {
		t.Errorf("the failure was recorded against %q, not the task that reported it", p.Capability)
	}
	if !strings.Contains(p.Detail, "reported fail") {
		t.Errorf("what the task observed did not reach the record: detail %q", p.Detail)
	}
	if n := len(verificationEvents(t, h, "S1-001")); n != 0 {
		t.Fatalf("%d verification.passed recorded for a task that reported fail", n)
	}
}

// TestAFailingTaskDoesNotClearItselfOverAGreenGate is the firing case for the
// verdict term on that guard, and the case the four tests above cannot see.
//
// All of them carry a claimed pass or a RED gate, so every one of them passes
// against a guard that reads the destination alone. This is the combination
// that does not: a task reporting its OWN failure over a tree whose checks are
// green. The comment on that guard in dispatchOne states why the destination
// cannot tell such a run from a cleared one, and what it costs when nothing
// else does; this is the assertion that holds the verdict term there.
//
// It is a sharper case than it was, not a softer one. When the tester held this
// edge, a green gate and a claimed failure were in plain contradiction and a
// reader could dismiss the combination as incoherent. The judge answers a
// question no command answers, so "every criterion held, the checks are green,
// and this is not what was asked for" is the ordinary shape of its failure —
// which means a guard reading the destination alone would clear the stage on
// precisely the report the stage exists to catch.
func TestAFailingTaskDoesNotClearItselfOverAGreenGate(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	h.edgeCheck(config.VerdictExitZero) // the gate will observe GREEN here
	h.Run.envelope = judgeEnvelope("S1-001", "fail")

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapJudge})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatalf("the judge task was not dispatched: %s", res.Idle)
	}
	// Admitted is the premise, not an aspiration: the setback self-edge is a
	// proposal like any other and a green gate satisfies it. If this ever stops
	// holding the test has stopped exercising the guard, so it fails loudly
	// rather than passing on a refusal that would mask the append below.
	if !res.Admitted {
		t.Fatalf("the setback self-edge was refused as %s (%s); this test needs it ADMITTED, "+
			"because a refused run proves nothing about a guard that runs after the decision",
			res.Reason, res.Detail)
	}

	passed, err := h.Led.VerificationsFor("S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if passed[config.CapJudge] {
		t.Fatal("a task that reported fail cleared itself: the stage counts a failure as a verification")
	}
	if n := len(verificationEvents(t, h, "S1-001")); n != 0 {
		t.Fatalf("%d verification.passed recorded for a task that reported fail over a green gate", n)
	}

	// The other half of the same run: the failure still has to reach the record,
	// or the guard could be satisfied by recording nothing at all.
	failed, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindVerificationFailed}, "S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 {
		t.Fatalf("%d verification.failed recorded for one failing task, want exactly 1", len(failed))
	}
}

// The scope rules in internal/authority are pure functions of facts somebody
// has to gather, and for a while nobody did: AdmitItem's own tests passed
// against hand-built facts while the dispatcher handed it empty maps, so both
// rules admitted everything in the only place they run. This test is deliberately
// at this level rather than in authority — it fails if the wiring is dropped,
// which is the failure that actually happened.
func TestTheDispatcherGivesTheScopeRulesTheFactsTheyJudgeOn(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "Build the shop under site/shop", 6)
	// An open item already holding one file, so a second claim on it collides.
	if _, err := h.Led.Append("t", ledger.KindItemCreated, "S1-001", ledger.ItemCreated{
		ID: "S1-001", SegmentID: "S1", Title: "the page", Area: "auth", Radius: "none",
		FileScope: []string{"site/shop/index.html"},
		Criteria:  []string{"AC-1 [exit_zero] go build ./..."},
	}); err != nil {
		t.Fatal(err)
	}

	h.Run.envelope = genEnvelope(
		map[string]any{ // beside the work, claimed by nobody
			"id": "S1-002", "title": "the stylesheet", "area": "auth",
			"blast_radius": "none", "file_scope": []string{"site/shop/style.css"},
			"criteria": []string{"AC-1 [exit_zero] go build ./..."},
		},
		map[string]any{ // nowhere near what this deliverable touches
			"id": "S1-003", "title": "rework the dispatcher", "area": "auth",
			"blast_radius": "none", "file_scope": []string{"internal/dispatch/dispatch.go"},
			"criteria": []string{"AC-1 [exit_zero] go build ./..."},
		},
		map[string]any{ // a file an open item already holds
			"id": "S1-004", "title": "the page again", "area": "auth",
			"blast_radius": "none", "file_scope": []string{"site/shop/index.html"},
			"criteria": []string{"AC-1 [exit_zero] go build ./..."},
		},
	)

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapPlan})
	if err != nil {
		t.Fatal(err)
	}
	props, err := h.Led.Proposals("", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, p := range props {
		byID[p.ItemID] = p.Reason
		// Printed on failure so a broken wiring names itself rather than
		// leaving somebody to guess which of the two facts went missing.
		t.Logf("REFUSED %s: %s — %s", p.ItemID, p.Reason, p.Detail)
	}
	if res.Created != 1 {
		t.Errorf("only the item beside the work is admissible, got %d created", res.Created)
	}
	if byID["S1-003"] != "scope_not_in_brief" {
		t.Errorf("an item scoped outside the deliverable was refused as %q, want scope_not_in_brief — the segment scopes never reached the rule", byID["S1-003"])
	}
	if byID["S1-004"] != "file_scope_collision" {
		t.Errorf("an item claiming an open item's file was refused as %q, want file_scope_collision — the open scopes never reached the rule", byID["S1-004"])
	}
}

// The author guard has two halves and only one of them was working.
//
// ReqIndependentVerifier refuses a verification proposal on two grounds: the
// worker's ROLE (a worker that implements may not verify) and the RUN's identity
// (the exact run that produced the work may not judge it). The second compares
// against Facts.ImplementRunID, and authority_test.go builds that fact by hand —
// so the rule was covered and the gathering of it was not. Gather looked for an
// admitted transition into `ready_for_testing`, a state the lifecycle stopped
// entering when the three checking stages became one, so the field stayed empty
// and the comparison was against "". Nothing looked broken, because the role
// half still refused the obvious cases.
//
// This test is at the dispatch level deliberately: it is the wiring that failed,
// not the rule.
func TestTheRunThatBuiltTheWorkIsIdentifiedForTheAuthorGuard(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	// The builder's own admitted move out of in_progress, which is what names
	// the run that must not be allowed to verify its own work.
	if _, err := h.Led.Append("t", ledger.KindTransitionAdmitted, "S1-001", ledger.TransitionOutcome{
		RunID: "e-built-it", ItemID: "S1-001", Worker: "frontend",
		From: string(authority.StateInProgress), To: string(authority.StateVerifying),
	}); err != nil {
		t.Fatal(err)
	}

	f, err := authority.Gather(h.Led, "S1-001", "j-1", func(string) bool { return true }, "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if f.ImplementRunID != "e-built-it" {
		t.Fatalf("the run that drove the item into verification is %q, want e-built-it — with it empty the author guard compares every verifier against nothing", f.ImplementRunID)
	}
}
func TestAPromptMergedAfterTheLibraryWasBuiltReachesTheNextRun(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	h.Run.envelope = genEnvelope()

	// The Lib the dispatcher holds was built by newHarness, before this write.
	// Written straight to the file, as a merge would: nothing tells the library
	// about it.
	path := filepath.Join(h.Cfg.Prompts.Dir, "implementer.md")
	write(t, path, "---\nid: implementer\nversion: v1\n---\nrole implementer. MERGED INSTRUCTION for {{work_item_id}}.\n")

	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := h.Run.lastInv.PromptText
	if !strings.Contains(got, "MERGED INSTRUCTION for S1-001.") {
		t.Errorf("the agent was given the prompt loaded at startup, not the merged one: %q", got)
	}
	if !strings.Contains(got, "SHARED POLICY") {
		t.Error("the shared preamble stopped being assembled above the role")
	}
}
