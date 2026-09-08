package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/CyborgShadow/ADLC/internal/dispatch"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/server"
)

// ---------------------------------------------------------------- serve

func cmdServe(e *env, args []string) int {
	fs := sub("serve")
	addr := fs.String("addr", e.cfg.Server.Addr, "loopback address to serve the dashboard on")
	// Dispatching by default, because a control plane that is up and not running
	// anything is the failure this whole tool exists to make visible — and it
	// looked exactly like a working one. `serve` built a scheduler to READ lane
	// health and never started it, so every lane read NEVER RUN and nothing on
	// any surface said which process was supposed to be firing them.
	noDispatch := fs.Bool("no-dispatch", false,
		"serve the dashboard only; fire no lanes. The pages then say so, because a lane that is not firing and a lane nobody is firing are different facts")
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
	dispatching := sched != nil && !*noDispatch
	s := &server.Server{Cfg: e.cfg, Led: e.led, Sched: sched, Lib: e.lib,
		Actor: e.actor, Repo: e.repo, Now: nowUTC, Dispatching: dispatching,
		// Given explicitly rather than borrowed from the scheduler, so the console
		// still works when the scheduler could not be built.
		Runner: &dispatch.ExecRunner{Command: e.cfg.Dispatch.Command},
		Log:    func(s string) { fmt.Println(nowUTC().Format("15:04:05") + "  " + s) }}

	// The dashboard watches the fleet's runs. Attached after the server exists,
	// and only in the process that is actually dispatching — a page cannot show
	// output from an agent some other process is running.
	if dispatching && sched.D != nil {
		sched.D.Watch = s
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("dashboard on http://%s  (loopback only, no authentication — those two go together)\n", ln.Addr())
	switch {
	case dispatching:
		for _, l := range sched.Enabled() {
			fmt.Printf("  lane %-20s every %4ds  scope %s\n", l.Name, l.EverySeconds, l.Scope())
		}
		go func() {
			if err := sched.Run(ctx); err != nil && ctx.Err() == nil {
				fmt.Println(nowUTC().Format("15:04:05") + "  SCHEDULER STOPPED: " + err.Error())
			}
		}()
	case sched == nil:
		fmt.Println("no scheduler could be built from this config, so no lane will fire from here")
	default:
		fmt.Println("--no-dispatch: no lane will fire from this process, and the pages say so")
	}
	reportAbandoned(e)
	if err := s.Serve(ctx, ln); err != nil {
		return fail("%v", err)
	}
	fmt.Println("stopped")
	return exitOK
}

// reportAbandoned names the runs nobody is going to finish.
//
// A control plane killed outright cannot record an end for the agents it
// started, and no later process can either — it has no way to know what they
// did. So nothing is written to the chain about them: a synthetic ending
// invented by a process that was not there is exactly the kind of figure this
// tool refuses to hold, and "UNKNOWN" is the honest answer.
//
// But an operator starting the fleet back up should be told, because those
// agents may still be alive, still spending, and still holding a lease on the
// item a fresh run is about to be handed.
func reportAbandoned(e *env) {
	timeout := time.Duration(e.cfg.Dispatch.TimeoutSeconds) * time.Second
	runs, err := e.led.Runs("", 200)
	if err != nil {
		return
	}
	n := 0
	for _, r := range runs {
		if r.StandingAt(nowUTC(), timeout) != ledger.StandingAbandoned {
			continue
		}
		if n == 0 {
			fmt.Println()
			fmt.Println("OPEN RUNS NOBODY WILL FINISH — started, never ended, past twice the timeout.")
			fmt.Println("Their outcome is UNKNOWN and will stay that way. Check whether the agent is")
			fmt.Println("still running before more work is started on the same item.")
		}
		n++
		fmt.Printf("  %-26s %-16s %-14s open %s\n", r.RunID, r.WorkerType, r.ItemID,
			nowUTC().Sub(time.UnixMilli(r.StartedMS)).Round(time.Second))
	}
	if n > 0 {
		fmt.Println()
	}
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
				Actor: e.actor, Repo: e.repo, Now: nowUTC,
				// Given explicitly rather than borrowed from the scheduler, so the console
				// still works when the scheduler could not be built.
				Runner: &dispatch.ExecRunner{Command: e.cfg.Dispatch.Command},
				Log:    func(s string) { fmt.Println(nowUTC().Format("15:04:05") + "  " + s) }}
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
	s := &dispatch.Scheduler{D: d, Cfg: e.cfg, Grace: 3}
	// Every writer in this process shares one ledger — the lanes, the dashboard
	// and the console alike — so wiring the wake here means a sign-off pressed on
	// a page starts the next lane at once rather than whenever its timer next
	// came round. The lane still re-derives what to do from the record; the wake
	// only decides WHEN it looks.
	s.WakeOn(e.led)
	return s, nil
}
