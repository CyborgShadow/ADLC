package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/lease"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/report"
)

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func sub(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("adlc "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// ---------------------------------------------------------------- init

func cmdInit(args []string) int {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return fail("%v", err)
	}
	led, err := ledger.Open(flagDB)
	if err != nil {
		return fail("open ledger: %v", err)
	}
	defer led.Close()
	actor := flagActor
	if actor == "" {
		actor = "cli"
	}

	// Seeding the registry from the declared config is what makes "never ran"
	// detectable. Registration cannot come from the runs, because a worker that
	// never runs contributes no rows to read.
	existing, err := led.Workers()
	if err != nil {
		return fail("%v", err)
	}
	have := map[string]bool{}
	for _, w := range existing {
		have[w.Type] = true
	}
	added := 0
	for _, w := range cfg.Workers {
		if have[w.Type] {
			continue
		}
		if _, err := led.Append(actor, ledger.KindWorkerRegistered, w.Type, ledger.WorkerRegistered{
			Type: w.Type, Layer: w.Layer, Description: w.Description,
			PromptID: w.Prompt, LowCadence: w.LowCadence,
		}); err != nil {
			return fail("register %s: %v", w.Type, err)
		}
		added++
	}
	seq, hash, _ := led.Head()
	fmt.Printf("ledger ready at %s\n", flagDB)
	fmt.Printf("  project        %s\n", cfg.Project)
	fmt.Printf("  workers        %d declared, %d newly registered\n", len(cfg.Workers), added)
	fmt.Printf("  checks         %d declared\n", len(cfg.Checks))
	fmt.Printf("  head           seq %d %s\n", seq, shortHash(hash))
	fmt.Printf("  auto-apply max %s (anything above stops for an approval)\n", cfg.Blast.AutoApplyMax)
	return exitOK
}

// ---------------------------------------------------------------- segment

func cmdSegment(e *env, args []string) int {
	if len(args) == 0 {
		return fail("segment needs a subcommand: create | list")
	}
	switch args[0] {
	case "create":
		fs := sub("segment create")
		id := fs.String("id", "", "segment id")
		title := fs.String("title", "", "one line describing the segment")
		brief := fs.String("brief", "", "the direction a generator decomposes into work items")
		rationale := fs.String("why", "", "why this deliverable matters, in the language of whoever wanted it")
		rank := fs.Int("rank", 0, "roadmap order; lower sorts first")
		target := fs.Int("target", 0, "how many unfinished items to keep in flight here (0 = hand-filled)")
		var deps listFlag
		fs.Var(&deps, "depends-on", "a segment this one depends on (repeatable)")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("segment create", *id != "" && *title != "", "-id and -title are required") {
			return exitUsage
		}
		if *target > 0 && strings.TrimSpace(*brief) == "" {
			return fail("-target %d with no -brief: a generator asked to keep work in flight with no direction will invent scope", *target)
		}
		if _, err := e.led.Append(e.actor, ledger.KindSegmentCreated, *id, ledger.SegmentCreated{
			ID: *id, Title: *title, Brief: *brief, Rationale: *rationale,
			Rank: *rank, TargetOpen: *target, DependsOn: deps,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("segment %s created\n", *id)
		return exitOK

	case "advance":
		return cmdSegmentAdvance(e, args[1:])

	case "list":
		segs, err := e.led.Segments()
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(segs)
		}
		for _, s := range segs {
			fmt.Printf("%-8s %-40s depends on %v\n", s.ID, s.Title, s.DependsOn)
		}
		return exitOK
	}
	return fail("segment: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- item

func cmdItem(e *env, args []string) int {
	if len(args) == 0 {
		return fail("item needs a subcommand: create | list | show | amend")
	}
	switch args[0] {
	case "create":
		fs := sub("item create")
		id := fs.String("id", "", "work item id — this is also its lease key, so it must be stable")
		seg := fs.String("segment", "", "segment id")
		title := fs.String("title", "", "one line describing the work")
		area := fs.String("area", "", "area tag, routed to an owning worker")
		radius := fs.String("radius", string(config.RadiusNone), "blast radius: none|host|fleet|region|global")
		var res, scope, deps, crit listFlag
		fs.Var(&res, "resource", "a resource this item changes, e.g. fleet:prod/host:web-01 (repeatable)")
		fs.Var(&scope, "file", "a path this item may edit (repeatable)")
		fs.Var(&deps, "depends-on", "an item this one waits on (repeatable)")
		fs.Var(&crit, "criterion", "an acceptance criterion, verifiable by a command (repeatable)")
		itemWhy := fs.String("why", "", "which part of the deliverable this item serves")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("item create", *id != "" && *seg != "" && *title != "", "-id, -segment and -title are required") {
			return exitUsage
		}
		if !config.Radius(*radius).Known() {
			return fail("radius %q is not one of %v", *radius, config.Radii())
		}
		if len(crit) == 0 {
			return fail("an item with no acceptance criteria cannot be verified by anyone, so it cannot be finished either. Give it at least one -criterion that a command can check")
		}
		if *area != "" {
			if _, ok := e.cfg.Owner(*area); !ok {
				return fail("area %q has no owning worker in the routing map. An item filed under an unowned area is unreachable, and an unreachable item looks exactly like one nobody has got round to", *area)
			}
		}
		if config.Radius(*radius) != config.RadiusNone && len(res) == 0 {
			return fail("blast radius %q with no -resource: the lease cannot protect a machine nobody named", *radius)
		}
		if _, err := e.led.Append(e.actor, ledger.KindItemCreated, *id, ledger.ItemCreated{
			ID: *id, SegmentID: *seg, Title: *title, Area: *area, Radius: *radius,
			Resources: res, FileScope: scope, DependsOn: deps, Criteria: crit, Rationale: *itemWhy,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("item %s created in %s (radius %s)\n", *id, *seg, *radius)
		return exitOK

	case "list":
		fs := sub("item list")
		seg := fs.String("segment", "", "restrict to one segment")
		state := fs.String("state", "", "restrict to one state")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		items, err := e.led.Items(*seg)
		if err != nil {
			return fail("%v", err)
		}
		var out []ledger.Item
		for _, it := range items {
			if *state == "" || it.State == *state {
				out = append(out, it)
			}
		}
		if e.jsonOut {
			return emit(out)
		}
		for _, it := range out {
			fmt.Printf("%-14s %-18s %-7s %s\n", it.ID, it.State, it.Radius, it.Title)
		}
		return exitOK

	case "show":
		if len(args) < 2 {
			return fail("item show <id>")
		}
		it, err := e.led.Item(args[1])
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(it)
		}
		fmt.Printf("%s  %s\n", it.ID, it.Title)
		fmt.Printf("  segment      %s\n", it.SegmentID)
		fmt.Printf("  state        %s\n", it.State)
		fmt.Printf("  blast radius %s\n", it.Radius)
		fmt.Printf("  resources    %v\n", it.Resources)
		fmt.Printf("  file scope   %v\n", it.FileScope)
		fmt.Printf("  depends on   %v\n", it.DependsOn)
		fmt.Printf("  attempts     %d of %d\n", it.Attempts, e.cfg.Dispatch.MaxAttempts)
		if it.PlanDigest != "" {
			fmt.Printf("  plan digest  %s\n", shortHash(it.PlanDigest))
		}
		if it.BlockedWhy != "" {
			fmt.Printf("  blocked      %s\n", it.BlockedWhy)
		}
		fmt.Println("  acceptance criteria:")
		for i, c := range it.Criteria {
			fmt.Printf("    AC-%d %s\n", i+1, c)
		}
		runs, _ := e.led.Runs(it.ID, 10)
		if len(runs) > 0 {
			fmt.Println("  recent runs:")
			for _, r := range runs {
				verdict := r.Verdict
				if !r.Finished() {
					verdict = "UNKNOWN (started, no end recorded)"
				}
				fmt.Printf("    %-26s %-16s %s\n", r.RunID, r.WorkerType, verdict)
			}
		}
		props, _ := e.led.Proposals(it.ID, true, 5)
		if len(props) > 0 {
			fmt.Println("  recent refusals:")
			for _, p := range props {
				fmt.Printf("    %s -> %s  [%s] %s\n", p.From, p.To, p.Reason, truncate(p.Detail, 90))
			}
		}
		return exitOK

	case "amend":
		fs := sub("item amend")
		id := fs.String("id", "", "item id")
		field := fs.String("field", "", "title|area|blast_radius|resources|file_scope|criteria|depends_on")
		value := fs.String("value", "", "new value; list fields take JSON")
		why := fs.String("why", "", "why this correction is being made")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("item amend", *id != "" && *field != "" && *why != "",
			"-id, -field and -why are required — a correction with no stated authority is indistinguishable from a rewrite") {
			return exitUsage
		}
		if _, err := e.led.Append(e.actor, ledger.KindItemAmended, *id, ledger.ItemAmended{
			ItemID: *id, Field: *field, Value: *value, Why: *why,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("amended %s.%s (appended, not edited)\n", *id, *field)
		return exitOK
	}
	return fail("item: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- run

func cmdRun(e *env, args []string) int {
	if len(args) == 0 {
		return fail("run needs a subcommand: start | finish | list | show")
	}
	switch args[0] {
	case "start":
		fs := sub("run start")
		id := fs.String("id", "", "run id — the same string as the workspace name and the lease key")
		worker := fs.String("worker", "", "worker type")
		item := fs.String("item", "", "work item id")
		base := fs.String("base", "", "commit the run starts from")
		dir := fs.String("workdir", "", "the run's isolated workspace")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("run start", *id != "" && *worker != "", "-id and -worker are required") {
			return exitUsage
		}
		if e.cfg.Worker(*worker) == nil {
			return fail("%q is not a declared worker type; declared: %v", *worker, workerNames(e.cfg))
		}
		taken, err := e.led.RunExists(*id)
		if err != nil {
			return fail("%v", err)
		}
		if taken {
			return fail("run id %s is already in the ledger. An id is checked when it is minted and again when work lands, because a duplicate makes two runs indistinguishable in the record", *id)
		}
		var segID string
		if *item != "" {
			it, err := e.led.Item(*item)
			if err != nil {
				return fail("%v", err)
			}
			segID = it.SegmentID
		}
		if _, err := e.led.Append(e.actor, ledger.KindRunStarted, *id, ledger.RunStarted{
			RunID: *id, WorkerType: *worker, ItemID: *item, SegmentID: segID,
			BaseSHA: *base, WorkDir: *dir, Model: e.cfg.Budget.DefaultModel,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("run %s started\n", *id)
		return exitOK

	case "finish":
		fs := sub("run finish")
		id := fs.String("id", "", "run id")
		verdict := fs.String("verdict", "", "pass|fail|reject|blocked")
		envPath := fs.String("envelope", "", "path to the run's envelope")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("run finish", *id != "", "-id is required") {
			return exitUsage
		}
		f := ledger.RunFinished{RunID: *id, Verdict: *verdict}
		if *envPath != "" {
			env, err := readEnvelope(*envPath)
			if err != nil {
				return fail("%v", err)
			}
			f.Verdict, f.EnvelopeSHA, f.HeadSHA, f.Artifact = env.Verdict, env.SHA(), env.HeadSHA, env.Artifact
			f.Usage = ledger.Usage(env.Usage)
			f.Model = env.Model
			if f.Model == "" {
				f.Model = e.cfg.Budget.DefaultModel
			}
			cost, priced := costOf(e, f.Model, f.Usage)
			f.CostMicros = cost
			if !priced {
				fmt.Fprintf(os.Stderr, "note: model %q has no price entry, so this run's cost is unknown rather than zero\n", f.Model)
			}
		}
		if _, err := e.led.Append(e.actor, ledger.KindRunFinished, *id, f); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("run %s finished: %s\n", *id, f.Verdict)
		return exitOK

	case "list":
		runs, err := e.led.Runs("", 50)
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(runs)
		}
		for _, r := range runs {
			v := r.Verdict
			if !r.Finished() {
				v = "UNKNOWN"
			}
			fmt.Printf("%-26s %-16s %-14s %-8s %s\n", r.RunID, r.WorkerType, r.ItemID, v,
				time.UnixMilli(r.StartedMS).UTC().Format(time.RFC3339))
		}
		return exitOK

	case "show", "steps", "replay", "retry", "prompt", "envelope":
		return cmdRunStory(e, args[0], args[1:])
	}
	return fail("run: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- question

func cmdQuestion(e *env, args []string) int {
	if len(args) == 0 {
		return fail("question needs a subcommand: raise | answer | list")
	}
	switch args[0] {
	case "raise":
		fs := sub("question raise")
		id := fs.String("id", "", "question id")
		item := fs.String("item", "", "work item this blocks, if any")
		text := fs.String("text", "", "the question, in plain language")
		lean := fs.String("lean", "", "the raiser's own recommendation, and why")
		evidence := fs.String("evidence", "", "what motivated the question")
		blocking := fs.Bool("blocking", false, "work cannot continue until this is answered")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("question raise", *id != "" && *text != "", "-id and -text are required") {
			return exitUsage
		}
		if strings.TrimSpace(*lean) == "" {
			return fail("-lean is required. A question with no recommendation hands back the analysis the run was dispatched to do, and it is the raiser who has the context")
		}
		if _, err := e.led.Append(e.actor, ledger.KindQuestionRaised, *id, ledger.QuestionRaised{
			ID: *id, ItemID: *item, Blocking: *blocking, Text: *text,
			Lean: *lean, Evidence: *evidence, RaisedBy: e.actor,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("question %s raised\n", *id)
		return exitOK

	case "answer":
		fs := sub("question answer")
		id := fs.String("id", "", "question id")
		answer := fs.String("answer", "", "the answer, recorded verbatim")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("question answer", *id != "" && *answer != "", "-id and -answer are required") {
			return exitUsage
		}
		if _, err := e.led.Append(e.actor, ledger.KindQuestionAnswered, *id, ledger.QuestionAnswered{
			ID: *id, Answer: *answer, AnsweredBy: e.actor,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("question %s answered\n", *id)
		return exitOK

	case "list":
		fs := sub("question list")
		open := fs.Bool("open", true, "only unanswered questions")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		qs, err := e.led.Questions("", *open)
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(qs)
		}
		for _, q := range qs {
			flag := ""
			if q.Blocking {
				flag = "BLOCKING "
			}
			fmt.Printf("%-24s %s%s\n", q.ID, flag, q.Text)
			if q.Lean != "" {
				fmt.Printf("%-24s   lean: %s\n", "", q.Lean)
			}
			if q.Answered {
				fmt.Printf("%-24s   answer: %s\n", "", q.Answer)
			}
		}
		return exitOK
	}
	return fail("question: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- approval

func cmdApproval(e *env, args []string) int {
	if len(args) == 0 {
		return fail("approval needs a subcommand: request | list | decide")
	}
	switch args[0] {
	case "request":
		fs := sub("approval request")
		id := fs.String("id", "", "approval id")
		item := fs.String("item", "", "work item id")
		digest := fs.String("plan", "", "digest of the dry run being approved")
		summary := fs.String("summary", "", "what this change does, in one line an operator can act on")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("approval request", *id != "" && *item != "", "-id and -item are required") {
			return exitUsage
		}
		it, err := e.led.Item(*item)
		if err != nil {
			return fail("%v", err)
		}
		d := *digest
		if d == "" {
			d = it.PlanDigest
		}
		if d == "" {
			return fail("no plan digest: an approval with nothing to name approves a sentence, and the apply then performs whatever the plan has become")
		}
		if _, err := e.led.Append(e.actor, ledger.KindApprovalRequested, *id, ledger.ApprovalRequested{
			ID: *id, ItemID: *item, Radius: it.Radius, PlanDigest: d, Summary: *summary,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("approval %s requested for %s (radius %s, plan %s)\n", *id, *item, it.Radius, shortHash(d))
		return exitOK

	case "decide":
		fs := sub("approval decide")
		id := fs.String("id", "", "approval id")
		verdict := fs.String("verdict", "", "approve|reject")
		approver := fs.String("approver", "", "the person deciding — named, not a role")
		note := fs.String("note", "", "why, recorded verbatim")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("approval decide", *id != "" && (*verdict == "approve" || *verdict == "reject"),
			"-id and -verdict approve|reject are required") {
			return exitUsage
		}
		if *verdict == "approve" && strings.TrimSpace(*approver) == "" {
			return fail("-approver is required to approve. An approval nobody's name is on is an approval nobody gave")
		}
		if _, err := e.led.Append(e.actor, ledger.KindApprovalDecided, *id, ledger.ApprovalDecided{
			ID: *id, Verdict: *verdict, Approver: *approver, Note: *note,
		}); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("approval %s: %s by %s\n", *id, *verdict, *approver)
		return exitOK

	case "list":
		aps, err := e.led.Approvals("")
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(aps)
		}
		for _, a := range aps {
			status := "OPEN"
			if a.Decided() {
				status = strings.ToUpper(a.Verdict) + " by " + a.Approver
			}
			fmt.Printf("%-20s %-14s %-7s plan %s  %s\n", a.ID, a.ItemID, a.Radius, shortHash(a.PlanDigest), status)
			if a.Summary != "" {
				fmt.Printf("%-20s   %s\n", "", a.Summary)
			}
		}
		return exitOK
	}
	return fail("approval: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- lease

func cmdLease(e *env, args []string) int {
	if len(args) == 0 {
		return fail("lease needs a subcommand: acquire | release | list")
	}
	switch args[0] {
	case "acquire":
		fs := sub("lease acquire")
		key := fs.String("key", "", "the work item id — never a phrase")
		runID := fs.String("run", "", "run id")
		worker := fs.String("worker", "", "worker type")
		var res listFlag
		fs.Var(&res, "resource", "a resource this run changes (repeatable)")
		subject := fs.String("subject", "", "one line of free text, used only for the advisory overlap hint")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if !need("lease acquire", *key != "" && *runID != "", "-key and -run are required") {
			return exitUsage
		}
		if _, err := e.led.Item(*key); err != nil {
			return fail("no item %q. The lease key is the item's own id, not a phrase: free-text keys for one subject can share no words at all, and then every overlap check stays silent", *key)
		}
		out, err := e.leases.Acquire(lease.Lease{
			Key: *key, RunID: *runID, Worker: *worker, Resources: res, Subject: *subject,
		})
		if err != nil {
			return fail("%v", err)
		}
		if !out.Granted {
			fmt.Fprintf(os.Stderr, "REFUSED [%s] %s\n", out.Reason, out.Detail)
			return exitRefused
		}
		fmt.Printf("lease %s granted to %s\n", *key, *runID)
		if out.Advisory != "" {
			fmt.Println(out.Advisory)
		}
		return exitOK

	case "release":
		fs := sub("lease release")
		key := fs.String("key", "", "the lease key")
		runID := fs.String("run", "", "run id that holds it")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		if err := e.leases.Release(*key, *runID); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("lease %s released\n", *key)
		return exitOK

	case "list":
		live, err := e.leases.Live()
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(live)
		}
		if len(live) == 0 {
			fmt.Println("no live leases")
			return exitOK
		}
		for _, l := range live {
			fmt.Printf("%-16s %-26s %-16s %v\n", l.Key, l.RunID, l.Worker, l.Resources)
		}
		return exitOK
	}
	return fail("lease: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- report

func cmdReport(e *env, args []string) int {
	if len(args) == 0 {
		return fail("report needs a subcommand: fleet | segment <id>")
	}
	switch args[0] {
	case "fleet":
		out, err := report.Fleet(e.led, e.cfg, nowUTC())
		if err != nil {
			return fail("%v", err)
		}
		fmt.Print(out)
		return exitOK
	case "segment":
		if len(args) < 2 {
			return fail("report segment <id>")
		}
		out, err := report.Segment(e.led, e.cfg, args[1])
		if err != nil {
			return fail("%v", err)
		}
		fmt.Print(out)
		return exitOK
	}
	return fail("report: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- ledger

func cmdLedger(e *env, args []string) int {
	if len(args) == 0 {
		return fail("ledger needs a subcommand: verify | events | head")
	}
	switch args[0] {
	case "verify":
		rep, err := e.led.Verify()
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			emit(rep)
		} else {
			printVerify(rep)
		}
		switch rep.Verdict {
		case ledger.VerdictTampered:
			return exitTampered
		case ledger.VerdictUnknown:
			return exitLedgerUnknown
		}
		return exitOK

	case "head":
		seq, hash, err := e.led.Head()
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("%d %s\n", seq, hash)
		return exitOK

	case "events":
		fs := sub("ledger events")
		from := fs.Int64("from", 1, "first sequence number")
		limit := fs.Int("limit", 40, "how many")
		kind := fs.String("kind", "", "restrict to one event kind")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		evs, err := e.led.Events(*from, 0)
		if err != nil {
			return fail("%v", err)
		}
		shown := 0
		for _, ev := range evs {
			if *kind != "" && string(ev.Kind) != *kind {
				continue
			}
			if *limit > 0 && shown >= *limit {
				break
			}
			shown++
			mark := " "
			if !ledger.KnownKinds[ev.Kind] {
				mark = "?"
			}
			fmt.Printf("%s%6d  %-24s %-20s %-10s %s\n", mark, ev.Seq, ev.Kind, ev.Subject, ev.Actor, shortHash(ev.Hash))
		}
		if shown == 0 {
			fmt.Println("no matching events")
		}
		return exitOK
	}
	return fail("ledger: unknown subcommand %q", args[0])
}

func printVerify(rep *ledger.Report) {
	fmt.Printf("LEDGER %s — %d event(s), head seq %d %s\n\n", rep.Verdict, rep.Events, rep.HeadSeq, shortHash(rep.HeadHash))
	for _, tier := range []ledger.Tier{ledger.TierIntegrity, ledger.TierKnowledge} {
		fmt.Printf("%s\n", strings.ToUpper(string(tier)))
		for _, c := range rep.Checks {
			if c.Tier != tier {
				continue
			}
			status := "PASS"
			if !c.Pass {
				status = "FAIL"
			}
			fmt.Printf("  %-4s %-22s %s\n", status, c.Name, c.Detail)
		}
		fmt.Println()
	}
	if len(rep.Findings) > 0 {
		fmt.Println("FINDINGS")
		for _, f := range rep.Findings {
			fmt.Printf("  %s\n", f)
		}
		fmt.Println()
	}
	switch rep.Verdict {
	case ledger.VerdictUnknown:
		fmt.Println("This is not an accusation. Integrity holds; this binary is not current enough to")
		fmt.Println("interpret the whole record. Upgrade it and verify again.")
	case ledger.VerdictTampered:
		fmt.Println("The record does not describe itself. Do not act on any report generated from it.")
	}
}

// ---------------------------------------------------------------- prompt

func cmdPrompt(e *env, args []string) int {
	if e.lib == nil {
		lib, err := prompt.Load(e.cfg.Prompts)
		if err != nil {
			return fail("%v", err)
		}
		e.lib = lib
	}
	if len(args) == 0 {
		return fail("prompt needs a subcommand: list | show | check | assemble")
	}
	switch args[0] {
	case "list":
		for _, id := range e.lib.IDs() {
			p, _ := e.lib.Get(id)
			fmt.Printf("%-24s %-6s %s  %s\n", p.ID, p.Version, p.SHA(), p.Path)
		}
		fmt.Printf("\npreamble %s  %s\n", e.lib.PreambleSHA(), e.lib.PreamblePath())
		return exitOK

	case "show":
		if len(args) < 2 {
			return fail("prompt show <id>")
		}
		p, err := e.lib.Get(args[1])
		if err != nil {
			return fail("%v", err)
		}
		fmt.Print(p.Body)
		return exitOK

	case "assemble":
		if len(args) < 2 {
			return fail("prompt assemble <id>")
		}
		a, err := e.lib.Assemble(args[1], nil)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Print(a.Text)
		return exitOK

	case "check":
		findings := e.lib.CheckClauses(e.cfg.Prompts.MandatoryClauses)
		if len(findings) == 0 {
			fmt.Printf("all %d prompt(s) carry every mandatory clause (%d declared)\n",
				len(e.lib.IDs()), len(e.cfg.Prompts.MandatoryClauses))
			return exitOK
		}
		for _, f := range findings {
			fmt.Fprintf(os.Stderr, "%s (%s) is missing a mandatory clause:\n  %s\n", f.PromptID, f.Path, f.Missing)
		}
		fmt.Fprintln(os.Stderr, "\nPrompts are gated artefacts. A run must not be able to delete a safety clause")
		fmt.Fprintln(os.Stderr, "from its own instructions, so this fails the gate rather than warning.")
		return exitClauseMissing
	}
	return fail("prompt: unknown subcommand %q", args[0])
}

// ---------------------------------------------------------------- helpers

func emit(v any) int {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(string(b))
	return exitOK
}

func workerNames(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Workers))
	for _, w := range cfg.Workers {
		out = append(out, w.Type)
	}
	return out
}

func shortHash(h string) string {
	if h == "" {
		return "(none)"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// stateOf is used by the transition command to read an item's current state.
func stateOf(e *env, itemID string) (authority.State, error) {
	it, err := e.led.Item(itemID)
	if err != nil {
		return "", err
	}
	return authority.State(it.State), nil
}
