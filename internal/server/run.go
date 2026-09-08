package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/story"
)

// unregisteredRun is an id the record refers to that was never registered.
//
// It is the same shape of problem as an item id that was proposed and refused:
// the id is real — it appears on transitions, on questions, on gate results —
// but nothing ever appended a run.started for it, so there is no run to show.
// Returning a 500 for that is wrong twice over. It is not a server error, and
// it hides something worth knowing: a proposal from a run nobody registered has
// no prompt pin, no base commit and no attribution behind it, which is exactly
// the defect `run_not_started` exists to refuse today.
type unregisteredRun struct {
	RunID string
	// Mentions are the events that name this run, so the page can show what it
	// was involved in even though the run itself was never recorded.
	Mentions []runMention
	Why      string
}

type runMention struct {
	Seq     int64
	Kind    string
	Subject string
	Says    string
	When    string
	Link    string
}

// runPage carries whichever of the two shapes this id turned out to be.
//
// One page name, two body types, and the fragments have to tell them apart. An
// earlier version did it by asking whether a field existed — {{with .Mentions}}
// — and that is not a type test: on the OTHER body the field is simply absent,
// which is a template execution error, and the page executes straight into the
// ResponseWriter. So the error arrived after the headers and half the HTML were
// already on the wire: the page truncated mid-sentence with the reason printed
// in the body, and no 500 anywhere.
//
// Both fields exist on this type and one of them is nil, so {{with}} is a
// question the template can actually answer.
type runPage struct {
	Registered   *story.Run
	Unregistered *unregisteredRun
}

func (s *Server) run(r *http.Request) (string, any, error) {
	id := strings.TrimPrefix(r.URL.Path, "/run/")
	st, err := story.OfRun(s.Led, s.Cfg, id)
	if err == nil {
		return "Run " + id, &runPage{Registered: st}, nil
	}
	if !errors.Is(err, ledger.ErrNotFound) {
		return "", nil, err
	}
	v, ferr := s.unregisteredRun(id)
	if ferr != nil {
		return "", nil, err
	}
	return "Run " + id, &runPage{Unregistered: v}, nil
}

// unregisteredRun gathers everything the record says about an id that has no
// run row.
func (s *Server) unregisteredRun(id string) (*unregisteredRun, error) {
	evs, err := s.Led.Events(1, 0)
	if err != nil {
		return nil, err
	}
	v := &unregisteredRun{RunID: id}
	needle := `"` + id + `"`
	for _, e := range evs {
		if e.Kind == ledger.KindRunStarted {
			continue
		}
		if !strings.Contains(string(e.Payload), needle) {
			continue
		}
		v.Mentions = append(v.Mentions, runMention{
			Seq: e.Seq, Kind: string(e.Kind), Subject: e.Subject,
			Says: saysWhat(e), Link: linkFor(e.Kind, e.Subject),
			When: time.UnixMilli(e.TsMS).Format("Jan 2 15:04:05"),
		})
	}
	sort.Slice(v.Mentions, func(i, j int) bool { return v.Mentions[i].Seq < v.Mentions[j].Seq })

	switch {
	case len(v.Mentions) == 0:
		v.Why = "Nothing in the record mentions this id at all. It is either a typo or a run from a " +
			"different project's ledger."
	default:
		v.Why = "This id appears in the record, but nothing ever appended a run.started for it — so " +
			"there is no prompt pin, no base commit, no start time and no attribution behind anything " +
			"it did. That is a real gap rather than a rendering problem, and it is the exact defect " +
			"the run_not_started refusal exists to prevent today: a proposal from a run nobody " +
			"registered is now turned down. These rows predate that guard, or were written by hand."
	}
	return v, nil
}
