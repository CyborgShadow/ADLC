package server

import (
	"net/http"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// signoff is the planning gate a person owns, and both of its answers.
//
// Everything else on the roadmap is done by an agent and decided by the control
// plane. This one is not, deliberately: research, decomposition and plan
// validation all cost runs, so the pipeline stops before them and waits for
// somebody to say the intent is worth pursuing.
//
// It takes a no as well as a yes. A gate with only one button is not a gate —
// it is a step, and the only way to decline is to leave the thing sitting there
// forever, which loses the decision and the reason for it. Declining is a real
// answer and it goes on the record with the same weight as agreeing.
func (s *Server) signoff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/roadmap", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	to := authority.SegmentState(strings.TrimSpace(r.FormValue("to")))
	who := orDefault(strings.TrimSpace(r.FormValue("who")), s.Actor)
	why := strings.TrimSpace(r.FormValue("why"))
	// Absent means yes, so a form posted without the field still works.
	declined := r.FormValue("verdict") == "no"

	if id == "" || !to.Known() {
		redirect(w, r, "/roadmap", "a sign-off needs a deliverable and a state this build knows", true)
		return
	}
	// Only the two steps a person owns. Anything else on the roadmap is an
	// agent's, and a hand-operated shortcut past a stage is a stage that stops
	// being run at all.
	if to != authority.SegRoadmap && to != authority.SegSignedOff {
		redirect(w, r, "/roadmap", "only accepting onto the roadmap and signing off are yours to do here", true)
		return
	}
	seg, err := s.Led.Segment(id)
	if err != nil {
		redirect(w, r, "/roadmap", err.Error(), true)
		return
	}
	from := authority.SegmentState(seg.State)
	want := map[authority.SegmentState]authority.SegmentState{
		authority.SegRoadmap:   authority.SegTheory,
		authority.SegSignedOff: authority.SegRoadmap,
	}[to]
	if from != want {
		redirect(w, r, "/roadmap", id+" is "+seg.State+", not "+string(want)+
			" — somebody moved it while this page was open; reload and look at where it is now", true)
		return
	}

	if declined {
		// A decline needs a reason, for the same reason every other human-driven
		// edge does: months later the useful record is not that somebody said no,
		// it is what they knew at the time.
		if why == "" {
			redirect(w, r, "/roadmap",
				"say why — a decision with no stated reason is indistinguishable from nobody having looked", true)
			return
		}
		if _, err := s.Led.Append(who, ledger.KindSegmentAdvanced, id, ledger.SegmentAdvanced{
			SegmentID: id, From: string(from), To: string(authority.SegPaused),
			Why: "declined by " + who + ": " + why,
		}); err != nil {
			redirect(w, r, "/roadmap", err.Error(), true)
			return
		}
		redirect(w, r, "/roadmap", id+" is parked with your reason on the record — nothing is spent on it, "+
			"and it is still here if the answer changes", false)
		return
	}

	if why == "" {
		why = string(to) + " by " + who
	}
	if _, err := s.Led.Append(who, ledger.KindSegmentAdvanced, id, ledger.SegmentAdvanced{
		SegmentID: id, From: string(from), To: string(to), Why: why,
	}); err != nil {
		redirect(w, r, "/roadmap", err.Error(), true)
		return
	}
	msg := id + " is on the roadmap — sign it off when you are ready and a researcher will pick it up"
	if to == authority.SegSignedOff {
		msg = id + " is signed off; the research lane will pick it up on its next tick"
	}
	redirect(w, r, "/roadmap", msg, false)
}

// resume brings a parked deliverable back to where it was.
//
// The pair to declining. A decision that cannot be revisited is one people
// avoid making, so they leave things sitting instead — which is the state this
// gate exists to prevent.
func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/roadmap", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	who := orDefault(strings.TrimSpace(r.FormValue("who")), s.Actor)
	why := strings.TrimSpace(r.FormValue("why"))
	seg, err := s.Led.Segment(id)
	if err != nil {
		redirect(w, r, "/roadmap", err.Error(), true)
		return
	}
	if authority.SegmentState(seg.State) != authority.SegPaused {
		redirect(w, r, "/roadmap", id+" is "+seg.State+", not parked", true)
		return
	}
	if why == "" {
		why = "resumed by " + who
	}
	if _, err := s.Led.Append(who, ledger.KindSegmentAdvanced, id, ledger.SegmentAdvanced{
		SegmentID: id, From: string(authority.SegPaused), To: string(authority.SegTheory), Why: why,
	}); err != nil {
		redirect(w, r, "/roadmap", err.Error(), true)
		return
	}
	redirect(w, r, "/roadmap", id+" is back as a theory, with the reason on the record", false)
}
