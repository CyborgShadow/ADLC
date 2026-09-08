package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// A run whose control plane was killed is closed and its claim released.
//
// The firing case for the whole mechanism. Before it, a killed control plane
// left the run open and the item claimed until the lease's own TTL expired an
// hour later, and every surface said "running" throughout — a stall wearing the
// costume of progress.
func TestReapClosesARunNobodyIsWaitingOn(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D

	runID := "r-orphan-1"
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: "researcher",
	}); err != nil {
		t.Fatal(err)
	}
	writeBeat(t, d, beatFile{
		RunID: runID, ItemID: "S1", LeaseKey: "plan-s1", Worker: "researcher",
		BeatMS: d.now().Add(-5 * time.Minute).UnixMilli(),
	})

	n, err := d.Reap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d runs, want 1", n)
	}
	r, err := d.Led.Run(runID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Finished() {
		t.Fatal("the orphaned run is still open, so its item stays claimed by a process that is gone")
	}
	// UNKNOWN, never a pass and never a failure: an absent heartbeat says
	// nobody will ever report this run, not that it went badly.
	if r.Verdict != "unknown" {
		t.Fatalf("a reaped run must be UNKNOWN, got %q", r.Verdict)
	}
	// And the beat file is gone, so a second pass does not report it again.
	again, err := d.Reap()
	if err != nil || again != 0 {
		t.Fatalf("a second pass reaped %d (err %v); reaping must be idempotent", again, err)
	}
}

// The clean case. A run whose heartbeat is warm is being waited on, and reaping
// it would kill work that is going fine — which is worse than the stall this
// mechanism exists to fix.
func TestReapLeavesALiveRunAlone(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D

	runID := "r-live-1"
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: "researcher",
	}); err != nil {
		t.Fatal(err)
	}
	writeBeat(t, d, beatFile{RunID: runID, BeatMS: d.now().UnixMilli()})

	n, err := d.Reap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reaped %d live runs, want 0", n)
	}
	r, _ := d.Led.Run(runID)
	if r.Finished() {
		t.Fatal("a run with a warm heartbeat was closed; the fleet just killed its own work")
	}
}

// A heartbeat is removed when the run ends, so a run that finished cleanly is
// never reaped afterwards.
func TestAFinishedRunLeavesNoHeartbeat(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	b := d.startBeat(beatFile{RunID: "r-clean-1"})
	if _, err := os.Stat(b.path); err != nil {
		t.Fatalf("the heartbeat was never written: %v", err)
	}
	b.done()
	if _, err := os.Stat(b.path); !os.IsNotExist(err) {
		t.Fatal("the heartbeat outlived its run; the reaper would close a run that ended cleanly")
	}
	// done twice must not panic: it is deferred and may also be called early.
	b.done()
}

func writeBeat(t *testing.T, d *Dispatcher, f beatFile) {
	t.Helper()
	dir := d.runsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, f.RunID+".beat"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A run that is open with NO heartbeat file at all is still reaped.
//
// This is the case the mechanism exists for and the one a file-only pass would
// miss: a control plane killed between recording the start and writing the
// first beat, and every run that was already open before heartbeats existed.
// Those would sit open forever — the exact stall being fixed.
func TestReapClosesAnOpenRunThatNeverWroteAHeartbeat(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	runID := "r-nobeat-1"
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: "researcher",
	}); err != nil {
		t.Fatal(err)
	}
	// Age it past the cutoff: a live run writes its first beat within a second.
	d.Now = func() time.Time { return time.Now().Add(10 * time.Minute) }

	n, err := d.Reap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1 — an open run with no heartbeat is nobody's", n)
	}
	r, _ := d.Led.Run(runID)
	if !r.Finished() || r.Verdict != "unknown" {
		t.Fatalf("run finished=%v verdict=%q; want closed as UNKNOWN", r.Finished(), r.Verdict)
	}
}

// The clean case for that second pass: a run that has only just started has not
// had time to write a beat, and closing it would kill work that is fine.
func TestReapLeavesAJustStartedRunAlone(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	runID := "r-fresh-1"
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: "researcher",
	}); err != nil {
		t.Fatal(err)
	}
	n, err := d.Reap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reaped %d, want 0 — a run seconds old has not had time to write a beat", n)
	}
}

// Reaping records the EVIDENCE, not only the conclusion.
//
// The verdict is UNKNOWN because nobody can say what the agent did. But why
// nobody can say is perfectly well known — the heartbeat went cold at a
// particular moment, and the agent either did or did not leave a result behind.
// An earlier version kept the conclusion and put the evidence in a log file, so
// every surface showed a bare "unknown" that somebody then had to go and chase.
func TestReapingRecordsWhyAndKeepsWhatWasLeftBehind(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D

	runID := "r-evidence-1"
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: "researcher",
	}); err != nil {
		t.Fatal(err)
	}
	// The agent got as far as writing an envelope; nobody ever read it.
	ws := t.TempDir()
	env := filepath.Join(ws, "envelope.json")
	body := []byte(`{"envelope_version":"1","status":"pass"}`)
	if err := os.WriteFile(env, body, 0o644); err != nil {
		t.Fatal(err)
	}
	writeBeat(t, d, beatFile{
		RunID: runID, Worker: "researcher", ItemID: "S1", Workspace: ws, Envelope: env,
		BeatMS: d.now().Add(-5 * time.Minute).UnixMilli(),
	})

	if _, err := d.Reap(); err != nil {
		t.Fatal(err)
	}

	ab, ok, err := d.Led.Abandonment(runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nothing on the record says why this run was closed; the reason is only in a log")
	}
	if ab.Envelope != "written" {
		t.Errorf("the agent left an envelope and the record says %q", ab.Envelope)
	}
	if ab.EnvelopeSHA == "" || !d.Led.HasBlob(ab.EnvelopeSHA) {
		t.Error("the envelope was not retained, so the only account of what the agent thought it did is gone")
	}
	if ab.Workspace != ws {
		t.Errorf("the workspace is %q; the tree is still on disk and should be findable", ab.Workspace)
	}
	if ab.ColdMS < int64(4*time.Minute/time.Millisecond) {
		t.Errorf("cold for %dms, want about five minutes", ab.ColdMS)
	}
	if !strings.Contains(ab.Why, "never admitted") {
		t.Errorf("the reason must say the envelope was not admitted, got %q", ab.Why)
	}

	// And the envelope is NOT admitted. An envelope is a claim; admitting one
	// whose checks nobody ran is the single thing this control plane exists to
	// refuse, and an agent dying does not make it safer.
	r, err := d.Led.Run(runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "unknown" {
		t.Fatalf("verdict %q — a found envelope must not become a pass", r.Verdict)
	}
	props, _ := d.Led.Proposals(runID, false, 10)
	if len(props) > 0 {
		t.Fatal("a reaped run proposed a transition; nobody ran its checks")
	}
}

// The other half of the same evidence: an agent that never reached a result.
// "Wrote an envelope nobody read" and "never got that far" are different facts
// and the record has to tell them apart.
func TestReapingSaysWhenNoEnvelopeWasEverWritten(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	runID := "r-evidence-2"
	if _, err := d.Led.Append("cli", ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: "researcher",
	}); err != nil {
		t.Fatal(err)
	}
	writeBeat(t, d, beatFile{
		RunID: runID, Worker: "researcher",
		Envelope: filepath.Join(t.TempDir(), "never-written.json"),
		BeatMS:   d.now().Add(-5 * time.Minute).UnixMilli(),
	})
	if _, err := d.Reap(); err != nil {
		t.Fatal(err)
	}
	ab, ok, _ := d.Led.Abandonment(runID)
	if !ok || ab.Envelope != "absent" {
		t.Fatalf("envelope recorded as %q, want absent", ab.Envelope)
	}
	if !strings.Contains(ab.Why, "did not reach a result") {
		t.Errorf("the reason should say the agent never got to a result, got %q", ab.Why)
	}
}
