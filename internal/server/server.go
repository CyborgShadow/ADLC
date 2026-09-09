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
	"sync"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/dispatch"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// Server serves the dashboard.
type Server struct {
	Cfg   *config.Config
	Led   *ledger.Ledger
	Sched *dispatch.Scheduler
	Lib   *prompt.Library
	Actor string
	Repo  string
	// Runner invokes the console agent. When it is nil the scheduler's own
	// runner is used, so a project with a working fleet has a working console
	// without configuring a second way to start an agent.
	Runner dispatch.Runner
	Log    func(string)
	turns  turnState
	// Dispatching says whether THIS process fires lanes. A dashboard that only
	// reads the record and one that also runs the fleet look identical, and the
	// difference is the whole answer to "why is nothing happening" — so it is
	// stated on the page rather than left to be worked out.
	Dispatching bool
	// live holds the output of the turns currently running, so the page can
	// show one as it happens. It is a view of work in flight and never a
	// source of truth; see live.go.
	live liveStore
	// decoders turn each running agent's output into something readable. One
	// per run, because a decoder carries state across lines.
	decoders map[string]*streamDecoder
	decMu    sync.Mutex

	// The conversational runner, built once from console.command.
	sess     *sessionRunner
	sessOnce sync.Once
	Now      func() time.Time
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
	mux.HandleFunc("/", s.home2)
	mux.HandleFunc("/overview", s.page("overview", s.overview))
	mux.HandleFunc("/advanced", s.page("advanced", s.advanced))
	mux.HandleFunc("/roadmap", s.page("roadmap", s.roadmap))
	mux.HandleFunc("/progress", s.page("progress", s.progress))
	mux.HandleFunc("/questions", s.page("questions", s.questions))
	mux.HandleFunc("/approvals", s.page("approvals", s.approvals))
	mux.HandleFunc("/roles", s.page("roles", s.rolesPage))
	mux.HandleFunc("/roles/", s.page("roles", s.rolesPage))
	// Outside /roles/ so that no role name can ever shadow them.
	mux.HandleFunc("/prompt/save", s.saveRolePrompt)
	mux.HandleFunc("/preamble/save", s.savePreamble)
	mux.HandleFunc("/coordination", s.page("coordination", s.coordination))
	mux.HandleFunc("/about", s.page("about", s.about))
	mux.HandleFunc("/console", s.page("console", s.consolePage))
	mux.HandleFunc("/config", s.page("config", s.configPageV2))
	mux.HandleFunc("/history", s.page("history", s.historyPage))
	mux.HandleFunc("/run/", s.page("run", s.run))
	mux.HandleFunc("/item/", s.page("item", s.item))
	mux.HandleFunc("/segment/", s.page("segment", s.segment))

	mux.HandleFunc("/answer", s.answer)
	mux.HandleFunc("/decide", s.decide)
	mux.HandleFunc("/loop", s.setLoop)
	mux.HandleFunc("/config/console", s.saveConsoleCfg)
	mux.HandleFunc("/config/blast", s.saveBlastCfg)
	mux.HandleFunc("/config/budget", s.saveBudgetCfg)
	mux.HandleFunc("/config/dispatch", s.saveDispatchCfg)
	mux.HandleFunc("/config/price", s.savePriceCfg)
	mux.HandleFunc("/signoff", s.signoff)
	mux.HandleFunc("/resume", s.resume)
	mux.HandleFunc("/console/ask", s.ask)
	mux.HandleFunc("/console/toggle", s.toggleDock)
	mux.HandleFunc("/console/do", s.consoleDo)
	mux.HandleFunc("/live", s.consoleLive)
	mux.HandleFunc("/console/live", s.consoleLive)
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
	// Dock is the console panel, rendered on every page.
	Dock dockData
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
	// Lanes is how many are declared and enabled, so the banner can say what is
	// not firing rather than only that something is not.
	Lanes          int
	NotDispatching bool
	// OpenWork is how many items are not in a terminal state.
	OpenWork int
	// Stalled is open work that nothing in the system can pick up. Zero is the
	// normal state and any other number is a fault, not a queue.
	Stalled int
	Stuck   int
	// ConsolePending are actions the console drafted and left for a person.
	// They belong in the same count as everything else waiting on you: an
	// action nobody presses is work the console believes it has handed over.
	ConsolePending int
	Tampered       bool
}

func (a attention) Any() bool {
	return a.Questions > 0 || a.Approvals > 0 || a.Blocked > 0 ||
		a.DarkLoops > 0 || a.Stuck > 0 || a.ConsolePending > 0 || a.Tampered ||
		a.NotDispatching || a.Stalled > 0
}

func (s *Server) page(name string, fn func(*http.Request) (string, any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title, body, err := fn(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d, err := s.shell(r, name, title, body)
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

func (s *Server) shell(r *http.Request, page, title string, body any) (*pageData, error) {
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
		if !authority.State(it.State).Terminal() {
			attn.OpenWork++
		}
		switch authority.State(it.State) {
		case authority.StateBlocked:
			attn.Blocked++
		case authority.StateRejected:
			if it.Attempts >= s.Cfg.Dispatch.MaxAttempts {
				attn.Stuck++
			}
		}
	}
	if s.Cfg.Console.Enabled {
		turns, _ := s.Led.ConsoleHistory(s.sessionID(), s.Cfg.Console.HistoryTurns)
		for _, t := range turns {
			for _, a := range t.Actions {
				if a.Outcome == ledger.ActionPending {
					attn.ConsolePending++
				}
			}
		}
	}
	if s.Sched != nil {
		if hs, err := s.Sched.Health(s.now()); err == nil {
			for _, h := range hs {
				if !h.Enabled {
					continue
				}
				attn.Lanes++
				if !h.Fresh(s.now(), 3) {
					attn.DarkLoops++
				}
			}
		}
	}
	// "Lanes are not firing" and "nobody in this process is firing them" are
	// different facts, and only one of them is a fault. Reporting the first when
	// the second is true sends somebody looking for a broken lane.
	attn.NotDispatching = !s.Dispatching && attn.Lanes > 0
	if attn.NotDispatching {
		attn.DarkLoops = 0
	}

	// A fleet that is stuck looked exactly like a fleet that is idle.
	//
	// A validator rejected a breakdown, the deliverable went back a state with
	// its rejected items still counting as open work, and the rule that decides
	// what to pick up then had nothing to offer — forever. Every lane went on
	// ticking, every liveness figure stayed green, and the banner said "nothing
	// needs you, the fleet is working" for half an hour over four items that
	// nothing in the system was ever going to touch again.
	//
	// Waiting on a PERSON is not that. An unanswered blocking question or an
	// undecided approval is the fleet working exactly as designed, and it is
	// already reported as something waiting on you. Calling it stuck as well
	// would teach somebody to ignore the word on the one occasion it means what
	// it says.
	//
	// The question is asked through the dispatcher's own selection rule rather
	// than a second copy of it here, because a stall detector that disagrees
	// with the thing it is watching is worse than none.
	waitingOnAPerson := attn.Questions > 0 || attn.Approvals > 0
	if s.Sched != nil && s.Sched.D != nil && attn.OpenWork > 0 &&
		!waitingOnAPerson && !s.anythingRunning() {
		if cands, cerr := s.Sched.D.Candidates(dispatch.Filter{}); cerr == nil && len(cands) == 0 {
			attn.Stalled = attn.OpenWork
		}
	}
	// The nav has two shapes. On Home it is one way out and nothing else: most
	// of what somebody wants is answered by asking, and a row of eleven tabs is
	// what makes a tool look like it has to be learned before it can be used.
	// Everywhere else it is the full set, because the moment you are on one of
	// those pages is the moment you want to move between them.
	nav := []navItem{{Href: "/", Label: "Home"}}
	if page == "home" {
		nav = append(nav, navItem{Href: "/advanced", Label: "Advanced",
			Count: attn.Questions + attn.Approvals,
			Alarm: attn.Questions+attn.Approvals > 0})
	} else {
		nav = append(nav,
			navItem{Href: "/overview", Label: "Overview"},
			navItem{Href: "/roadmap", Label: "Roadmap"},
			navItem{Href: "/progress", Label: "Progress"},
			navItem{Href: "/questions", Label: "Questions", Count: attn.Questions, Alarm: attn.Questions > 0},
			navItem{Href: "/approvals", Label: "Approvals", Count: attn.Approvals, Alarm: attn.Approvals > 0},
			navItem{Href: "/coordination", Label: "Coordination"},
			navItem{Href: "/roles", Label: "Roles"},
			navItem{Href: "/config", Label: "Config", Count: attn.DarkLoops, Alarm: attn.DarkLoops > 0},
			navItem{Href: "/history", Label: "History"},
			navItem{Href: "/about", Label: "About"},
		)
	}
	for i := range nav {
		nav[i].Active = (page == "home" && nav[i].Href == "/") ||
			(nav[i].Href != "/" && strings.HasPrefix(nav[i].Href, "/"+page))
	}
	refresh := s.Cfg.Server.RefreshSeconds
	switch {
	case s.turns.any() && (refresh == 0 || refresh > 2):
		// Something is running and the answer is worth waiting for.
		refresh = 2
	case page == "home" || page == "console" || page == "questions" || page == "approvals":
		// These pages are forms somebody types into, and a page that reloads
		// itself throws away what they had half-written. Answering a question
		// means typing a reason, and the reason is the part that is useful in
		// six months — losing it to a refresh teaches people to type "ok".
		//
		// Nothing on them changes usefully on its own either: a question that
		// arrives while you are answering another one can wait for the reload
		// that answering causes.
		refresh = 0
	}
	return &pageData{
		Page: page, Title: title, Refresh: refresh, Project: s.Cfg.Project,
		Verdict: string(rep.Verdict), HeadSeq: seq, Attn: attn, Body: body,
		Now: s.now().Format("15:04:05"), Nav: nav, Dock: s.dock(r),
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
	Active []activeRun
	Recent []recentRun
	Stages []stageCount
	// Deliverables are the roadmap stages that hold something. Work exists
	// before any work item does, and a row that shows only items reads as an
	// idle fleet while a researcher is running.
	Deliverables []segStage
	Spend        spend.Report
	Blobs        int
	Refusals     []ledger.Proposal
	// Cost is re-derived from each run's recorded usage rather than summed
	// from the stored column, so a project whose runs were unpriced when they
	// ran still reports a figure — and says which pricing it used.
	Cost CostView
}

type stageCount struct {
	authority.Stage
	N int
}

// segStage is one deliverable stage, counted.
//
// The item stages alone were the whole of "where the work is", and a
// deliverable being researched has no items yet — so every tile read zero
// while a researcher was running, and the row said the fleet was empty about a
// fleet that was working.
type segStage struct {
	Key   string
	Label string
	N     int
	Live  bool
}

type recentRun struct {
	ledger.Run
	When    string
	Cost    string
	Outcome string
	Class   string
	// Why is the recorded reason a run ended the way it did, when there is
	// one. An outcome with no cause is a figure somebody has to go and chase.
	Why string
}

func (s *Server) overview(*http.Request) (string, any, error) {
	v := overviewView{Cost: s.CostView()}
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
	segs, _ := s.Led.Segments()
	bySeg := map[string]int{}
	for _, sg := range segs {
		bySeg[sg.State]++
	}
	for _, st := range authority.SegmentStages() {
		n := bySeg[string(st)]
		if n == 0 {
			continue
		}
		// Only the stages holding something. Eleven tiles of zero is not a
		// picture of the roadmap, it is a picture of the state machine.
		v.Deliverables = append(v.Deliverables, segStage{
			Key: string(st), Label: st.Label(), N: n,
			Live: st == authority.SegResearching || st == authority.SegPlanning ||
				st == authority.SegValidating || st == authority.SegBuilding,
		})
	}
	for _, st := range authority.Stages() {
		v.Stages = append(v.Stages, stageCount{Stage: st, N: byStage[st.Key]})
	}

	runs, _ := s.Led.Runs("", 25)
	for _, r := range runs {
		rr := recentRun{Run: r, Cost: spend.Micros(r.CostMicros).String(),
			When: time.Since(time.UnixMilli(r.StartedMS)).Round(time.Second).String() + " ago"}
		limit := time.Duration(s.Cfg.Dispatch.TimeoutSeconds) * time.Second
		if ab, ok, aerr := s.Led.Abandonment(r.RunID); aerr == nil && ok {
			// Not a bare "unknown". The verdict is unknown, and the reason is
			// not — so the reason is what a person reads.
			rr.Why = ab.Why
		}
		switch {
		case rr.Why != "":
			rr.Outcome, rr.Class = "abandoned", "warn"
		case r.StandingAt(s.now(), limit) == ledger.StandingWorking:
			// An agent inside its timeout is working, not missing.
			rr.Outcome, rr.Class = "running", "live"
		case !r.Finished():
			rr.Outcome, rr.Class = "UNKNOWN — open past twice the timeout, so nobody will record how it ended", "warn"
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

type progressView struct {
	// Stage is the tile that was clicked, and Drill is what is behind it.
	Stage      string
	StageLabel string
	Drill      []itemLine

	Rows    []roadmapRow
	Stages  []authority.Stage
	Totals  map[string]int
	Overall authority.SegmentProgress
}

// progress rolls the whole fleet up, and drills into one stage when asked.
//
// The tiles are counts of something. A count you cannot click is a number that
// makes you go and find the rows yourself, which on any real backlog means
// nobody does — so each one leads to the items behind it.
func (s *Server) progress(r *http.Request) (string, any, error) {
	segs, _ := s.Led.Segments()
	all, _ := s.Led.Items("")
	v := progressView{Stages: authority.Stages(), Totals: map[string]int{},
		Overall: authority.Progress(all), Stage: r.URL.Query().Get("stage")}
	for _, sg := range segs {
		items, _ := s.Led.Items(sg.ID)
		v.Rows = append(v.Rows, roadmapRow{Segment: sg, Progress: authority.Progress(items), Stage: sg.State})
	}
	for _, it := range all {
		v.Totals[authority.StageOf(authority.State(it.State)).Key]++
	}
	byID := map[string]authority.State{}
	for _, it := range all {
		byID[it.ID] = authority.State(it.State)
	}
	if v.Stage != "" {
		for _, st := range authority.Stages() {
			if st.Key == v.Stage {
				v.StageLabel = st.Label
			}
		}
		for _, it := range all {
			if authority.StageOf(authority.State(it.State)).Key != v.Stage {
				continue
			}
			state := authority.State(it.State)
			line := itemLine{Item: it, Stage: v.StageLabel,
				Says: humanState(state), Class: itemClass(state)}
			if state == authority.StateQueued {
				// Name them. The page knows which and knows their states.
				line.Says, line.Waiting = blockedOn(it, byID)
			}
			v.Drill = append(v.Drill, line)
		}
		sort.Slice(v.Drill, func(i, j int) bool { return v.Drill[i].ID < v.Drill[j].ID })
	}
	return "Progress", v, nil
}

// questionRow is one blocking question with the context needed to answer it.
//
// The page used to show the question's text, the agent's recommendation and a
// box. That is enough to answer a question you already understand and not
// enough to answer one you are meeting for the first time: what work it came
// out of, what that work is for, and what stops until you reply were all ids
// you had to go and look up, which in practice means nobody did.
type questionRow struct {
	ledger.Question
	// ItemTitle and SegmentTitle name the work rather than identify it. An id
	// is not context.
	ItemTitle    string
	SegmentID    string
	SegmentTitle string
	// Blocked is what stands still until this is answered, in a sentence.
	Blocked string
	// Lean is repeated here only so the accept control can send it back
	// verbatim without the page having to re-type it.
	HasLean bool
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
	rows := make([]questionRow, 0, len(qs))
	for _, q := range qs {
		row := questionRow{Question: q, HasLean: strings.TrimSpace(q.Lean) != ""}
		if q.ItemID != "" {
			if it, ierr := s.Led.Item(q.ItemID); ierr == nil {
				row.ItemTitle = it.Title
				row.SegmentID = it.SegmentID
				if seg, serr := s.Led.Segment(it.SegmentID); serr == nil {
					row.SegmentTitle = seg.Title
				}
			}
		}
		switch {
		case !q.Blocking:
			row.Blocked = "Nothing is waiting. This was raised so the decision is on the record rather than made quietly inside a run."
		case row.ItemTitle != "":
			row.Blocked = "Nothing moves on " + q.ItemID + " (" + row.ItemTitle + ") until this is answered."
		default:
			row.Blocked = "The work this came out of is stopped until this is answered."
		}
		rows = append(rows, row)
	}
	return "Questions", struct{ Items []questionRow }{rows}, nil
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

type historyRun struct {
	ledger.Run
	Headline string
	When     string
	Cost     string
	Class    string
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

// answer records a decision on a question.
//
// Three shapes, because a question with a recommendation attached is usually
// answered by accepting or rejecting it, and a free-text box made both of those
// into a retyping exercise. Whichever is pressed, what lands on the record says
// which happened: "a person decided this" and "a person agreed with what the
// agent proposed" are different facts, and a record that renders them the same
// cannot answer why a decision was made.
func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/questions", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	text := strings.TrimSpace(r.FormValue("answer"))
	why := strings.TrimSpace(r.FormValue("why"))
	choice := strings.TrimSpace(r.FormValue("choice"))
	who := strings.TrimSpace(r.FormValue("who"))
	if id == "" {
		redirect(w, r, "/questions", "an answer needs to say which question it is about", true)
		return
	}
	// A name is required rather than defaulted: the record names whoever
	// decided, and a decision attributed to whatever the process was started as
	// answers that question with the name of a service.
	if who == "" {
		redirect(w, r, "/questions", "say who you are — an answer nobody is named on cannot be weighed later", true)
		return
	}
	q, err := s.Led.Question(id)
	if err != nil {
		redirect(w, r, "/questions", err.Error(), true)
		return
	}
	if q.Answered {
		redirect(w, r, "/questions", id+" was already answered; reload and read what it says", true)
		return
	}

	var recorded string
	switch choice {
	case "accept":
		if strings.TrimSpace(q.Lean) == "" {
			redirect(w, r, "/questions", "there is no recommendation to accept on "+id, true)
			return
		}
		// Required HERE and not on a free-text answer. Pressing accept
		// contributes nothing of your own to the record — without a reason the
		// chain would say a person agreed and be unable to say why anybody
		// thought it was right. A written answer already is the reasoning, and
		// demanding it twice is friction that teaches people to type "ok".
		if why == "" {
			redirect(w, r, "/questions", "say why you are accepting it — otherwise the record shows agreement with no reasoning behind it, which is the part that is useful in six months", true)
			return
		}
		// The agent's words, quoted as its words, with the person's reasoning
		// under them. Recording the lean alone would leave the record unable to
		// say whether anybody had actually read it.
		recorded = "Accepted the recommendation as raised.\n\n" + q.Lean + "\n\nWhy: " + why
	case "reject":
		if text == "" {
			redirect(w, r, "/questions", "rejecting the recommendation needs the decision you are making instead", true)
			return
		}
		recorded = "Did NOT take the recommendation.\n\n" + text
	default:
		if text == "" {
			redirect(w, r, "/questions", "an answer needs some text", true)
			return
		}
		// Verbatim, and nothing appended when there is nothing to append: a
		// written answer IS the reasoning, and demanding it twice is friction
		// that teaches people to type "ok".
		recorded = text
	}
	if why != "" && choice != "accept" {
		recorded += "\n\nWhy: " + why
	}

	// Recorded verbatim. Summarising a human's note loses the reasoning that
	// sets the severity of everything decomposed from it.
	if _, err := s.Led.Append(who, ledger.KindQuestionAnswered, id, ledger.QuestionAnswered{
		ID: id, Answer: recorded, AnsweredBy: who,
	}); err != nil {
		redirect(w, r, "/questions", err.Error(), true)
		return
	}
	redirect(w, r, "/questions", "answered "+id+" — the run it stopped is dispatchable again", false)
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

// anythingRunning reports whether an agent is working right now.
//
// A stall is open work that nothing can pick up. Work waiting on an agent that
// is already running is not a stall, it is a queue, and calling it one would
// train somebody to ignore the warning.
func (s *Server) anythingRunning() bool {
	active, err := s.Led.ActiveRuns()
	if err != nil {
		// Unknown, so say nothing rather than raise an alarm on a failed read.
		return true
	}
	return len(active) > 0
}
