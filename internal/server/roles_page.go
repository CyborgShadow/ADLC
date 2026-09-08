package server

// The Roles page.
//
// A role is a prompt file and nothing else: the dispatcher reads it at dispatch
// time and carries no prompt of its own. So the page that lists roles has to be
// the page that shows the prompt, and the page that shows the prompt has to be
// the page that edits it — otherwise the operator reads a role here and changes
// it somewhere else, and the two drift without either surface noticing.
//
// The page is a selector and a reader. You pick a role from the rail, you get
// its whole prompt: the role file as stored, and the text an agent is actually
// handed once the shared preamble is joined to it. Both, because a role prompt
// read on its own is missing the fleet policy it never restates, and a run that
// misbehaves is usually obeying the half nobody was looking at.
//
// Edits are refused when they drop a mandatory clause. That refusal is the
// reason this surface can exist at all: a prompt is the instruction set of an
// agent that will re-derive its own design from first principles if a rule is
// not restated, so an editor that could quietly delete "you never mark your own
// work done" would be a way to disable an audit control with no commit, no
// review and no trace.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// roleLayerOrder is the pipeline order, which is also the order somebody
// reading the page needs: work enters at the top and leaves at the bottom.
// Alphabetical would put the arbiter before the builder it arbitrates.
var roleLayerOrder = []struct{ Key, Label, What string }{
	{"planning", "Planning", "Turns an intent into work items somebody has checked."},
	{"worker", "Building", "Writes one item and its tests, then stops."},
	{"verification", "Verification", "Runs the tests, judges the item, reviews it adversarially."},
	{"stewardship", "Stewardship", "Looks at the system rather than at the item."},
	{"platform", "Platform", "Touches real machines, and only after an approval."},
}

type rolesView struct {
	Layers []roleLayerGroup
	// Sel is the role being read. Nil is the landing state: a selector and
	// nothing else, which is the whole point of the page.
	Sel      *roleDetail
	Preamble preambleEditor
	Clauses  []string
	Dir      string
	// Unknown is a /roles/<x> that names neither a role nor a prompt. It is
	// reported rather than 404'd, because the usual cause is a role that was
	// renamed in config and the operator following an old link.
	Unknown  string
	NoLib    bool
	Orphans  []orphanPrompt
	NeverRun int
}

type roleLayerGroup struct {
	Key, Label, What string
	Roles            []roleCard
}

// roleCard is one row of the selector. It carries enough to choose without
// clicking: what the role does, what it may do, where it may do it, and
// whether it has ever actually done it.
type roleCard struct {
	Type, Description, PromptID string
	Caps, Areas                 []string
	Runs, Pass, Refusals        int
	LowCadence, Missing         bool
	// Never is a role the ledger has no run for. It is computed from the
	// registry rather than inferred from silence, because a worker that never
	// runs never files a row saying so.
	Never    bool
	Selected bool
}

type roleDetail struct {
	Type, Description, Layer string
	Caps, Areas              []string
	// UsedBy names every role dispatched with this same file. Editing is a
	// shared edit whenever there is more than one, and it is not obvious from
	// the role you arrived from.
	UsedBy                       []string
	PromptID, Version, Path, SHA string
	Body, Assembled              string
	Vars                         []string
	Runs, Pass, Fail, Refusals   int
	Last, Cost                   string
	Never, LowCadence            bool
	// Missing is a role whose config names a prompt the library does not have.
	// Nothing can be dispatched for it, which is worth saying loudly.
	Missing bool
	Why     string
	// Lost are mandatory clauses absent from this role's assembled text right
	// now. Shown rather than only enforced on save, because a clause can also
	// go missing by editing the preamble that used to carry it.
	Lost []string
}

type preambleEditor struct {
	Path, SHA, Text string
	Roles           int
	Lost            []string
	Absent          bool
}

// orphanPrompt is a file in the prompt directory no role is dispatched with.
// It is listed for the same reason a never-run role is: an unused prompt looks
// exactly like a used one until somebody checks, and editing the wrong file is
// a change that appears to do nothing.
type orphanPrompt struct{ ID, Path string }

func (s *Server) rolesPage(r *http.Request) (string, any, error) {
	sel := strings.Trim(strings.TrimPrefix(r.URL.Path, "/roles"), "/")
	v := &rolesView{Clauses: s.Cfg.Prompts.MandatoryClauses, Dir: s.Cfg.Prompts.Dir, NoLib: s.Lib == nil}

	stats := map[string]ledger.WorkerStat{}
	if all, err := s.Led.WorkerStats(""); err == nil {
		for _, st := range all {
			stats[st.Type] = st
		}
	}

	// Resolve the selection before building the rail, so a card can mark itself.
	// A prompt id is accepted as well as a role name, so the orphan list and any
	// link written against a prompt file still lands somewhere useful.
	chosen := -1
	for i, w := range s.Cfg.Workers {
		if w.Type == sel {
			chosen = i
			break
		}
	}
	if chosen < 0 && sel != "" {
		for i, w := range s.Cfg.Workers {
			if w.Prompt == sel {
				chosen = i
				break
			}
		}
	}

	groups := map[string]*roleLayerGroup{}
	var order []*roleLayerGroup
	for _, l := range roleLayerOrder {
		g := &roleLayerGroup{Key: l.Key, Label: l.Label, What: l.What}
		groups[l.Key] = g
		order = append(order, g)
	}
	for i, w := range s.Cfg.Workers {
		st := stats[w.Type]
		c := roleCard{Type: w.Type, Description: w.Description, PromptID: w.Prompt,
			Caps: w.Capabilities, Areas: w.Areas, Runs: st.Runs, Pass: st.Pass,
			Refusals: st.Refusals, LowCadence: w.LowCadence, Never: st.Runs == 0,
			Selected: i == chosen}
		if s.Lib != nil {
			if _, err := s.Lib.Get(w.Prompt); err != nil {
				c.Missing = true
			}
		}
		if c.Never && !w.LowCadence {
			v.NeverRun++
		}
		g, ok := groups[w.Layer]
		if !ok {
			// A layer the pipeline does not know still gets a group. Dropping the
			// role would hide a config error behind a page that looks complete.
			g = &roleLayerGroup{Key: w.Layer, Label: orDefault(w.Layer, "unplaced"),
				What: "Declared with a layer the lifecycle does not use, so nothing here is scheduled by layer."}
			groups[w.Layer] = g
			order = append(order, g)
		}
		g.Roles = append(g.Roles, c)
	}
	for _, g := range order {
		if len(g.Roles) > 0 {
			v.Layers = append(v.Layers, *g)
		}
	}

	if chosen >= 0 {
		v.Sel = s.roleDetail(&s.Cfg.Workers[chosen], stats[s.Cfg.Workers[chosen].Type])
	} else if sel != "" {
		if s.Lib != nil {
			if _, err := s.Lib.Get(sel); err == nil {
				v.Sel = s.roleDetail(&config.WorkerDecl{Prompt: sel,
					Description: "No role in this project's config is dispatched with this prompt."}, ledger.WorkerStat{})
			}
		}
		if v.Sel == nil {
			v.Unknown = sel
		}
	}

	v.Preamble = s.preambleEditor()
	v.Orphans = s.orphanPrompts()

	title := "Roles"
	if v.Sel != nil && v.Sel.Type != "" {
		title = "Roles · " + v.Sel.Type
	}
	return title, v, nil
}

// roleDetail assembles everything about one role. w may describe a prompt no
// role uses, in which case Type is empty and the page says so.
func (s *Server) roleDetail(w *config.WorkerDecl, st ledger.WorkerStat) *roleDetail {
	d := &roleDetail{Type: w.Type, Description: w.Description, Layer: w.Layer,
		Caps: w.Capabilities, Areas: w.Areas, PromptID: w.Prompt,
		Runs: st.Runs, Pass: st.Pass, Fail: st.Fail, Refusals: st.Refusals,
		Never: st.Runs == 0, LowCadence: w.LowCadence, Last: "never",
		Cost: spend.Micros(st.CostMicros).String()}
	if st.LastMS > 0 {
		d.Last = time.Since(time.UnixMilli(st.LastMS)).Round(time.Second).String() + " ago"
	}
	for _, o := range s.Cfg.Workers {
		if o.Prompt == w.Prompt && o.Type != "" {
			d.UsedBy = append(d.UsedBy, o.Type)
		}
	}
	if s.Lib == nil {
		d.Missing, d.Why = true, "No prompt library is loaded, so no role can be dispatched at all."
		return d
	}
	p, err := s.Lib.Get(w.Prompt)
	if err != nil {
		d.Missing, d.Why = true, err.Error()
		return d
	}
	d.Version, d.Path, d.SHA, d.Body = p.Version, p.Path, p.SHA(), p.Body
	if asm, err := s.Lib.Assemble(w.Prompt, nil); err == nil {
		d.Assembled = asm.Text
		d.Vars = promptPlaceholders(asm.Text)
		d.Lost = prompt.MissingClauses(asm.Text, s.Cfg.Prompts.MandatoryClauses)
	}
	return d
}

func (s *Server) preambleEditor() preambleEditor {
	var e preambleEditor
	if s.Lib == nil || s.Lib.PreamblePath() == "" {
		e.Absent = true
		return e
	}
	e.Path, e.SHA, e.Text = s.Lib.PreamblePath(), s.Lib.PreambleSHA(), s.Lib.PreambleText()
	e.Roles = len(s.Cfg.Workers)
	// Checked against the preamble alone. A clause a role restates is still
	// carried for that role, but a clause only the preamble had is gone from
	// every role that does not, which is the failure worth naming here.
	e.Lost = prompt.MissingClauses(e.Text, s.Cfg.Prompts.MandatoryClauses)
	return e
}

func (s *Server) orphanPrompts() []orphanPrompt {
	if s.Lib == nil {
		return nil
	}
	used := map[string]bool{}
	for _, w := range s.Cfg.Workers {
		used[w.Prompt] = true
	}
	var out []orphanPrompt
	for _, id := range s.Lib.IDs() {
		if used[id] {
			continue
		}
		p, err := s.Lib.Get(id)
		if err != nil || p.Path == s.Lib.PreamblePath() {
			continue
		}
		out = append(out, orphanPrompt{ID: id, Path: p.Path})
	}
	return out
}

// promptPlaceholders lists the {{name}} slots the dispatcher fills in. They are
// shown unsubstituted on purpose: an operator editing a prompt needs to know
// which words are supplied per run, or they will write a value in by hand and
// pin every future run to one work item.
func promptPlaceholders(text string) []string {
	seen := map[string]bool{}
	var out []string
	for i := 0; ; {
		j := strings.Index(text[i:], "{{")
		if j < 0 {
			return out
		}
		i += j + 2
		k := strings.Index(text[i:], "}}")
		if k < 0 {
			return out
		}
		name := strings.TrimSpace(text[i : i+k])
		i += k + 2
		if name == "" || strings.ContainsAny(name, " \n\t") || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
}

// ------------------------------------------------------------------ writes

// saveRolePrompt writes one role prompt back to its file.
func (s *Server) saveRolePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/roles", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	back := safeBack(orDefault(r.FormValue("back"), "/roles"))
	if s.Lib == nil {
		redirect(w, r, back, "no prompt library is loaded, so there is no file to write", true)
		return
	}
	body := r.FormValue("body")
	if strings.TrimSpace(body) == "" {
		redirect(w, r, back, "a role prompt cannot be emptied — the run would be dispatched with the preamble and no job", true)
		return
	}
	p, err := s.Lib.Get(id)
	if err != nil {
		redirect(w, r, back, err.Error(), true)
		return
	}
	pre := s.Lib.PreambleText()
	// Compared before against after rather than checked absolutely. A clause
	// that was already missing is a defect somebody has to be able to fix from
	// here; refusing every edit until it is fixed would leave the only surface
	// that can fix it locked.
	if lost := removedClauses(pre+"\n"+p.Body, pre+"\n"+body, s.Cfg.Prompts.MandatoryClauses); len(lost) > 0 {
		redirect(w, r, back, clauseRefusal(lost, "this role's prompt"), true)
		return
	}
	if err := s.Lib.SetPrompt(id, body); err != nil {
		redirect(w, r, back, err.Error(), true)
		return
	}
	after, _ := s.Lib.Get(id)
	// Recorded, because a change to what an agent is told is exactly what
	// somebody is looking for three days later when a run started behaving
	// differently and nothing in the config moved.
	_, _ = s.Led.Append(s.Actor, ledger.KindNoteRecorded, id, ledger.NoteRecorded{
		About: "prompt:" + id,
		Text: fmt.Sprintf("role prompt %s edited from the dashboard: %s was %s, now %s",
			id, p.Path, roleShortSHA(p.SHA()), roleShortSHA(after.SHA())),
	})
	redirect(w, r, back, "saved "+p.Path+" — the next dispatch is assembled from it; runs already in flight keep the bytes they were given", false)
}

// savePreamble writes the shared preamble back to its file.
func (s *Server) savePreamble(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/roles", http.StatusSeeOther)
		return
	}
	back := safeBack(orDefault(r.FormValue("back"), "/roles"))
	if s.Lib == nil || s.Lib.PreamblePath() == "" {
		redirect(w, r, back, "this project declares no preamble file, so there is nothing to write", true)
		return
	}
	text := r.FormValue("body")
	if strings.TrimSpace(text) == "" {
		redirect(w, r, back, "the preamble cannot be emptied — it is the only fleet policy every role is given", true)
		return
	}
	old := s.Lib.PreambleText()
	// Checked against every role, not against the preamble alone. The preamble
	// is the only copy of a fleet-wide rule, so deleting a line here removes it
	// from every role that relied on it while each role file still looks fine.
	lost := map[string][]string{}
	for _, id := range s.Lib.IDs() {
		p, err := s.Lib.Get(id)
		if err != nil {
			continue
		}
		for _, c := range removedClauses(old+"\n"+p.Body, text+"\n"+p.Body, s.Cfg.Prompts.MandatoryClauses) {
			lost[c] = append(lost[c], id)
		}
	}
	if len(lost) > 0 {
		var msg []string
		for _, c := range s.Cfg.Prompts.MandatoryClauses {
			if roles, ok := lost[c]; ok {
				msg = append(msg, fmt.Sprintf("%q (%s)", c, strings.Join(roles, ", ")))
			}
		}
		redirect(w, r, back, "refused: that edit removes a mandatory clause from "+
			"roles that do not restate it — "+strings.Join(msg, "; ")+
			". Put the clause back, or take it out of mandatory_clauses in the config first.", true)
		return
	}
	if err := s.Lib.SetPreamble(text); err != nil {
		redirect(w, r, back, err.Error(), true)
		return
	}
	_, _ = s.Led.Append(s.Actor, ledger.KindNoteRecorded, "_preamble", ledger.NoteRecorded{
		About: "prompt:_preamble",
		Text: fmt.Sprintf("shared preamble edited from the dashboard: %s now %s, which changes every role",
			s.Lib.PreamblePath(), roleShortSHA(s.Lib.PreambleSHA())),
	})
	redirect(w, r, back, "saved the shared preamble — every role assembled from the next dispatch onward carries it", false)
}

// roleShortSHA trims a digest for a one-line note. The full digest stays in the
// prompt library; this is only ever for reading.
func roleShortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// removedClauses names the clauses present in before and absent from after.
func removedClauses(before, after string, clauses []string) []string {
	had := map[string]bool{}
	for _, c := range clauses {
		had[c] = true
	}
	for _, c := range prompt.MissingClauses(before, clauses) {
		had[c] = false
	}
	var out []string
	for _, c := range prompt.MissingClauses(after, clauses) {
		if had[c] {
			out = append(out, c)
		}
	}
	return out
}

func clauseRefusal(lost []string, what string) string {
	var q []string
	for _, c := range lost {
		q = append(q, fmt.Sprintf("%q", c))
	}
	return "refused, and nothing was written: that edit removes a mandatory clause from " + what +
		" — " + strings.Join(q, "; ") +
		". The clauses exist so a prompt edit cannot quietly delete a safety rule; put it back, " +
		"or change mandatory_clauses in the config if the rule itself is wrong."
}
