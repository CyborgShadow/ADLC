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
