package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Signing off the intent is ONE decision, and it is asked once.
//
// It used to be two: a deliverable was created as a theory, and somebody
// pressed "is this worth carrying?" and then "should the fleet start spending
// on this?" back to back, with nothing running between them and nothing new to
// read. The two states are behaviourally identical — neither dispatches,
// neither opens for work, and the only code that ever told them apart was the
// label on the page. Writing the brief IS saying it is worth carrying. The
// second click taught people to click without reading, and the click after it
// is the one that spends money.
func TestSigningOffTheIntentIsAskedOnceNotTwice(t *testing.T) {
	s := newServer(t)
	seg, err := s.Led.Segment("S1")
	if err != nil {
		t.Fatal(err)
	}
	if seg.State != "roadmap" {
		t.Fatalf("a deliverable with a brief is created on the roadmap, waiting for the one gate — got %s", seg.State)
	}

	code, loc := post(t, s, "/signoff", url.Values{
		"id": {"S1"}, "to": {"signed_off"}, "who": {"brandon"}, "why": {"support cost is worth it"},
	})
	if code != http.StatusSeeOther || strings.Contains(loc, "bad=1") {
		t.Fatalf("signing off should be allowed straight from the roadmap: %d %s", code, loc)
	}
	if seg, _ := s.Led.Segment("S1"); seg.State != "signed_off" {
		t.Fatalf("want signed_off, got %s", seg.State)
	}

	// The reason is recorded in the person's own words, not summarised.
	_, body := get(t, s, "/segment/S1")
	if !strings.Contains(body, "support cost is worth it") {
		t.Error("the stated reason must survive to the record a person reads later")
	}
}

// TestTheSignOffControlIsNotAShortcutPastTheRestOfPlanning pins the boundary.
// A hand-operated jump past research or plan validation is a stage that stops
// being run at all, and nothing on the board would say so.
func TestTheSignOffControlIsNotAShortcutPastTheRestOfPlanning(t *testing.T) {
	s := newServer(t)
	for _, to := range []string{"ready", "delivered", "planned", "not_a_state"} {
		_, loc := post(t, s, "/signoff", url.Values{"id": {"S1"}, "to": {to}})
		if !strings.Contains(loc, "bad=1") {
			t.Errorf("%q must be refused; only the two steps a person owns are here", to)
		}
	}
	if seg, _ := s.Led.Segment("S1"); seg.State != "roadmap" {
		t.Fatalf("nothing should have moved, got %s", seg.State)
	}
}

// TestSigningOffSomethingThatMovedIsRefusedWithWhere covers the stale page. Two
// people on the same dashboard is the ordinary case, not the exotic one.
func TestSigningOffSomethingThatMovedIsRefusedWithWhere(t *testing.T) {
	s := newServer(t)
	if _, loc := post(t, s, "/signoff", url.Values{"id": {"S1"}, "to": {"signed_off"}}); strings.Contains(loc, "bad=1") {
		t.Fatal("the first move should be allowed")
	}
	_, loc := post(t, s, "/signoff", url.Values{"id": {"S1"}, "to": {"roadmap"}})
	if !strings.Contains(loc, "bad=1") {
		t.Fatal("signing off twice must be refused")
	}
	if !strings.Contains(loc, "signed_off") {
		t.Errorf("the refusal should say where it actually is: %s", loc)
	}
}

// The approach gate has to show the approach.
//
// It asks "is this approach worth what it will cost?", and it was shipped
// rendering the question and nothing else — the researcher's write-up lives in
// its envelope and the page never read it. An operator was asked to weigh a
// decision they had not been shown, which is worse than having no gate at all:
// a gate nobody can answer gets clicked through, and one that gets clicked
// through has stopped catching anything. It was caught the first time the gate
// was ever used, by the person being asked.
func TestTheApproachGateShowsTheApproachItIsAskingAbout(t *testing.T) {
	s := newServer(t)
	for _, to := range []string{"roadmap", "signed_off"} {
		post(t, s, "/signoff", url.Values{
			"id": {"S1"}, "to": {to}, "who": {"brandon"}, "why": {"worth it"},
		})
	}
	// A researcher's run, with its approach in the envelope exactly as one
	// arrives from a real dispatch.
	approach := "# S1 approach\n\nOption B: build a checker first. It touches internal/ and cmd/."
	seedResearch(t, s, "S1", approach)
	advanceSegment(t, s, "S1", "signed_off", "researched")

	_, body := get(t, s, "/roadmap")
	if !strings.Contains(body, "Is this approach worth what it will cost?") {
		t.Fatal("the approach gate must ask its question")
	}
	if !strings.Contains(body, "Option B: build a checker first") {
		t.Error("the gate asks about an approach it does not show; an operator cannot answer it, so they will click through it")
	}

	// The clean case: before there is an approach to show, the gate must not
	// render an empty box implying the researcher said nothing.
	s2 := newServer(t)
	_, early := get(t, s2, "/roadmap")
	if strings.Contains(early, "The approach you are being asked about") {
		t.Error("a deliverable with no research yet must not show an empty approach block")
	}
}

// seedResearch records a researcher's run with its approach in the envelope,
// the way a real dispatch leaves one: the blob is the evidence and the run row
// points at it.
func seedResearch(t *testing.T, s *Server, segID, approach string) {
	t.Helper()
	env := fmt.Sprintf(`{"envelope_version":"1","run_id":"r-1","worker_type":"researcher",
		"verdict":"pass","summary":"an approach","commands_run":[],
		"outputs":{"notes_md":%s},"usage":{"input_tokens":1,"output_tokens":1}}`,
		mustJSON(t, approach))
	sha, err := s.Led.PutBlob(ledger.BlobEnvelope, []byte(env))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Led.Append("pm", ledger.KindRunStarted, "r-1", ledger.RunStarted{
		RunID: "r-1", WorkerType: "researcher", SegmentID: segID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Led.Append("pm", ledger.KindRunFinished, "r-1", ledger.RunFinished{
		RunID: "r-1", Verdict: "pass", EnvelopeSHA: sha,
	}); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// advanceSegment moves a deliverable the way the control plane does, so a test
// about a gate starts at the gate.
func advanceSegment(t *testing.T, s *Server, id, from, to string) {
	t.Helper()
	if _, err := s.Led.Append("pm", ledger.KindSegmentAdvanced, id, ledger.SegmentAdvanced{
		SegmentID: id, From: from, To: to, Why: "seeded by the test",
	}); err != nil {
		t.Fatal(err)
	}
}
