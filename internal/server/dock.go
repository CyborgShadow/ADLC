package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/console"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// The dock is the console panel that rides on every page.

// dockCookie remembers whether the panel is open.
//
// A cookie rather than JavaScript, and rather than a query parameter on every
// link. The panel has to survive navigation, the page's own fifteen-second
// refresh, and closing the browser — a query parameter survives none of those
// without rewriting every link on the site, and this dashboard exists to work
// when things are going wrong, which is the worst time to depend on a
// mechanism with more moving parts than the thing it displays.
const dockCookie = "adlc_console"

// dockTurns is how much of the conversation the panel carries. It is small on
// purpose: the panel is for the exchange you are having, and the whole
// transcript is a page.
const dockTurns = 6

type dockAction struct {
	ledger.ConsoleAction
	Pressable bool
	// Human is the action in a person's words. The machine name is what the
	// agent writes and what the record keeps; nobody operating this should have
	// to read it.
	Human string
}

type dockTurn struct {
	ledger.ConsoleTurn
	Running bool
	Actions []dockAction
}

type dockData struct {
	// Show is false when the console is off in config — no panel, no tab, and
	// no hint of a feature this project has not switched on.
	Show      bool
	Open      bool
	Blocked   string
	Authority string
	Pending   int
	Turns     []dockTurn
	// Here is the path to come back to after asking, so the answer arrives on
	// the page the question was about.
	Here      string
	OpenHref  string
	CloseHref string
}

func (s *Server) dock(r *http.Request) dockData {
	// Never on Home: that page IS the console, and a floating copy of it in the
	// corner of itself gives one conversation two input boxes.
	d := dockData{Show: s.Cfg.Console.Enabled && r.URL.Path != "/"}
	if !d.Show {
		return d
	}
	here := r.URL.RequestURI()
	if here == "" {
		here = "/"
	}
	d.Here = here
	d.Authority = string(s.Cfg.Console.Authority)
	d.OpenHref = "/console/toggle?open=1&back=" + url.QueryEscape(here)
	d.CloseHref = "/console/toggle?open=0&back=" + url.QueryEscape(here)

	if c, err := r.Cookie(dockCookie); err == nil && c.Value == "open" {
		d.Open = true
	}
	if s.runner() == nil {
		d.Blocked = "No agent runner is configured, so a turn has nothing to invoke."
	} else if s.Lib == nil {
		d.Blocked = "No prompt library is loaded."
	} else if _, err := s.Lib.Get(s.consolePrompt()); err != nil {
		d.Blocked = "The console prompt is missing from the library."
	}

	turns, err := s.Led.ConsoleHistory(s.sessionID(), dockTurns)
	if err != nil {
		d.Blocked = err.Error()
		return d
	}
	for _, t := range turns {
		row := dockTurn{ConsoleTurn: t, Running: t.Pending() && s.turns.running(t.TurnID)}
		for _, a := range t.Actions {
			p := a.Outcome == ledger.ActionPending
			if p {
				d.Pending++
			}
			row.Actions = append(row.Actions, dockAction{
				ConsoleAction: a, Pressable: p,
				Human: console.ActionKind(a.Kind).Human(),
			})
		}
		d.Turns = append(d.Turns, row)
	}
	return d
}

// toggleDock opens or shuts the panel and returns to where you were.
func (s *Server) toggleDock(w http.ResponseWriter, r *http.Request) {
	open := r.URL.Query().Get("open") == "1"
	value := "shut"
	if open {
		value = "open"
	}
	http.SetCookie(w, &http.Cookie{
		Name: dockCookie, Value: value, Path: "/",
		MaxAge: 60 * 60 * 24 * 365, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeBack(r.URL.Query().Get("back")), http.StatusSeeOther)
}

// safeBack keeps a redirect target inside this dashboard.
//
// The value arrives in a request, and a redirect target taken from a request is
// how an open redirect happens. This surface is loopback-only, so the exposure
// is small — but "small" is not a reason to hand an attacker a working
// primitive, and the check is one line.
func safeBack(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return "/"
	}
	return v
}
