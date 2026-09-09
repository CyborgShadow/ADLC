package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// `adlc run envelope` is where a refusal is actually read. A run refused for a
// malformed envelope kept no copy of it, so this command answered
// "no digest given: not found" and exited non-zero for exactly the runs
// somebody most needs to see — the ones nobody could explain.

// captureStdout runs f with os.Stdout redirected, because the command writes
// the envelope to it verbatim and that is the behaviour under test.
func captureStdout(t *testing.T, f func() int) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := f()
	os.Stdout = old
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	return string(out), code
}

func TestRunEnvelopePrintsTheBytesARunWasRefusedFor(t *testing.T) {
	led, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()

	const raw = `{"envelope_version":"1","verdict":"triumphant",` // does not parse
	sha, err := led.PutBlob(ledger.BlobEnvelope, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	// The shape the dispatcher leaves behind for a malformed envelope: the run
	// finished fail, and its digest is the digest of what would not parse.
	if _, err := led.Append("t", ledger.KindRunStarted, "r-1", ledger.RunStarted{
		RunID: "r-1", WorkerType: "frontend", ItemID: "S1-001",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := led.Append("t", ledger.KindRunFinished, "r-1", ledger.RunFinished{
		RunID: "r-1", Verdict: "fail", EnvelopeSHA: sha,
	}); err != nil {
		t.Fatal(err)
	}

	e := &env{led: led, actor: "t"}
	out, code := captureStdout(t, func() int { return cmdRunStory(e, "envelope", []string{"r-1"}) })
	if code != exitOK {
		t.Errorf("exit %d, want %d — a retained envelope that cannot be printed is not retained", code, exitOK)
	}
	if out != raw {
		t.Errorf("the command printed %q, want the agent's own bytes %q", out, raw)
	}
}

// The clean case for the same command: a run that genuinely retained nothing
// still says so and does not pretend to print an envelope.
func TestRunEnvelopeStillRefusesWhenNothingWasRetained(t *testing.T) {
	led, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()

	if _, err := led.Append("t", ledger.KindRunStarted, "r-2", ledger.RunStarted{
		RunID: "r-2", WorkerType: "frontend", ItemID: "S1-001",
	}); err != nil {
		t.Fatal(err)
	}

	e := &env{led: led, actor: "t"}
	out, code := captureStdout(t, func() int { return cmdRunStory(e, "envelope", []string{"r-2"}) })
	if code == exitOK {
		t.Error("a run with no retained envelope must not report success")
	}
	if out != "" {
		t.Errorf("nothing was retained, so nothing should have been printed, got %q", out)
	}
}
