// Package server is the operator's dashboard.
//
// Everything it shows is a projection of the hash-chained ledger. Both
// statements were honest. Only one was measured.
//
// It writes back a deliberately small number of things: an answer to a
// question, a decision on an approval, and a lane's cadence or pause switch.
// Those are the moments the fleet genuinely needs a person, and a surface that
// stops at "here is the status" leaves the operator typing CLI commands
// exactly when they are least able to.
//
// It binds to loopback and has no authentication, and those two facts are
// welded together.
package server

import (
	"context"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/dispatch"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/spend"
	"github.com/CyborgShadow/ADLC/internal/story"
)

// Server serves the dashboard.
type Server struct {
	Cfg   *config.Config
	Led   *ledger.Ledger
	Sched *dispatch.Scheduler
	Lib   *prompt.Library
	Actor string
	Repo  string
	Now   func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Bind resolves the listen address and refuses anything that is not loopback.
func Bind(addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("server address %q is not host:port: %w", addr, err)
	}
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf(
			"server address %q has an empty host, which listens on EVERY interface. This dashboard writes to the ledger and has no authentication because it is not reachable; those two facts have to stay together. Use 127.0.0.1:%s", addr, port)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf(
				"server address %q is not loopback. This dashboard writes to the ledger and has no authentication, so it may only be bound to 127.0.0.1 or ::1", addr)
		}
	}
	return net.Listen("tcp", addr)
}

// Handler builds the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page("overview", s.overview))
	mux.HandleFunc("/roadmap", s.page("roadmap", s.roadmap))
	mux.HandleFunc("/progress", s.page("progress", s.progress))
	mux.HandleFunc("/questions", s.page("questions", s.questions))
	mux.HandleFunc("/approvals", s.page("approvals", s.approvals))
	mux.HandleFunc("/roles", s.page("roles", s.roles))
	mux.HandleFunc("/role/", s.page("role", s.role))
	mux.HandleFunc("/coordination", s.page("coordination", s.coordination))
	mux.HandleFunc("/about", s.page("about", s.about))
	mux.HandleFunc("/config", s.page("config", s.configPage))
	mux.HandleFunc("/history", s.page("history", s.history))
	mux.HandleFunc("/run/", s.page("run", s.run))
	mux.HandleFunc("/item/", s.page("item", s.item))
	mux.HandleFunc("/segment/", s.page("segment", s.segment))

	mux.HandleFunc("/answer", s.answer)
	mux.HandleFunc("/decide", s.decide)
	mux.HandleFunc("/loop", s.setLoop)
	mux.HandleFunc("/signoff", s.signoff)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		rep, err := s.Led.Verify()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Three-valued, like everything else here: UNKNOWN means this build cannot
		// read part of the record, which is neither healthy nor an accusation.
		fmt.Fprintf(w, "%s seq=%d events=%d\n", rep.Verdict, rep.HeadSeq, rep.Events)
	})
	return mux
}

// Serve runs until the context is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ------------------------------------------------------------------ shell

type pageData struct {
	Page     string
	Title    string
	Refresh  int
	Nav      []navItem
	Attn     attention
	Body     any
	Verdict  string
	HeadSeq  int64
	Project  string
	Now      string
	Flash    string
	FlashBad bool
}

type navItem struct {
	Href, Label string
	Count       int
	Alarm       bool
	Active      bool
}

type attention struct {
	Questions int
	Approvals int
	Blocked   int
	DarkLoops int
	Stuck     int
	Tampered  bool
}

func (a attention) Any() bool {
	return a.Questions > 0 || a.Approvals > 0 || a.Blocked > 0 || a.DarkLoops > 0 || a.Stuck > 0 || a.Tampered
}

func (s *Server) page(name string, fn func(*http.Request) (string, any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title, body, err := fn(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d, err := s.shell(name, title, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d.Flash = r.URL.Query().Get("msg")
		d.FlashBad = r.URL.Query().Get("bad") == "1"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "page", d); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (s *Server) shell(page, title string, body any) (*pageData, error) {
	seq, _, err := s.Led.Head()
	if err != nil {
		return nil, err
	}
	rep, err := s.Led.Verify()
	if err != nil {
		return nil, err
	}
	qs, _ := s.Led.Questions("", true)
	aps, _ := s.Led.Approvals("")
	items, _ := s.Led.Items("")

	var attn attention
	attn.Tampered = rep.Verdict == ledger.VerdictTampered
	for _, q := range qs {
		if q.Blocking {
			attn.Questions++
		}
	}
	for _, a := range aps {
		if !a.Decided() {
			attn.Approvals++
		}
	}
	for _, it := range items {
		switch authority.State(it.State) {
		case authority.StateBlocked:
			attn.Blocked++
		case authority.StateRejected:
			if it.Attempts >= s.Cfg.Dispatch.MaxAttempts {
				attn.Stuck++
			}
		}
	}
	if s.Sched != nil {
		if hs, err := s.Sched.Health(s.now()); err == nil {
			for _, h := range hs {
				if h.Enabled && !h.Fresh(s.now(), 3) {
					attn.DarkLoops++
				}
			}
		}
	}
	nav := []navItem{
		{Href: "/", Label: "Overview"},
		{Href: "/roadmap", Label: "Roadmap"},
		{Href: "/progress", Label: "Progress"},
		{Href: "/questions", Label: "Questions", Count: attn.Questions, Alarm: attn.Questions > 0},
		{Href: "/approvals", Label: "Approvals", Count: attn.Approvals, Alarm: attn.Approvals > 0},
		{Href: "/coordination", Label: "Coordination"},
		{Href: "/roles", Label: "Roles"},
		{Href: "/config", Label: "Config", Count: attn.DarkLoops, Alarm: attn.DarkLoops > 0},
		{Href: "/history", Label: "History"},
		{Href: "/about", Label: "About the ADLC"},
	}
	for i := range nav {
		nav[i].Active = (page == "overview" && nav[i].Href == "/") ||
			(page != "overview" && strings.HasPrefix(nav[i].Href, "/"+page))
	}
	return &pageData{
		Page: page, Title: title, Refresh: s.Cfg.Server.RefreshSeconds, Project: s.Cfg.Project,
		Verdict: string(rep.Verdict), HeadSeq: seq, Attn: attn, Body: body,
		Now: s.now().Format("15:04:05"), Nav: nav,
	}, nil
}

// ------------------------------------------------------------------ pages

type activeRun struct {
	ledger.Run
	Elapsed string
	Item    string
	Stage   string
	Tries   int
	Passed  int
	Failed  int
	Cost    string
	Stale   bool
}

type overviewView struct {
	Active   []activeRun
	Recent   []recentRun
	Stages   []stageCount
	Spend    spend.Report
	Blobs    int
	Refusals []ledger.Proposal
}

type stageCount struct {
	authority.Stage
	N int
}

type recentRun struct {
	ledger.Run
	When    string
	Cost    string
	Outcome string
	Class   string
}

func (s *Server) overview(*http.Request) (string, any, error) {
	v := overviewView{}
	active, err := s.Led.ActiveRuns()
	if err != nil {
		return "", nil, err
	}
	now := s.now()
	for _, r := range active {
		a := activeRun{Run: r, Item: r.ItemID, Cost: spend.Micros(r.CostMicros).String()}
		d := now.Sub(time.UnixMilli(r.StartedMS))
		a.Elapsed = d.Round(time.Second).String()
		// A run open far longer than the dispatch timeout is almost certainly a
		// process that died without recording an end. Saying so is more useful than
		// showing it as busy forever.
		limit := time.Duration(s.Cfg.Dispatch.TimeoutSeconds) * time.Second
		if limit > 0 && d > 2*limit {
			a.Stale = true
		}
		if r.ItemID != "" {
			if it, err := s.Led.Item(r.ItemID); err == nil {
				a.Stage = authority.StageOf(authority.State(it.State)).Label
			}
			a.Tries, a.Passed, a.Failed, _ = s.Led.AttemptsFor(r.ItemID)
		}
		v.Active = append(v.Active, a)
	}

	items, _ := s.Led.Items("")
	byStage := map[string]int{}
	for _, it := range items {
		byStage[authority.StageOf(authority.State(it.State)).Key]++
	}
	for _, st := range authority.Stages() {
		v.Stages = append(v.Stages, stageCount{Stage: st, N: byStage[st.Key]})
	}

	runs, _ := s.Led.Runs("", 25)
	for _, r := range runs {
		rr := recentRun{Run: r, Cost: spend.Micros(r.CostMicros).String(),
			When: time.Since(time.UnixMilli(r.StartedMS)).Round(time.Second).String() + " ago"}
		switch {
		case !r.Finished():
			rr.Outcome, rr.Class = "UNKNOWN — no end recorded", "warn"
		case r.Verdict == "pass":
			rr.Outcome, rr.Class = "pass", "ok"
		case r.Verdict == "blocked":
			rr.Outcome, rr.Class = "blocked", "warn"
		default:
			rr.Outcome, rr.Class = r.Verdict, "bad"
		}
		v.Recent = append(v.Recent, rr)
	}
	v.Spend, _ = spend.Summarise(s.Led, s.Cfg.Budget, now)
	v.Blobs, _, _ = s.Led.BlobStats()
	v.Refusals, _ = s.Led.Proposals("", true, 8)
	return "Overview", v, nil
}

type roadmapRow struct {
	ledger.Segment
	Progress authority.SegmentProgress
	Stage    string
	Class    string
	Next     string
	// NeedsYou marks the one planning gate no machine passes on its own, and
	// carries the state the sign-off control would move it to.
	NeedsYou bool
	SignTo   string
	SignVerb string
}

func (s *Server) roadmap(*http.Request) (string, any, error) {
	segs, err := s.Led.Segments()
	if err != nil {
		return "", nil, err
	}
	var rows []roadmapRow
	for _, sg := range segs {
		items, _ := s.Led.Items(sg.ID)
		r := roadmapRow{Segment: sg, Progress: authority.Progress(items)}
		r.Stage = sg.State
		switch authority.SegmentState(sg.State) {
		case authority.SegTheory:
			r.Class, r.Next = "mute", "An idea. Nothing is committed to it yet."
			r.NeedsYou, r.SignTo, r.SignVerb = true, string(authority.SegRoadmap), "Put on the roadmap"
		case authority.SegRoadmap:
			r.Class, r.Next = "warn", "On the roadmap, waiting for you to sign off the intent. Nothing moves until you do."
			r.NeedsYou, r.SignTo, r.SignVerb = true, string(authority.SegSignedOff), "Sign it off"
		case authority.SegSignedOff:
			r.Class, r.Next = "live", "Signed off. A researcher will turn the intent into an approach."
		case authority.SegResearching:
			r.Class, r.Next = "live", "A researcher is working out the approach."
		case authority.SegResearched:
			r.Class, r.Next = "live", "The approach is written. A planner will decompose it into work."
		case authority.SegPlanning:
			r.Class, r.Next = "live", "A planner is decomposing the approach."
		case authority.SegPlanned:
			r.Class, r.Next = "warn", "The plan is written and nobody has checked it against the intent yet. No work starts until they do."
		case authority.SegValidating:
			r.Class, r.Next = "live", "A validator is checking the plan against the intent."
		case authority.SegReady:
			r.Class, r.Next = "live", "The plan was accepted. Work can start."
		case authority.SegBuilding:
			r.Class, r.Next = "live", "Work is in flight."
		case authority.SegDelivered:
			r.Class, r.Next = "ok", "Delivered."
		case authority.SegPaused:
			r.Class, r.Next = "mute", "Paused by an operator."
		}
		rows = append(rows, r)
	}
	return "Roadmap", rows, nil
}

type progressView struct {
	Rows    []roadmapRow
	Stages  []authority.Stage
	Totals  map[string]int
	Overall authority.SegmentProgress
}

func (s *Server) progress(*http.Request) (string, any, error) {
	segs, _ := s.Led.Segments()
	all, _ := s.Led.Items("")
	v := progressView{Stages: authority.Stages(), Totals: map[string]int{},
		Overall: authority.Progress(all)}
	for _, sg := range segs {
		items, _ := s.Led.Items(sg.ID)
		v.Rows = append(v.Rows, roadmapRow{Segment: sg, Progress: authority.Progress(items), Stage: sg.State})
	}
	for _, it := range all {
		v.Totals[authority.StageOf(authority.State(it.State)).Key]++
	}
	return "Progress", v, nil
}

func (s *Server) questions(*http.Request) (string, any, error) {
	qs, err := s.Led.Questions("", true)
	if err != nil {
		return "", nil, err
	}
	sort.Slice(qs, func(i, j int) bool {
		if qs[i].Blocking != qs[j].Blocking {
			return qs[i].Blocking
		}
		return qs[i].ID < qs[j].ID
	})
	return "Questions", struct{ Items []ledger.Question }{qs}, nil
}

type approvalRow struct {
	ledger.Approval
	Item ledger.Item
}

func (s *Server) approvals(*http.Request) (string, any, error) {
	aps, err := s.Led.Approvals("")
	if err != nil {
		return "", nil, err
	}
	var out []approvalRow
	for _, a := range aps {
		it, _ := s.Led.Item(a.ItemID)
		out = append(out, approvalRow{Approval: a, Item: it})
	}
	return "Approvals", struct{ Rows []approvalRow }{out}, nil
}

type roleRow struct {
	config.WorkerDecl
	Stat    ledger.WorkerStat
	Prompt  string
	SHA     string
	Missing bool
}

func (s *Server) roles(*http.Request) (string, any, error) {
	stats, _ := s.Led.WorkerStats("")
	byType := map[string]ledger.WorkerStat{}
	for _, st := range stats {
		byType[st.Type] = st
	}
	var out []roleRow
	for _, w := range s.Cfg.Workers {
		r := roleRow{WorkerDecl: w, Stat: byType[w.Type], Prompt: w.Prompt}
		if s.Lib != nil {
			if p, err := s.Lib.Get(w.Prompt); err == nil {
				r.SHA = p.SHA()
			} else {
				r.Missing = true
			}
		}
		out = append(out, r)
	}
	return "Roles", struct {
		Rows     []roleRow
		Preamble string
		PSHA     string
		PPath    string
	}{out, preambleText(s.Lib), preambleSHA(s.Lib), preamblePath(s.Lib)}, nil
}

func (s *Server) role(r *http.Request) (string, any, error) {
	id := strings.TrimPrefix(r.URL.Path, "/role/")
	if s.Lib == nil {
		return "", nil, fmt.Errorf("no prompt library is loaded")
	}
	p, err := s.Lib.Get(id)
	if err != nil {
		return "", nil, err
	}
	asm, err := s.Lib.Assemble(id, nil)
	if err != nil {
		return "", nil, err
	}
	var users []string
	for _, w := range s.Cfg.Workers {
		if w.Prompt == id {
			users = append(users, w.Type)
		}
	}
	return "Role " + id, struct {
		ID, Version, Path, SHA, Body, Assembled string
		UsedBy                                  []string
	}{p.ID, p.Version, p.Path, p.SHA(), p.Body, asm.Text, users}, nil
}

type handoff struct {
	Stage, Who, Does, HandsTo string
}

func (s *Server) coordination(*http.Request) (string, any, error) {
	who := func(cap string) string {
		ws := s.Cfg.WorkersWith(cap)
		if len(ws) == 0 {
			return "NOBODY — this stage is unreachable"
		}
		return strings.Join(ws, ", ")
	}
	chain := []handoff{
		{"1 · Theory", "you", "write down an idea: a title, what it is for, and why it matters",
			"it sits as a theory until you put it on the roadmap"},
		{"2 · Sign-off", "you", "agree the intent is worth pursuing — the one planning gate no machine passes on its own",
			"a researcher picks it up"},
		{"3 · Research", who(config.CapResearch), "turns the intent into a written approach: what already exists, what the options are, which one and why",
			"a planner decomposes the approach"},
		{"4 · Plan", who(config.CapPlan), "breaks the approach into work items, each with acceptance criteria a command can check",
			"the plan goes for validation before any of it is built"},
		{"5 · Validate the plan", who(config.CapValidate), "checks the plan against the intent — would these items, done, actually deliver it?",
			"on a pass the work opens; on a reject it goes back for decomposition"},
		{"6 · Build", who(config.CapImplement), "implements one item and writes tests for what it wrote, then stops",
			"the item goes to a tester and the builder never sees it again"},
		{"7 · Test", who(config.CapTest), "executes the tests against the item",
			"green goes to a judge; a failure goes back to the builder with the output"},
		{"8 · Judge", who(config.CapJudge), "judges the item against its acceptance criteria, by running things rather than by reading",
			"a pass goes to adversarial validation"},
		{"9 · Validate", who(config.CapValidate), "adversarial review against the project's invariants; the only role that may reject",
			"reviewed, or back with blockers"},
		{"10 · Janitor", who(config.CapCurate), "hygiene pass over what landed: stale docs, dead references, duplicated facts",
			"then the arbiter looks at the whole system"},
		{"11 · Arbitrate", who(config.CapArbitrate), "judges the change against the system rather than against the item — the only role that looks wider than one unit of work",
			"cleared to merge, or stopped for an approval if it reaches a real machine"},
		{"12 · Apply", who(config.CapOperate), "performs the change against the real thing, after an approval that named the exact plan",
			"the applied artifact goes back to a validator for confirmation"},
		{"13 · Merge", "the control plane", "rebases onto the trunk, re-runs the gate on the rebased tree, and fast-forwards — refusing anything that would revert a sibling's landed work",
			"merged"},
		{"14 · Improve", who(config.CapImprove), "records what was learned and raises self-improvements as their own work items",
			"done — and the fleet is a little better than it was"},
	}
	var lanes []ledger.LoopHealth
	if s.Sched != nil {
		lanes, _ = s.Sched.Health(s.now())
	}
	type areaRow struct{ Area, Owner, Caps string }
	var areas []areaRow
	keys := make([]string, 0, len(s.Cfg.Routing))
	for a := range s.Cfg.Routing {
		keys = append(keys, a)
	}
	sort.Strings(keys)
	for _, a := range keys {
		owner := s.Cfg.Routing[a]
		caps := ""
		if w := s.Cfg.Worker(owner); w != nil {
			caps = strings.Join(w.Capabilities, ", ")
		}
		areas = append(areas, areaRow{a, owner, caps})
	}
	return "Coordination", struct {
		Chain []handoff
		Lanes []ledger.LoopHealth
		Areas []areaRow
		Now   time.Time
	}{chain, lanes, areas, s.now()}, nil
}

func lowCadence(c *config.Config) []string {
	var out []string
	for _, w := range c.Workers {
		if w.LowCadence {
			out = append(out, w.Type)
		}
	}
	if len(out) == 0 {
		return []string{"nobody is declared for this"}
	}
	return out
}

type loopRow struct {
	config.LoopDecl
	Health ledger.LoopHealth
	Status string
	Since  string
}

func (s *Server) configPage(*http.Request) (string, any, error) {
	var lanes []loopRow
	health := map[string]ledger.LoopHealth{}
	if s.Sched != nil {
		if hs, err := s.Sched.Health(s.now()); err == nil {
			for _, h := range hs {
				health[h.Loop] = h
			}
		}
	}
	for _, l := range s.Cfg.Loops {
		h := health[l.Name]
		row := loopRow{LoopDecl: l, Health: h, Status: "live", Since: "—"}
		switch {
		case !l.Enabled:
			row.Status = "paused"
		case h.LastTickMS == 0:
			row.Status = "NEVER RUN"
		case !h.Fresh(s.now(), 3):
			row.Status = "STALE"
		}
		if h.LastTickMS > 0 {
			row.Since = s.now().Sub(time.UnixMilli(h.LastTickMS)).Round(time.Second).String() + " ago"
		}
		lanes = append(lanes, row)
	}
	return "Config", struct {
		Path     string
		Lanes    []loopRow
		Cfg      *config.Config
		Clauses  []string
		Checks   []config.Check
		Radii    []config.Radius
		PriceSet bool
	}{
		s.Cfg.Path(), lanes, s.Cfg, s.Cfg.Prompts.MandatoryClauses, s.Cfg.Checks,
		config.Radii(), len(s.Cfg.Budget.PriceMicrosPerMTok) > 0,
	}, nil
}

type historyRun struct {
	ledger.Run
	Headline string
	When     string
	Cost     string
	Class    string
}

func (s *Server) history(r *http.Request) (string, any, error) {
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "runs"
	}
	if tab == "ledger" {
		from := int64(1)
		evs, err := s.Led.Events(from, 0)
		if err != nil {
			return "", nil, err
		}
		if len(evs) > 300 {
			evs = evs[len(evs)-300:]
		}
		for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
			evs[i], evs[j] = evs[j], evs[i]
		}
		type row struct {
			ledger.Event
			When    string
			Known   bool
			Payload string
		}
		var rows []row
		for _, e := range evs {
			rows = append(rows, row{Event: e, Known: ledger.KnownKinds[e.Kind],
				When:    time.UnixMilli(e.TsMS).Format("Jan 2 15:04:05"),
				Payload: string(e.Payload)})
		}
		return "History", struct {
			Tab  string
			Rows []row
		}{tab, rows}, nil
	}
	runs, err := s.Led.Runs("", 120)
	if err != nil {
		return "", nil, err
	}
	var out []historyRun
	for _, r := range runs {
		h := historyRun{Run: r, Cost: spend.Micros(r.CostMicros).String(),
			When: time.UnixMilli(r.StartedMS).Format("Jan 2 15:04")}
		if st, err := story.OfRun(s.Led, s.Cfg, r.RunID); err == nil {
			h.Headline = st.Headline
		}
		switch {
		case !r.Finished():
			h.Class = "warn"
		case r.Verdict == "pass":
			h.Class = "ok"
		default:
			h.Class = "bad"
		}
		out = append(out, h)
	}
	return "History", struct {
		Tab  string
		Runs []historyRun
	}{tab, out}, nil
}

func (s *Server) run(r *http.Request) (string, any, error) {
	id := strings.TrimPrefix(r.URL.Path, "/run/")
	st, err := story.OfRun(s.Led, s.Cfg, id)
	if err != nil {
		return "", nil, err
	}
	return "Run " + id, st, nil
}

func (s *Server) item(r *http.Request) (string, any, error) {
	id := strings.TrimPrefix(r.URL.Path, "/item/")
	it, err := s.Led.Item(id)
	if err != nil {
		return "", nil, err
	}
	runs, _ := s.Led.Runs(id, 30)
	props, _ := s.Led.Proposals(id, false, 30)
	qs, _ := s.Led.Questions(id, false)
	aps, _ := s.Led.Approvals(id)
	seg, _ := s.Led.Segment(it.SegmentID)
	tries, passed, failed, _ := s.Led.AttemptsFor(id)
	return "Item " + id, struct {
		Item                  ledger.Item
		Segment               ledger.Segment
		Stage                 authority.Stage
		Runs                  []ledger.Run
		Proposals             []ledger.Proposal
		Questions             []ledger.Question
		Approvals             []ledger.Approval
		Tries, Passed, Failed int
	}{it, seg, authority.StageOf(authority.State(it.State)), runs, props, qs, aps, tries, passed, failed}, nil
}

func (s *Server) segment(r *http.Request) (string, any, error) {
	id := strings.TrimPrefix(r.URL.Path, "/segment/")
	sg, err := s.Led.Segment(id)
	if err != nil {
		return "", nil, err
	}
	items, _ := s.Led.Items(id)
	hist, _ := s.Led.SegmentHistory(id)
	type move struct {
		When, From, To, Why string
	}
	var moves []move
	for _, e := range hist {
		var p ledger.SegmentAdvanced
		_ = jsonUnmarshal(e.Payload, &p)
		moves = append(moves, move{time.UnixMilli(e.TsMS).Format("Jan 2 15:04"), p.From, p.To, p.Why})
	}
	return "Deliverable " + id, struct {
		Segment  ledger.Segment
		Items    []ledger.Item
		Progress authority.SegmentProgress
		Moves    []move
	}{sg, items, authority.Progress(items), moves}, nil
}

// ------------------------------------------------------------------ writes

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/questions", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	text := strings.TrimSpace(r.FormValue("answer"))
	who := orDefault(strings.TrimSpace(r.FormValue("who")), s.Actor)
	if id == "" || text == "" {
		redirect(w, r, "/questions", "an answer needs both the question and some text", true)
		return
	}
	// Recorded verbatim. Summarising a human's note loses the reasoning that sets
	// the severity of everything decomposed from it.
	if _, err := s.Led.Append(who, ledger.KindQuestionAnswered, id, ledger.QuestionAnswered{
		ID: id, Answer: text, AnsweredBy: who,
	}); err != nil {
		redirect(w, r, "/questions", err.Error(), true)
		return
	}
	redirect(w, r, "/questions", "answered "+id+" — the item it was blocking is dispatchable again", false)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/approvals", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	verdict := strings.TrimSpace(r.FormValue("verdict"))
	who := strings.TrimSpace(r.FormValue("approver"))
	note := strings.TrimSpace(r.FormValue("note"))
	if id == "" || (verdict != "approve" && verdict != "reject") {
		redirect(w, r, "/approvals", "a decision needs an approval id and approve or reject", true)
		return
	}
	if verdict == "approve" && who == "" {
		// An approval nobody's name is on is an approval nobody gave, and this is
		// the last gate before something irreversible.
		redirect(w, r, "/approvals", "approving requires your name", true)
		return
	}
	if _, err := s.Led.Append(orDefault(who, s.Actor), ledger.KindApprovalDecided, id, ledger.ApprovalDecided{
		ID: id, Verdict: verdict, Approver: who, Note: note,
	}); err != nil {
		redirect(w, r, "/approvals", err.Error(), true)
		return
	}
	redirect(w, r, "/approvals", verdict+"d "+id, false)
}

func (s *Server) setLoop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/config", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	every, _ := strconv.Atoi(r.FormValue("every"))
	maxPer, _ := strconv.Atoi(r.FormValue("max"))
	enabled := r.FormValue("enabled") == "on"
	if err := s.Cfg.SetLoop(name, enabled, every, maxPer); err != nil {
		redirect(w, r, "/config", err.Error(), true)
		return
	}
	// The change is also recorded, because a lane being paused is exactly the
	// kind of thing somebody needs to find in the history three days later when
	// they are asking why nothing got verified.
	state := "paused"
	if enabled {
		state = "enabled"
	}
	_, _ = s.Led.Append(s.Actor, ledger.KindNoteRecorded, name, ledger.NoteRecorded{
		About: "loop:" + name,
		Text:  fmt.Sprintf("lane %s %s from the dashboard, cadence %ds, up to %d dispatch(es) per firing", name, state, every, maxPer),
	})
	redirect(w, r, "/config", "lane "+name+" "+state+" — it takes effect on its next tick", false)
}

func redirect(w http.ResponseWriter, r *http.Request, to, msg string, bad bool) {
	q := "?msg=" + template.URLQueryEscaper(msg)
	if bad {
		q += "&bad=1"
	}
	http.Redirect(w, r, to+q, http.StatusSeeOther)
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func preambleText(l *prompt.Library) string {
	if l == nil {
		return ""
	}
	return l.Preamble
}

func preambleSHA(l *prompt.Library) string {
	if l == nil {
		return ""
	}
	return l.PreambleSHA()
}

func preamblePath(l *prompt.Library) string {
	if l == nil {
		return ""
	}
	return l.PreamblePath()
}
