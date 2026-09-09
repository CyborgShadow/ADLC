package dispatch

import (
	"context"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
)

// The bytes an agent was refused for are the only account of what it thought
// it had done, and until this existed they were the one thing the record threw
// away: the malformed-envelope branch finished the run and returned before the
// PutBlob that retains every other envelope, so `adlc run envelope` on a
// refused run answered "no digest given" and nobody could see what had been
// written. Refusals are recorded, not just admissions — and a refusal whose
// evidence is gone is a refusal nobody can act on.

const malformedEnvelope = `{"envelope_version":"1","run_id":"whatever",` +
	`"worker_type":"frontend","verdict":"triumphant",` // not one of pass|fail|reject|blocked

// implementTick puts one item through one implement run with the given raw
// agent output, and returns the run id that output was written under.
func implementTick(t *testing.T, h *harness, raw string) string {
	t.Helper()
	h.Run.envelope = raw
	if _, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapImplement}); err != nil {
		t.Fatal(err)
	}
	if h.Run.calls != 1 {
		t.Fatalf("the agent was invoked %d times, want exactly 1", h.Run.calls)
	}
	return h.Run.lastInv.RunID
}

func malformedHarness(t *testing.T) *harness {
	t.Helper()
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "in_progress")
	return h
}

// TestAMalformedEnvelopesOwnBytesSurviveItsRefusal is the firing case. It
// fails against the branch that finished the run with an empty envelope
// digest, because there was then nothing to read back.
func TestAMalformedEnvelopesOwnBytesSurviveItsRefusal(t *testing.T) {
	h := malformedHarness(t)
	runID := implementTick(t, h, malformedEnvelope)

	run, err := h.Led.Run(runID)
	if err != nil {
		t.Fatalf("the refused run is not in the ledger: %v", err)
	}
	if run.EnvelopeSHA == "" {
		t.Fatal("the run records no envelope digest, so the bytes it was refused for are unreadable — `adlc run envelope` can only answer that no digest was given")
	}
	got, err := h.Led.Blob(run.EnvelopeSHA)
	if err != nil {
		t.Fatalf("the digest the run records leads to nothing: %v", err)
	}
	if string(got) != malformedEnvelope {
		t.Fatalf("what was retained is not what the agent wrote:\n got %q\nwant %q", got, malformedEnvelope)
	}
}

// TestRetainingAMalformedEnvelopeDoesNotAdmitIt is the clean case. Keeping the
// bytes is a read path and nothing else: if retention could move the verdict,
// the item or the reason, this branch would be admitting the very thing it
// refused.
func TestRetainingAMalformedEnvelopeDoesNotAdmitIt(t *testing.T) {
	h := malformedHarness(t)
	runID := implementTick(t, h, malformedEnvelope)

	run, err := h.Led.Run(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Verdict != "fail" {
		t.Errorf("a run refused for a malformed envelope must finish fail, got %q", run.Verdict)
	}

	props, err := h.Led.Proposals("S1-001", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range props {
		if p.RunID == runID && p.Reason == string(authority.ReasonMalformedEnvelope) {
			found = true
		}
	}
	if !found {
		t.Errorf("the refusal is no longer recorded as %s: %+v", authority.ReasonMalformedEnvelope, props)
	}

	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != "in_progress" {
		t.Errorf("a refused run must leave the item where it was, got %s", it.State)
	}

	// And what was stored is never read back as a proposal: every reader of an
	// envelope blob parses it, and this one still does not parse.
	body, err := h.Led.Blob(run.EnvelopeSHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := envelope.Parse(body); perr == nil {
		t.Error("the retained bytes parsed as an envelope, so this test is not exercising a malformed one at all")
	}
}

// TestAnAcceptedEnvelopeIsStillRetained keeps the pair honest: retention is
// not a marker of refusal either. If only refused runs kept their bytes the
// assertions above would pass for the wrong reason.
func TestAnAcceptedEnvelopeIsStillRetained(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "Build the thing", 2)
	h.Run.envelope = genEnvelope()

	if _, err := h.D.TickScoped(context.Background(), Filter{Capability: config.CapPlan}); err != nil {
		t.Fatal(err)
	}
	run, err := h.Led.Run(h.Run.lastInv.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Verdict != "pass" {
		t.Fatalf("a well-formed passing envelope must still finish pass, got %q", run.Verdict)
	}
	body, err := h.Led.Blob(run.EnvelopeSHA)
	if err != nil {
		t.Fatalf("an accepted run's envelope must be readable back: %v", err)
	}
	if _, perr := envelope.Parse(body); perr != nil {
		t.Errorf("an accepted run retained something that does not parse: %v", perr)
	}
	if !h.Led.HasBlob(run.EnvelopeSHA) {
		t.Error("HasBlob disagrees with Blob about the same digest")
	}
}
