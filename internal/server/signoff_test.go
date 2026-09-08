package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestSigningOffWalksTheTwoStepsAPersonOwns covers the one planning gate no
// machine passes. The fixture's deliverable has a brief, so it starts as a
// theory and stays there until somebody presses the button.
func TestSigningOffWalksTheTwoStepsAPersonOwns(t *testing.T) {
	s := newServer(t)
	seg, err := s.Led.Segment("S1")
	if err != nil {
		t.Fatal(err)
	}
	if seg.State != "theory" {
		t.Fatalf("a deliverable with a brief starts as a theory, got %s", seg.State)
	}

	code, loc := post(t, s, "/signoff", url.Values{
		"id": {"S1"}, "to": {"roadmap"}, "who": {"brandon"}, "why": {"support cost is worth it"},
	})
	if code != http.StatusSeeOther || strings.Contains(loc, "bad=1") {
		t.Fatalf("accepting onto the roadmap should be allowed: %d %s", code, loc)
	}
	if seg, _ := s.Led.Segment("S1"); seg.State != "roadmap" {
		t.Fatalf("want roadmap, got %s", seg.State)
	}

	if _, loc := post(t, s, "/signoff", url.Values{
		"id": {"S1"}, "to": {"signed_off"}, "who": {"brandon"},
	}); strings.Contains(loc, "bad=1") {
		t.Fatalf("signing off should be allowed from the roadmap: %s", loc)
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
	if seg, _ := s.Led.Segment("S1"); seg.State != "theory" {
		t.Fatalf("nothing should have moved, got %s", seg.State)
	}
}

// TestSigningOffSomethingThatMovedIsRefusedWithWhere covers the stale page. Two
// people on the same dashboard is the ordinary case, not the exotic one.
func TestSigningOffSomethingThatMovedIsRefusedWithWhere(t *testing.T) {
	s := newServer(t)
	if _, loc := post(t, s, "/signoff", url.Values{"id": {"S1"}, "to": {"roadmap"}}); strings.Contains(loc, "bad=1") {
		t.Fatal("the first move should be allowed")
	}
	_, loc := post(t, s, "/signoff", url.Values{"id": {"S1"}, "to": {"roadmap"}})
	if !strings.Contains(loc, "bad=1") {
		t.Fatal("moving it onto the roadmap twice must be refused")
	}
	if !strings.Contains(loc, "roadmap") {
		t.Errorf("the refusal should say where it actually is: %s", loc)
	}
}
