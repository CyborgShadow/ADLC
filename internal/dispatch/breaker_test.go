package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// deadRunner is an agent that cannot start: it errors, writes no envelope, and
// prints something on the way out. It is the exact shape of the failure that
// burned 332 runs in ninety minutes.
type deadRunner struct {
	stderr string
	usage  ledger.Usage
	cost   float64
	priced bool
	calls  int
}

func (r *deadRunner) Invoke(_ context.Context, _ Invocation) (Result, error) {
	r.calls++
	return Result{
			Stderr: r.stderr, ExitCode: 127,
			Usage: r.usage, CostUSD: r.cost, CostKnown: r.priced,
		},
		fmt.Errorf("exec: agent not found")
}

func seedBuildableItems(t *testing.T, h *harness, n int) {
	t.Helper()
	h.segment(t, "S1", "seg", "", 0)
	for i := 1; i <= n; i++ {
		h.item(t, fmt.Sprintf("S1-%03d", i), "S1", "ui", "in_progress")
	}
}

// A dispatch that produced no envelope is not a verdict about the work.
//
// The firing case for the largest single defect in a 660-run ledger. 295 of its
// 297 recorded failures had an empty envelope_sha and lasted two or three
// seconds against a healthy run's five to twelve minutes: they were agents that
// never started, and every one of them told the fleet report that a change was
// broken. The refusal was not recorded at all — the path returned before
// recordRefusal — so `no_envelope` stood at zero against 276 occurrences.
func TestADispatchWithNoEnvelopeIsUnknownAndIsRecorded(t *testing.T) {
	draining.Store(false)
	defer draining.Store(false)

	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	seedBuildableItems(t, h, 1)
	dead := &deadRunner{
		stderr: "claude: command not found",
		usage:  ledger.Usage{InputTokens: 12000, OutputTokens: 900},
		cost:   0.42, priced: true,
	}
	h.D.Runner = dead

	res, err := h.D.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID == "" {
		t.Fatal("nothing was dispatched, so this proves nothing")
	}
	run, err := h.Led.Run(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// UNKNOWN, not fail. "fail" is what a verifier says about work that is
	// genuinely broken, and reusing it here invented 295 broken changes.
	if run.Verdict != "unknown" {
		t.Fatalf("a run that never produced an envelope recorded %q; that is a verdict about work nobody did", run.Verdict)
	}
	// The consumption is kept. A run that spent tokens and then died is not a
	// free run, and hardcoding an empty Usage put real agent minutes on the
	// bill at zero.
	if run.Usage.InputTokens != 12000 {
		t.Errorf("the tokens the run consumed were discarded: %+v", run.Usage)
	}

	// The refusal is on the record, which is what puts no_envelope in the fleet
	// report's reason table.
	props, err := h.Led.Proposals("", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range props {
		if p.Reason == string(authority.ReasonNoEnvelope) {
			found = true
		}
	}
	if !found {
		t.Fatal("no no_envelope refusal was recorded; the largest failure mode in the ledger is visible on no surface")
	}

	// And what the agent printed reaches the chain, because "the agents stopped
	// starting at 12:29" is only answerable if something kept what they said.
	notes, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindNoteRecorded}, res.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, n := range notes {
		if strings.Contains(string(n.Payload), "command not found") {
			kept = true
		}
	}
	if !kept {
		t.Fatal("the agent's output went only to the log, so nobody can say afterwards what broke")
	}
}

// The figure the harness itself reported is recorded beside the re-priced one,
// never instead of it — and when no runner reports one, nothing is invented.
//
// The recorded total is a reconstruction from a default price table at list
// rates. A night reported as $1,242.94 was a reconstruction, not a bill, and the
// two claims have to be separable to say so.
func TestAReportedCostIsKeptBesideThePricedOneOrNotAtAll(t *testing.T) {
	draining.Store(false)
	defer draining.Store(false)

	reported := func(t *testing.T, r *deadRunner) bool {
		t.Helper()
		ws, routing := specialists()
		h := newHarness(t, ws, routing)
		seedBuildableItems(t, h, 1)
		h.D.Runner = r
		res, err := h.D.Tick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.RunID == "" {
			t.Fatal("nothing was dispatched, so this proves nothing")
		}
		notes, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindNoteRecorded}, res.RunID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range notes {
			if strings.Contains(string(n.Payload), "harness reported") {
				return true
			}
		}
		return false
	}

	// Firing case: a runner that can say what it was billed.
	if !reported(t, &deadRunner{cost: 1.25, priced: true}) {
		t.Error("the harness's own cost figure was parsed and thrown away")
	}
	// Clean case: a runner that cannot must not leave a $0.00 behind. Absence
	// and free are opposite facts.
	if reported(t, &deadRunner{}) {
		t.Error("a cost was recorded for a runner that reported none; absence must never render as zero")
	}
}

// The clean case. A run that produced an envelope reporting a FAILURE is the
// invocation working, and must not be renamed, counted towards the breaker, or
// filed as a no_envelope refusal.
func TestAFailingEnvelopeIsStillAFailAndDoesNotTripTheBreaker(t *testing.T) {
	draining.Store(false)
	defer draining.Store(false)

	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	seedBuildableItems(t, h, maxNoEnvelopeStreak+2)
	h.Run.envelope = `{"envelope_version":"1","run_id":"{{run_id}}","worker_type":"frontend",
		"work_item_id":"S1-001","verdict":"fail","summary":"the tests did not pass",
		"commands_run":[],"outputs":{}}`

	for i := 0; i < maxNoEnvelopeStreak+2; i++ {
		if _, err := h.D.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.D.NoEnvelopeStreak(); got != 0 {
		t.Fatalf("streak is %d after %d failing-but-working runs; a breaker that trips on failing WORK stops the fleet doing its job",
			got, maxNoEnvelopeStreak+2)
	}
	if Draining() {
		t.Fatal("the fleet stopped itself because agents reported failures, which is the fleet working")
	}
	props, err := h.Led.Proposals("", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range props {
		if p.Reason == string(authority.ReasonNoEnvelope) {
			t.Fatal("a run that wrote an envelope was filed as no_envelope")
		}
	}
}

// Past the streak the fleet stops itself, through the drain path that already
// exists rather than a second way to stop.
func TestTheFleetStopsItselfWhenTheAgentsStopStarting(t *testing.T) {
	draining.Store(false)
	defer draining.Store(false)

	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	seedBuildableItems(t, h, maxNoEnvelopeStreak+2)
	h.D.Runner = &deadRunner{stderr: "boom"}

	for i := 0; i < maxNoEnvelopeStreak-1; i++ {
		if _, err := h.D.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Clean case: under the threshold the fleet keeps working. One flaky agent
	// and one killed process in a row must not stop a fleet that is fine.
	if Draining() {
		t.Fatalf("the fleet stopped after %d no-envelope dispatches, below the threshold of %d",
			maxNoEnvelopeStreak-1, maxNoEnvelopeStreak)
	}

	if _, err := h.D.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Firing case.
	if !Draining() {
		t.Fatalf("%d dispatches in a row produced no envelope and the fleet kept firing", maxNoEnvelopeStreak)
	}
	if !h.D.StopRequested() {
		t.Error("only this process is draining; another control plane on the same tree keeps firing into the same broken invocation")
	}
	if !h.logged("CIRCUIT BREAKER") {
		t.Error("the fleet stopped itself and said nothing about why")
	}
}

// backoff stretches a lane only while dispatch is producing nothing, and is
// capped so a recovered fleet is one sleep away from firing rather than an hour.
func TestBackoffStretchesOnlyWhileNothingIsComing(t *testing.T) {
	base := time.Minute
	// Clean case: a working fleet keeps its declared cadence exactly.
	if got := backoff(base, 0); got != base {
		t.Fatalf("a healthy lane was slowed to %s; the declared cadence is the cadence", got)
	}
	// Firing case.
	if got := backoff(base, 2); got != 4*base {
		t.Fatalf("backoff(2) = %s, want %s", got, 4*base)
	}
	// Capped, so a lane never goes quiet enough to look dead.
	if got := backoff(base, 40); got != 16*base {
		t.Fatalf("backoff is uncapped: a streak of 40 gave %s", got)
	}
}

// The per-segment cap could not fire at any configuration: the check passed a
// literal 0 for the segment's spend and "" for its id, so the comparison in
// spend.CheckDispatch was never true. A declared cap that cannot refuse is
// worse than no cap, because it still gets believed.
func TestThePerSegmentCapCanActuallyFire(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "spent", "", 0)
	h.segmentAt(t, "S2", "fresh", "", 0, "ready")
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	h.item(t, "S2-001", "S2", "ui", "in_progress")
	h.Run.envelope = `{"envelope_version":"1","run_id":"{{run_id}}","worker_type":"frontend",
		"work_item_id":"S1-001","verdict":"fail","summary":"no","commands_run":[],"outputs":{}}`

	// S1 has already spent; S2 has not.
	if _, err := h.Led.Append("t", ledger.KindRunStarted, "past-1", ledger.RunStarted{
		RunID: "past-1", WorkerType: "frontend", ItemID: "S1-001", SegmentID: "S1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Led.Append("t", ledger.KindRunFinished, "past-1", ledger.RunFinished{
		RunID: "past-1", Verdict: "fail", CostMicros: 9_000_000,
	}); err != nil {
		t.Fatal(err)
	}

	// Clean case first: a cap of zero means unlimited and must never read as a
	// cap of zero already exhausted.
	h.Cfg.Budget.PerSegmentMicros = 0
	got, err := h.D.Candidates(Filter{Capability: config.CapImplement})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("%d candidates with no cap declared; an undeclared cap must refuse nothing", len(got))
	}
	res, err := h.D.dispatchOneFor(t, "S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID == "" {
		t.Fatal("nothing was dispatched with no cap declared")
	}

	// Firing case: S1 is over, S2 is not, and only S1 is refused.
	h.Cfg.Budget.PerSegmentMicros = 1_000_000
	if r, derr := h.D.dispatchOneFor(t, "S1-001"); derr != nil {
		t.Fatal(derr)
	} else if r.RunID != "" {
		t.Error("a deliverable past its per-segment cap was dispatched anyway")
	}
	if !h.logged("OVER SEGMENT BUDGET S1") {
		t.Error("the refusal was silent, so an operator sees a deliverable that has simply stopped")
	}
	if r, derr := h.D.dispatchOneFor(t, "S2-001"); derr != nil {
		t.Fatal(derr)
	} else if r.RunID == "" {
		t.Error("a deliverable within its own budget was stopped by another deliverable's spend")
	}
}

// dispatchOneFor drives one named item through the ordinary dispatch path, so a
// budget test exercises the same code a lane does rather than a shortcut.
func (d *Dispatcher) dispatchOneFor(t *testing.T, itemID string) (TickResult, error) {
	t.Helper()
	cands, err := d.Candidates(Filter{})
	if err != nil {
		return TickResult{}, err
	}
	for _, c := range cands {
		if c.Item.ID != itemID {
			continue
		}
		res, _, derr := d.dispatchOne(context.Background(), c, d.now())
		return res, derr
	}
	return TickResult{}, nil
}

// Queue wait and agent runtime are different things, and the record could not
// tell them apart: run.started is appended before the concurrency slot is
// taken, so a queued run and a running one looked identical. That is half of
// why 17 concurrent agents against a declared ceiling of 8 could not be
// explained from the ledger. The other half was attribution — every run in 660
// carried the actor "cli".
func TestQueueWaitAndTheDispatchingProcessAreOnTheRecord(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	seedBuildableItems(t, h, 1)
	h.Run.envelope = `{"envelope_version":"1","run_id":"{{run_id}}","worker_type":"frontend",
		"work_item_id":"S1-001","verdict":"fail","summary":"no","commands_run":[],"outputs":{}}`

	res, err := h.D.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID == "" {
		t.Fatal("nothing was dispatched, so this proves nothing")
	}
	notes, err := h.Led.EventsOfKind([]ledger.Kind{ledger.KindNoteRecorded}, res.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	slot := false
	for _, n := range notes {
		if strings.Contains(string(n.Payload), "concurrency slot") {
			slot = true
		}
	}
	if !slot {
		t.Error("nothing records when the agent actually started, so a run queued behind the ceiling looks like a run that is working")
	}

	run, err := h.Led.Run(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Actor == "test" || !strings.Contains(run.Actor, "pid") {
		t.Errorf("the run's actor is %q; a per-process cap is not a cap, and a record that cannot name the process cannot show which one ran this", run.Actor)
	}

	// Clean case: the identity is stable for one Dispatcher and different for
	// another, which is the whole point — two instances are two ceilings.
	if h.D.processActor() != run.Actor {
		t.Errorf("the same Dispatcher reported two identities, %q then %q", run.Actor, h.D.processActor())
	}
	other := &Dispatcher{Cfg: h.Cfg, Led: h.Led, Actor: h.D.Actor}
	if other.processActor() == h.D.processActor() {
		t.Errorf("two Dispatchers share the identity %q, so two ceilings still read as one", other.processActor())
	}
}

// A run closed by the reaper keeps what it consumed. The verdict stays UNKNOWN
// — an envelope is a claim and nobody ran its checks — but the token counts are
// not a claim about the work, and discarding them made the bill get quieter
// exactly as the fleet got sicker.
func TestAReapedRunKeepsWhatItConsumed(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D

	// Firing case: the agent left an envelope before it was orphaned.
	env := filepath.Join(t.TempDir(), "envelope.json")
	write(t, env, `{"envelope_version":"1","run_id":"r-usage","worker_type":"frontend",
		"work_item_id":"S1-001","verdict":"pass","summary":"done","commands_run":[],"outputs":{},
		"usage":{"input_tokens":4321,"output_tokens":765}}`)
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, "r-usage", ledger.RunStarted{
		RunID: "r-usage", WorkerType: "frontend",
	}); err != nil {
		t.Fatal(err)
	}
	writeBeat(t, d, beatFile{RunID: "r-usage", Worker: "frontend", Envelope: env,
		BeatMS: d.now().Add(-5 * time.Minute).UnixMilli()})

	// Clean case: nothing was ever written, so nothing is claimed about it.
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, "r-silent", ledger.RunStarted{
		RunID: "r-silent", WorkerType: "frontend",
	}); err != nil {
		t.Fatal(err)
	}
	writeBeat(t, d, beatFile{RunID: "r-silent", Worker: "frontend",
		Envelope: filepath.Join(t.TempDir(), "never-written.json"),
		BeatMS:   d.now().Add(-5 * time.Minute).UnixMilli()})

	if _, err := d.Reap(); err != nil {
		t.Fatal(err)
	}
	got, err := d.Led.Run("r-usage")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "unknown" {
		t.Fatalf("a reaped run must stay UNKNOWN whatever it left behind, got %q", got.Verdict)
	}
	if got.Usage.InputTokens != 4321 {
		t.Errorf("the reaped run recorded %+v; a run that consumed tokens is not a free run", got.Usage)
	}
	quiet, err := d.Led.Run("r-silent")
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Usage.InputTokens != 0 || quiet.CostMicros != 0 {
		t.Errorf("a run that left nothing acquired usage %+v cost %d; absence must not become a number",
			quiet.Usage, quiet.CostMicros)
	}
}

// A malformed envelope is still a run that spent money. It keeps the verdict
// `fail` — an agent produced output and the control plane refused it — but it
// must not keep a cost of zero.
func TestAMalformedEnvelopeStillCostsWhatItSpent(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	seedBuildableItems(t, h, 2)
	h.Run.envelope = `{"envelope_version":"1","usage":{"input_tokens":5000,"output_tokens":250}}`

	res, err := h.D.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != string(authority.ReasonMalformedEnvelope) {
		t.Fatalf("expected a malformed-envelope refusal, got %q", res.Reason)
	}
	run, err := h.Led.Run(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Usage.InputTokens != 5000 {
		t.Errorf("a refused envelope's token counts were discarded: %+v", run.Usage)
	}

	// Clean case: an envelope that says nothing about usage stays at nothing.
	// Absence must not become a confident zero dressed up as a measurement.
	h.Run.envelope = `not json at all`
	second, err := h.D.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := h.Led.Run(second.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Usage.InputTokens != 0 {
		t.Errorf("usage was invented for an unreadable envelope: %+v", quiet.Usage)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	// The breaker's tests set a package-wide flag. Left set, every later
	// package's lanes would idle for a reason nobody could see.
	draining.Store(false)
	os.Exit(code)
}
