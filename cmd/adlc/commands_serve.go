package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/CyborgShadow/adlc/internal/dispatch"
	"github.com/CyborgShadow/adlc/internal/prompt"
	"github.com/CyborgShadow/adlc/internal/server"
)

// ---------------------------------------------------------------- serve

func cmdServe(e *env, args []string) int {
	fs := sub("serve")
	addr := fs.String("addr", e.cfg.Server.Addr, "loopback address to serve the dashboard on")
	if fs.Parse(args) != nil {
		return exitUsage
	}
	ln, err := server.Bind(*addr)
	if err != nil {
		return fail("%v", err)
	}
	defer ln.Close()

	sched, _ := e.scheduler()
	if e.lib == nil {
		if lib, lerr := prompt.Load(e.cfg.Prompts); lerr == nil {
			e.lib = lib
		}
	}
	s := &server.Server{Cfg: e.cfg, Led: e.led, Sched: sched, Lib: e.lib,
		Actor: e.actor, Repo: e.repo, Now: nowUTC}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("dashboard on http://%s  (loopback only, no authentication — those two go together)\n", ln.Addr())
	fmt.Println("it answers questions and decides approvals; everything else it shows is read from the ledger")
	if err := s.Serve(ctx, ln); err != nil {
		return fail("%v", err)
	}
	fmt.Println("stopped")
	return exitOK
}

// ---------------------------------------------------------------- schedule

func cmdSchedule(e *env, args []string) int {
	if len(args) == 0 {
		return fail("schedule needs a subcommand: run | status | once")
	}
	sched, err := e.scheduler()
	if err != nil {
		return fail("%v", err)
	}

	switch args[0] {
	case "run":
		fs := sub("schedule run")
		serve := fs.Bool("serve", true, "also serve the dashboard")
		addr := fs.String("addr", e.cfg.Server.Addr, "dashboard address")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		if *serve {
			ln, err := server.Bind(*addr)
			if err != nil {
				return fail("%v", err)
			}
			defer ln.Close()
			if e.lib == nil {
				if lib, lerr := prompt.Load(e.cfg.Prompts); lerr == nil {
					e.lib = lib
				}
			}
			s := &server.Server{Cfg: e.cfg, Led: e.led, Sched: sched, Lib: e.lib,
				Actor: e.actor, Repo: e.repo, Now: nowUTC}
			go func() { _ = s.Serve(ctx, ln) }()
			fmt.Printf("dashboard on http://%s\n", ln.Addr())
		}
		for _, l := range sched.Enabled() {
			fmt.Printf("loop %-22s every %4ds  offset %3ds  scope %s\n",
				l.Name, l.EverySeconds, l.OffsetSeconds, l.Scope())
		}
		fmt.Println("\nCtrl+C stops cleanly: each lane finishes the dispatch it is in and exits.")
		fmt.Println("Nothing is held in memory — every outcome is already on the ledger — so a restart resumes.")
		if err := sched.Run(ctx); err != nil && ctx.Err() == nil {
			return fail("%v", err)
		}
		fmt.Println("stopped")
		return exitOK

	case "once":
		fs := sub("schedule once")
		name := fs.String("loop", "", "the loop to fire")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		l := e.cfg.Loop(*name)
		if l == nil {
			return fail("no loop named %q is declared", *name)
		}
		res, err := sched.FireOnce(context.Background(), *l)
		if err != nil {
			return fail("%v", err)
		}
		return reportTick(res)

	case "status":
		hs, err := sched.Health(nowUTC())
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(hs)
		}
		fmt.Println("LOOP LIVENESS (derived from the tick record, not self-reported)")
		fmt.Println("Every firing writes a tick, including idle ones, so a loop that has quietly")
		fmt.Println("stopped reads STALE rather than looking like a loop with nothing to report.")
		fmt.Println()
		fmt.Printf("  %-22s %-10s %6s %6s %-12s %s\n", "loop", "status", "ticks", "disp", "last", "scope")
		now := nowUTC()
		for _, h := range hs {
			status := "live"
			switch {
			case !h.Enabled:
				status = "disabled"
			case h.LastTickMS == 0:
				status = "NEVER RUN"
			case !h.Fresh(now, 3):
				status = "STALE"
			}
			last := "—"
			if h.LastTickMS > 0 {
				last = now.Sub(time.UnixMilli(h.LastTickMS)).Round(time.Second).String()
			}
			fmt.Printf("  %-22s %-10s %6d %6d %-12s %s\n", h.Loop, status, h.Ticks, h.Dispatches, last, h.Scope)
		}
		return exitOK
	}
	return fail("schedule: unknown subcommand %q", args[0])
}

// scheduler builds a scheduler over this environment.
func (e *env) scheduler() (*dispatch.Scheduler, error) {
	lib := e.lib
	if lib == nil {
		l, err := prompt.Load(e.cfg.Prompts)
		if err != nil {
			return nil, fmt.Errorf("prompt library: %w", err)
		}
		lib = l
	}
	d := &dispatch.Dispatcher{
		Cfg: e.cfg, Led: e.led, Lib: lib, Leases: e.leases, Repo: e.repo,
		Actor: e.actor, Now: nowUTC,
		Runner: &dispatch.ExecRunner{Command: e.cfg.Dispatch.Command},
		Log:    func(s string) { fmt.Println(nowUTC().Format("15:04:05") + "  " + s) },
	}
	return &dispatch.Scheduler{D: d, Cfg: e.cfg, Grace: 3}, nil
}
