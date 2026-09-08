package server

import (
	"net/http"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// signoff moves a deliverable through the two planning steps a person owns.
//
// Everything else on the roadmap is done by an agent and decided by the control
// plane. This one is not, deliberately: research, decomposition and plan
// validation all cost runs, so the pipeline stops before them and waits for
// somebody to say the intent is worth pursuing. Making that the cheapest
// possible action — one button, before any money is spent — is the whole point;
// a gate that is expensive to pass gets routed around.
func (s *Server) signoff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/roadmap", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	to := authority.SegmentState(strings.TrimSpace(r.FormValue("to")))
	who := orDefault(strings.TrimSpace(r.FormValue("who")), s.Actor)
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
	why := strings.TrimSpace(r.FormValue("why"))
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
