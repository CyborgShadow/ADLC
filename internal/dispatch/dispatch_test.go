package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	n := 0
	led.SetClock(func() time.Time { n++; return testNow.Add(time.Duration(n) * time.Second) })
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
		Log: func(s string) { h.Logs = append(h.Logs, s) },
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
	state := "researched"
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
		{Type: "verifier", Layer: "verification", Prompt: "verifier", Capabilities: []string{config.CapTest}},
		{Type: "judge", Layer: "verification", Prompt: "validator", Capabilities: []string{config.CapJudge}},
		{Type: "security", Layer: "verification", Prompt: "validator", Capabilities: []string{config.CapValidate}, Areas: []string{"auth"}},
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
// sorts first — a security reviewer and a general reviewer both hold validate,
// so the area has to decide between them.
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

	only, err := h.D.Candidates(Filter{Capability: config.CapTest})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Item.ID != "S1-003" {
		t.Fatalf("a verify lane should see only verification work, got %+v", ids(only))
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
	h.moveSegment(t, "S1", string(authority.SegResearched))
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
			"blast_radius": "none", "criteria": []string{"the Set-Cookie header carries HttpOnly and Secure"},
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

// verifyEnvelope is a verification run's report claiming a pass and nothing
// else of interest, so a test about what clears a task is not also a test about
// criteria, findings or generated items.
func verifyEnvelope(worker, itemID string) string {
	env := map[string]any{
		"envelope_version": "1",
		"run_id":           "{{run_id}}",
		"worker_type":      worker,
		"work_item_id":     itemID,
		"verdict":          "pass",
		"summary":          "ran the suite and it passed",
		"commands_run":     []any{},
		"usage":            map[string]any{"input_tokens": 10, "output_tokens": 5},
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
// The task was cleared from the envelope's verdict alone, before the gate had
// run the checks and before the authority had decided anything. A tester
// claiming a pass over a tree whose checks the gate then observed RED was
// refused — recorded, with the reason, in the same chain — and the stage
// counted its task as cleared regardless. A refusal is evidence the claim was
// wrong; a task cleared by one is not verified at all.
func TestAVerificationTaskTheGateRefusedClearsNothing(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	h.edgeCheck(config.VerdictOutputEmpty) // the gate will observe RED here
	h.Run.envelope = verifyEnvelope("verifier", "S1-001")

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapTest})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatalf("the test task was not dispatched: %s", res.Idle)
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
	if passed[config.CapTest] {
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
	h.Run.envelope = verifyEnvelope("verifier", "S1-001")

	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapTest})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatalf("the test task was not dispatched: %s", res.Idle)
	}
	if !res.Admitted {
		t.Fatalf("a claim the gate confirmed was refused as %s: %s", res.Reason, res.Detail)
	}

	passed, err := h.Led.VerificationsFor("S1-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !passed[config.CapTest] {
		t.Fatalf("the confirmed task did not clear; the stage holds %v", passed)
	}
	if n := len(verificationEvents(t, h, "S1-001")); n != 1 {
		t.Fatalf("%d verification.passed recorded for one cleared task, want exactly 1", n)
	}

	// A cleared task moves nothing on its own: two of the three are still
	// outstanding, and the item leaves when Refresh sees them all cleared.
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateVerifying) {
		t.Errorf("one cleared task moved the item to %s", it.State)
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
// Two tasks genuinely cleared and the third run's claim refused by the gate:
// the stage is two of three, so the item stays where it is. Cleared from the
// claim, it left verification as reviewed with a third of its verification
// never performed — and the refusal saying so sits in the same chain, two
// lines above the move.
func TestAStageDoesNotLeaveVerificationOnARefusedClaim(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "verifying")
	for _, cap := range []string{config.CapJudge, config.CapValidate} {
		if _, err := h.Led.Append("t", ledger.KindVerificationPassed, "S1-001",
			ledger.VerificationPassed{ItemID: "S1-001", Capability: cap,
				RunID: "r-" + cap, Round: 0}); err != nil {
			t.Fatal(err)
		}
	}

	h.edgeCheck(config.VerdictOutputEmpty) // the third run's gate observes RED
	h.Run.envelope = verifyEnvelope("verifier", "S1-001")
	res, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapTest})
	if err != nil {
		t.Fatal(err)
	}
	if res.Admitted {
		t.Fatalf("the third claim was admitted over a RED gate: %+v", res)
	}

	if _, err := h.D.Refresh(); err != nil {
		t.Fatal(err)
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != string(authority.StateVerifying) {
		t.Fatalf("the item left verification as %s on two passes and a refusal", it.State)
	}
	if n := reviewedMoves(t, h, "S1-001"); n != 0 {
		t.Fatalf("%d transition(s) to reviewed recorded while a task was still outstanding", n)
	}

	// The clean case for the same arithmetic: once the third task genuinely
	// clears, the stage completes and the item moves. Without it the assertion
	// above passes on the day nothing ever leaves verification.
	h.edgeCheck(config.VerdictExitZero)
	h.Run.envelope = verifyEnvelope("verifier", "S1-001")
	res, err = h.D.TickScoped(context.Background(), Filter{Capability: config.CapTest})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Admitted {
		t.Fatalf("the third task was refused over a GREEN gate as %s: %s", res.Reason, res.Detail)
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
