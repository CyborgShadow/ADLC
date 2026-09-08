package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/dispatch"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/gate"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// ---------------------------------------------------------------- gate

func cmdGate(e *env, args []string) int {
	if len(args) == 0 || args[0] != "run" {
		return fail("gate needs a subcommand: run")
	}
	fs := sub("gate run")
	dir := fs.String("workdir", e.repo, "the tree to run the checks in")
	edgeFrom := fs.String("from", "", "restrict to the checks required for an edge")
	edgeTo := fs.String("to", "", "the other end of that edge")
	artifact := fs.String("artifact", "", "digest a behavioural check runs against")
	if fs.Parse(args[1:]) != nil {
		return exitUsage
	}
	r := &gate.Runner{Cfg: e.cfg, Dir: *dir, Artifact: *artifact, Vars: map[string]string{"workdir": *dir}}
	ctx := context.Background()

	var res *gate.Result
	var err error
	if *edgeFrom != "" && *edgeTo != "" {
		res, err = r.RunEdge(ctx, *edgeFrom, *edgeTo)
	} else {
		res, err = r.RunAll(ctx)
	}
	if err != nil {
		return fail("%v", err)
	}
	if e.jsonOut {
		emit(res)
	} else {
		printGate(res)
	}
	switch {
	case res.Status == gate.StatusRed:
		return exitGateRed
	case res.Status == gate.StatusUnknown || res.NoChecksDeclared:
		return exitGateUnknown
	}
	return exitOK
}

func printGate(res *gate.Result) {
	fmt.Printf("GATE %s — %s\n", res.Status, res.Summary())
	fmt.Printf("  tree      %s%s\n", shortHash(res.TreeSHA), dirtyNote(res))
	if res.Artifact != "" {
		fmt.Printf("  artifact  %s\n", shortHash(res.Artifact))
	}
	fmt.Println()
	for _, c := range res.Checks {
		fmt.Printf("  %-8s %-14s %-16s %s\n", c.Verdict, c.CheckID, c.Rule, c.Why)
	}
	if res.NoChecksDeclared {
		fmt.Println("\n  Nothing ran. A gate with no checks declared for this edge reports green over")
		fmt.Println("  nothing, so it reports UNKNOWN instead.")
	}
	fmt.Println()
}

func dirtyNote(res *gate.Result) string {
	if !res.Dirty {
		return ""
	}
	return fmt.Sprintf("  DIRTY — %d uncommitted path(s) in the declared source roots; evidence over an uncommitted tree describes a state nobody can check out again",
		len(res.DirtyPaths))
}

// ---------------------------------------------------------------- transition

func cmdTransition(e *env, args []string) int {
	if len(args) == 0 {
		return fail("transition needs a subcommand: propose | table")
	}
	if args[0] == "table" {
		printTable()
		return exitOK
	}
	if args[0] != "propose" {
		return fail("transition: unknown subcommand %q", args[0])
	}

	fs := sub("transition propose")
	item := fs.String("item", "", "work item id")
	to := fs.String("to", "", "the state proposed")
	runID := fs.String("run", "", "the run proposing it")
	worker := fs.String("worker", "", "worker type, omitted when the coordinator proposes")
	asPM := fs.Bool("pm", false, "the coordinating actor proposing in its own name")
	envPath := fs.String("envelope", "", "path to the run's envelope")
	dir := fs.String("workdir", e.repo, "the tree the gate runs in")
	reason := fs.String("reason", "", "why — required on every edge a human drives")
	if fs.Parse(args[1:]) != nil {
		return exitUsage
	}
	if !need("transition propose", *item != "" && *to != "", "-item and -to are required") {
		return exitUsage
	}

	from, err := stateOf(e, *item)
	if err != nil {
		return fail("%v", err)
	}
	var env *envelope.Envelope
	if *envPath != "" {
		if env, err = readEnvelope(*envPath); err != nil {
			// A malformed envelope is a refusable proposal, recorded as one — not a
			// dead run whose work is thrown away over an encoding fault.
			recordRefusal(e, *runID, *item, *worker, string(from), *to, authority.ReasonMalformedEnvelope, err.Error())
			fmt.Fprintf(os.Stderr, "REFUSED [%s] %v\n", authority.ReasonMalformedEnvelope, err)
			return exitRefused
		}
	}

	artifact := ""
	if env != nil {
		artifact = env.Artifact
	}
	r := &gate.Runner{Cfg: e.cfg, Dir: *dir, Artifact: artifact, Vars: map[string]string{"workdir": *dir}}
	gres, err := r.RunEdge(context.Background(), string(from), *to)
	if err != nil {
		return fail("gate: %v", err)
	}
	if _, err := e.led.Append(e.actor, ledger.KindGateObserved, *runID, ledger.GateObserved{
		RunID: *runID, ItemID: *item, Edge: gres.Edge, TreeSHA: gres.TreeSHA,
		Dirty: gres.Dirty, Artifact: gres.Artifact, Status: string(gres.Status), Checks: gres.JSON(),
	}); err != nil {
		return fail("%v", err)
	}

	headSHA := ""
	if env != nil {
		headSHA = env.HeadSHA
	}
	facts, err := authority.Gather(e.led, *item, *runID, func(sha string) bool {
		return gate.CommitExists(e.repo, sha)
	}, headSHA, nowUTC())
	if err != nil {
		return fail("%v", err)
	}
	dec := authority.New(e.cfg).Decide(authority.Request{
		Actor: e.actor, Worker: *worker, AsPM: *asPM, RunID: *runID,
		From: from, To: authority.State(*to), Env: env, Gate: gres,
		Reason: *reason, Now: nowUTC(),
	}, facts)

	if !dec.Admitted {
		recordRefusal(e, *runID, *item, *worker, string(from), *to, dec.Reason, dec.Detail)
		fmt.Fprintf(os.Stderr, "REFUSED  %s  %s -> %s\n", *item, from, *to)
		fmt.Fprintf(os.Stderr, "  [%s] %s\n", dec.Reason, dec.Detail)
		return exitRefused
	}

	if _, err := e.led.Append(e.actor, ledger.KindTransitionAdmitted, *item, ledger.TransitionOutcome{
		RunID: *runID, ItemID: *item, Worker: *worker, From: string(from), To: *to,
	}); err != nil {
		return fail("%v", err)
	}
	var planDigest, blockedWhy string
	if env != nil {
		planDigest = env.PlanDigest
		if authority.State(*to) == authority.StateBlocked {
			if q, ok := env.BlockingQuestion(); ok {
				blockedWhy = q.Text
			} else {
				blockedWhy = env.EnvironmentFailure
			}
		}
	}
	if _, err := e.led.Append(e.actor, ledger.KindItemTransitioned, *item, ledger.ItemTransitioned{
		ItemID: *item, From: string(from), To: *to, RunID: *runID, Reason: *reason,
		PlanDigest: planDigest, BlockedWhy: blockedWhy,
		BumpAttempt: authority.State(*to) == authority.StateInProgress && from != authority.StateReady,
	}); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("ADMITTED %s  %s -> %s\n", *item, from, *to)
	return exitOK
}

func recordRefusal(e *env, runID, item, worker, from, to string, reason authority.Reason, detail string) {
	_, _ = e.led.Append(e.actor, ledger.KindTransitionRefused, item, ledger.TransitionOutcome{
		RunID: runID, ItemID: item, Worker: worker, From: from, To: to,
		Reason: string(reason), Detail: detail,
	})
}

func printTable() {
	fmt.Println("TRANSITION AUTHORITY")
	fmt.Println("Everything not on this table is refused with no_such_edge.")
	fmt.Println()
	fmt.Printf("%-20s %-22s %-34s %s\n", "FROM", "TO", "MAY BE PROPOSED BY", "REQUIRES")
	for _, ed := range authority.Table() {
		var props, reqs []string
		for _, p := range ed.Proposers {
			props = append(props, string(p))
		}
		for _, r := range ed.Requires {
			reqs = append(reqs, string(r))
		}
		fmt.Printf("%-20s %-22s %-34s %s\n", ed.From, ed.To,
			joinNonEmpty(props, ", "), joinNonEmpty(reqs, ", "))
		if ed.Doc != "" && ed.Doc != "same" {
			fmt.Printf("%-20s   %s\n", "", ed.Doc)
		}
	}
}

// ---------------------------------------------------------------- dispatch

func cmdDispatch(e *env, args []string) int {
	if len(args) == 0 {
		return fail("dispatch needs a subcommand: plan | once | loop")
	}
	lib := e.lib
	if lib == nil {
		l, err := prompt.Load(e.cfg.Prompts)
		if err != nil {
			return fail("prompt library: %v", err)
		}
		lib = l
	}
	d := &dispatch.Dispatcher{
		Cfg: e.cfg, Led: e.led, Lib: lib, Leases: e.leases, Repo: e.repo,
		Actor: e.actor, Now: nowUTC,
		Runner: &dispatch.ExecRunner{Command: e.cfg.Dispatch.Command},
		Log:    func(s string) { fmt.Println(s) },
	}

	fs := sub("dispatch")
	capability := fs.String("capability", "", "restrict to implement|verify|validate|operate|generate")
	worker := fs.String("worker", "", "restrict to one declared worker type")
	areaCSV := fs.String("areas", "", "comma-separated areas to restrict to")
	every := fs.Duration("every", 60*time.Second, "loop: how often to look for work")
	maxRuns := fs.Int("max", 0, "loop: stop after this many dispatches (0 = until interrupted)")
	if fs.Parse(args[1:]) != nil {
		return exitUsage
	}
	flt := dispatch.Filter{Capability: *capability, Worker: *worker}
	if *areaCSV != "" {
		flt.Areas = strings.Split(*areaCSV, ",")
	}

	switch args[0] {
	case "plan":
		cands, err := d.Candidates(flt)
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(cands)
		}
		if len(cands) == 0 {
			fmt.Println("nothing is dispatchable: every item is done, blocked, awaiting approval, or past its rework limit")
			return exitOK
		}
		fmt.Println("DISPATCH ORDER — work nearest the finish line first, so verification never")
		fmt.Println("queues behind new implementation. That ordering is why the verification layer")
		fmt.Println("here cannot go dark the way a separately scheduled one did.")
		fmt.Println()
		for i, c := range cands {
			fmt.Printf("%2d. %-14s %-18s needs %-10s -> %s\n", i+1, c.Item.ID, c.From, c.Capability, c.Worker)
		}
		return exitOK

	case "once":
		res, err := d.TickScoped(context.Background(), flt)
		if err != nil {
			return fail("%v", err)
		}
		return reportTick(res)

	case "loop":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		n := 0
		for {
			res, err := d.TickScoped(ctx, flt)
			if err != nil {
				return fail("%v", err)
			}
			if res.Dispatched {
				n++
				if *maxRuns > 0 && n >= *maxRuns {
					fmt.Printf("stopping after %d dispatch(es)\n", n)
					return exitOK
				}
				continue // more work may be ready immediately
			}
			fmt.Printf("idle: %s\n", res.Idle)
			select {
			case <-ctx.Done():
				fmt.Println("stopped")
				return exitOK
			case <-time.After(*every):
			}
		}
	}
	return fail("dispatch: unknown subcommand %q", args[0])
}

func reportTick(res dispatch.TickResult) int {
	if !res.Dispatched {
		fmt.Printf("idle: %s\n", res.Idle)
		return exitOK
	}
	if res.Admitted {
		return exitOK
	}
	if res.Reason != "" {
		return exitRefused
	}
	return exitOK
}

// ---------------------------------------------------------------- helpers

func readEnvelope(path string) (*envelope.Envelope, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return envelope.Parse(b)
}

func costOf(e *env, model string, u ledger.Usage) (int64, bool) {
	c, ok := spend.Cost(e.cfg.Budget, model, u)
	return int64(c), ok
}
