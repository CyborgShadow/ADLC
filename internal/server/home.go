package server

import (
	"net/http"
	"time"

	"github.com/CyborgShadow/ADLC/internal/console"
)

// Home is the console, and almost nothing else.
//
// Everything this dashboard shows is a projection of one ledger, and for most
// of what an operator wants — what needs me, why is this stopped, start this
// work — asking is faster than knowing which of ten pages holds the answer.
// The ten pages are still there and still authoritative; they are just no
// longer the first thing you have to learn.
//
// The rest of the dashboard moves behind Advanced rather than away, because the
// moment you need it is the moment something has gone wrong, and a surface that
// hides its own record at exactly that point is not a simplification.

// homeCard is one thing waiting for a person, surfaced on Home so that the
// simple page is not also the uninformed one.
type homeCard struct {
	Label string
	N     int
	Href  string
	Class string
}

type homeView struct {
	Turns   []consoleRow
	Blocked string
	// Authority is what the console may do on its own, shown plainly because it
	// changes what pressing Ask means.
	Authority string
	AuthWhat  string
	Worker    string
	Waiting   []homeCard
	// Quiet is true when nothing at all needs a person, which is worth saying
	// in one line rather than leaving as an absent list.
	Quiet   bool
	Vocab   []console.HumanAction
	Project string
	// Examples are complete questions rather than fragments, because each one
	// is a button that asks it — half a sentence cannot be submitted.
	Examples []string
}

func (s *Server) home(*http.Request) (string, any, error) {
	v := homeView{
		Authority: string(s.Cfg.Console.Authority),
		AuthWhat:  s.Cfg.Console.Authority.Describe(),
		Worker:    s.Cfg.Console.Worker,
		Vocab:     console.HumanVocabulary(s.Cfg.Console.Authority),
		Project:   s.Cfg.Project,
		Examples: []string{
			"What needs me right now?",
			"What is the fleet working on?",
			"Why is nothing moving?",
			"What has this cost so far?",
		},
	}
	if !s.Cfg.Console.Enabled {
		v.Blocked = "The console is off in this project's config. Turn it on under Advanced → Config, " +
			"or use the pages there directly."
	} else if s.runner() == nil {
		v.Blocked = "No agent runner is configured, so a question has nothing to invoke. " +
			"Start the fleet with `adlc schedule run`, or set dispatch.command in the config."
	} else if s.Lib != nil {
		if _, err := s.Lib.Get(s.consolePrompt()); err != nil {
			v.Blocked = "The console prompt is missing from the prompt library."
		}
	}

	turns, err := s.Led.ConsoleHistory(s.sessionID(), s.Cfg.Console.HistoryTurns)
	if err != nil {
		return "", nil, err
	}
	for _, t := range turns {
		row := consoleRow{ConsoleTurn: t, Asked: t.Asked, Reply: t.Reply}
		row.Running = t.Pending() && s.turns.running(t.TurnID)
		if row.Running {
			row.Waited = s.turns.since(t.TurnID, s.now()).Round(time.Second).String()
		}
		for _, a := range t.Actions {
			row.Actions = append(row.Actions, actionRow(a, "/"))
		}
		v.Turns = append(v.Turns, row)
	}

	// The short list of things that genuinely need a person. Home is the simple
	// page, not the one that hides a blocked fleet behind a chat box.
	qs, _ := s.Led.Questions("", true)
	blocking := 0
	for _, q := range qs {
		if q.Blocking {
			blocking++
		}
	}
	aps, _ := s.Led.Approvals("")
	pendingAp := 0
	for _, a := range aps {
		if !a.Decided() {
			pendingAp++
		}
	}
	segs, _ := s.Led.Segments()
	needSignOff := 0
	for _, sg := range segs {
		if sg.State == "roadmap" || sg.State == "theory" {
			needSignOff++
		}
	}
	add := func(label string, n int, href, class string) {
		if n > 0 {
			v.Waiting = append(v.Waiting, homeCard{Label: label, N: n, Href: href, Class: class})
		}
	}
	add("blocking questions", blocking, "/questions", "bad")
	add("waiting for approval", pendingAp, "/approvals", "warn")
	add("ideas to sign off", needSignOff, "/roadmap", "warn")
	v.Quiet = len(v.Waiting) == 0
	return "Home", v, nil
}

// advancedLink is one of the pages Home does not show.
type advancedLink struct {
	Href, Label, What string
	Count             int
	Alarm             bool
}

func (s *Server) advanced(*http.Request) (string, any, error) {
	// Built from the same attention counts the banner uses, so the index cannot
	// say a page is quiet while the banner says it is not.
	qs, _ := s.Led.Questions("", true)
	blocking := 0
	for _, q := range qs {
		if q.Blocking {
			blocking++
		}
	}
	aps, _ := s.Led.Approvals("")
	pending := 0
	for _, a := range aps {
		if !a.Decided() {
			pending++
		}
	}
	links := []advancedLink{
		{"/overview", "Overview", "What is running this second, where every work item sits, and what it has cost.", 0, false},
		{"/roadmap", "Roadmap", "The deliverables, what each is for, and the work still to do, in progress and finished.", 0, false},
		{"/progress", "Progress", "The fleet rolled up by stage, and every deliverable's completion.", 0, false},
		{"/questions", "Questions", "What an agent stopped on because it could not make the call itself.", blocking, blocking > 0},
		{"/approvals", "Approvals", "Changes that reach something real and need a named person to clear the exact plan.", pending, pending > 0},
		{"/coordination", "Coordination", "The fourteen handoffs in order, who owns each, and whether the lanes are firing.", 0, false},
		{"/roles", "Roles", "Every role, what it is given, and its prompt — readable and editable in place.", 0, false},
		{"/config", "Config", "Lanes, safety policy, spend caps, console authority and pricing.", 0, false},
		{"/history", "History", "Every run in plain language, and the raw hash-chained ledger behind it.", 0, false},
		{"/console", "Console", "The whole conversation, and everything you can ask the console to do.", 0, false},
		{"/about", "About the ADLC", "How the lifecycle works, how work actually moves, and how it is all stored.", 0, false},
	}
	return "Advanced", struct{ Links []advancedLink }{links}, nil
}

// home2 serves Home, and refuses anything else that fell through to the root.
//
// Go's mux matches "/" against every path nothing else claimed, so without this
// an unknown URL would render Home and report 200. A page that answers every
// wrong address is a page that hides every typo — including a link this
// dashboard wrote itself.
func (s *Server) home2(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.page("home", s.home)(w, r)
}
