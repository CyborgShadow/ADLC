package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// The Config page's write handlers.
//
// One handler per section rather than one form for the page. A single Save
// button over forty fields means every change carries every other change with
// it, so an operator adjusting a timeout during an incident also silently
// re-applies whatever was in the blast-radius boxes when the page loaded.

func (s *Server) saveConsoleCfg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/config", http.StatusSeeOther)
		return
	}
	auth := config.ConsoleAuthority(strings.TrimSpace(r.FormValue("authority")))
	enabled := r.FormValue("enabled") == "on"
	if err := s.Cfg.SetConsole(enabled, auth); err != nil {
		redirect(w, r, "/config", err.Error(), true)
		return
	}
	if !enabled {
		redirect(w, r, "/config", "the console is off; the panel is gone from every page", false)
		return
	}
	redirect(w, r, "/config", "console authority is now "+string(auth)+" — it "+auth.Describe(), false)
}

func (s *Server) saveBlastCfg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/config", http.StatusSeeOther)
		return
	}
	ttl, err := strconv.Atoi(strings.TrimSpace(r.FormValue("ttl")))
	if err != nil {
		redirect(w, r, "/config", "the approval lifetime has to be a number of minutes", true)
		return
	}
	if err := s.Cfg.SetBlast(
		config.Radius(r.FormValue("auto_apply_max")),
		config.Radius(r.FormValue("named_approver_min")),
		config.Radius(r.FormValue("two_approvals_min")),
		ttl,
	); err != nil {
		redirect(w, r, "/config", err.Error(), true)
		return
	}
	redirect(w, r, "/config", "safety policy saved — it applies to the next transition, not to anything already approved", false)
}

func (s *Server) saveBudgetCfg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/config", http.StatusSeeOther)
		return
	}
	// Entered in dollars because that is what a person thinks in; stored in
	// micros because money in floats drifts across thousands of runs.
	dollars := func(field string) (int64, error) {
		v := strings.TrimSpace(strings.TrimPrefix(r.FormValue(field), "$"))
		if v == "" {
			return 0, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, err
		}
		return int64(f * 1e6), nil
	}
	run, err1 := dollars("per_run")
	day, err2 := dollars("per_day")
	seg, err3 := dollars("per_segment")
	for _, err := range []error{err1, err2, err3} {
		if err != nil {
			redirect(w, r, "/config", "a cap has to be a number of dollars, or blank for unlimited", true)
			return
		}
	}
	if err := s.Cfg.SetBudget(run, day, seg, strings.TrimSpace(r.FormValue("model"))); err != nil {
		redirect(w, r, "/config", err.Error(), true)
		return
	}
	redirect(w, r, "/config", "spend caps saved", false)
}

func (s *Server) saveDispatchCfg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/config", http.StatusSeeOther)
		return
	}
	attempts, err1 := strconv.Atoi(strings.TrimSpace(r.FormValue("attempts")))
	timeout, err2 := strconv.Atoi(strings.TrimSpace(r.FormValue("timeout")))
	refresh, err3 := strconv.Atoi(strings.TrimSpace(r.FormValue("refresh")))
	if err1 != nil || err2 != nil || err3 != nil {
		redirect(w, r, "/config", "attempts, timeout and refresh all have to be numbers", true)
		return
	}
	if err := s.Cfg.SetDispatch(attempts, timeout); err != nil {
		redirect(w, r, "/config", err.Error(), true)
		return
	}
	if err := s.Cfg.SetServer(refresh); err != nil {
		redirect(w, r, "/config", err.Error(), true)
		return
	}
	redirect(w, r, "/config", "saved — the refresh rate applies when you next load a page", false)
}

func (s *Server) savePriceCfg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/config", http.StatusSeeOther)
		return
	}
	in, err1 := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(r.FormValue("in"), "$")), 64)
	out, err2 := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(r.FormValue("out"), "$")), 64)
	if err1 != nil || err2 != nil {
		redirect(w, r, "/config", "both rates have to be a number of dollars per million tokens", true)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if err := s.Cfg.SetPrice(model, in, out); err != nil {
		redirect(w, r, "/config", err.Error(), true)
		return
	}
	redirect(w, r, "/config",
		model+" is now priced from your own table rather than the built-in defaults", false)
}
