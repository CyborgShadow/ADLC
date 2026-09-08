package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/story"
)

// cmdRunStory implements the read-and-reproduce half of `adlc run`.
func cmdRunStory(e *env, sub string, args []string) int {
	switch sub {
	case "show":
		if len(args) < 1 {
			return fail("run show <run-id>")
		}
		st, err := story.OfRun(e.led, e.cfg, args[0])
		if err != nil {
			return fail("%v", err)
		}
		if e.jsonOut {
			return emit(st)
		}
		printStory(st)
		return exitOK

	case "steps":
		if len(args) < 1 {
			return fail("run steps <run-id>")
		}
		st, err := story.OfRun(e.led, e.cfg, args[0])
		if err != nil {
			return fail("%v", err)
		}
		for i, s := range st.Steps {
			fmt.Printf("%2d. [%s] %-40s %s\n", i+1, s.At.UTC().Format("15:04:05"), s.Title, s.Detail)
		}
		return exitOK

	case "prompt":
		if len(args) < 1 {
			return fail("run prompt <run-id> — prints the exact text this run was given")
		}
		r, err := e.led.Run(args[0])
		if err != nil {
			return fail("%v", err)
		}
		b, err := e.led.Blob(r.PromptSHA)
		if err != nil {
			return fail("%v", err)
		}
		os.Stdout.Write(b)
		return exitOK

	case "envelope":
		if len(args) < 1 {
			return fail("run envelope <run-id> — prints exactly what this run reported")
		}
		r, err := e.led.Run(args[0])
		if err != nil {
			return fail("%v", err)
		}
		b, err := e.led.Blob(r.EnvelopeSHA)
		if err != nil {
			return fail("%v", err)
		}
		os.Stdout.Write(b)
		return exitOK

	case "replay":
		if len(args) < 1 {
			return fail("run replay <run-id>")
		}
		rp, err := story.Rederive(context.Background(), e.led, e.cfg, e.repo, args[0], nowUTC())
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("REPLAY %s\n\n", rp.RunID)
		fmt.Printf("  recorded at the time   %s\n", rp.Recorded)
		fmt.Printf("  re-derived now         %s\n", rp.Rederived)
		fmt.Printf("  gate against this tree %s\n", orDash(rp.GateNow))
		if rp.Detail != "" {
			fmt.Printf("  detail                 %s\n", rp.Detail)
		}
		for _, n := range rp.Notes {
			fmt.Printf("  note                   %s\n", n)
		}
		fmt.Println()
		if rp.Agrees {
			fmt.Println("The decision reproduces. Given the same recorded inputs, the control plane")
			fmt.Println("makes the same call today that it made then.")
			return exitOK
		}
		fmt.Println("The decision does NOT reproduce. That is not automatically a defect — rules are")
		fmt.Println("allowed to get stricter, and a run admitted under an older policy may be refused")
		fmt.Println("under a newer one. What matters is that the difference is visible and explicable.")
		return exitRefused

	case "retry":
		if len(args) < 1 {
			return fail("run retry <run-id> — re-open the item this run worked on")
		}
		return retryRun(e, args[0])
	}
	return fail("run: unknown subcommand %q", sub)
}

// retryRun puts an item back where a fresh run can pick it up.
//
// It is deliberately not "run that agent again". A retry that re-invokes the
// same agent against the same state reproduces the same conditions and usually
// the same outcome; what actually helps is putting the item back in a state
// the dispatcher will pick up, with the reason recorded so the next run can
// read what went wrong last time.
func retryRun(e *env, runID string) int {
	r, err := e.led.Run(runID)
	if err != nil {
		return fail("%v", err)
	}
	if r.ItemID == "" {
		return fail("run %s was not against a work item, so there is nothing to retry", runID)
	}
	it, err := e.led.Item(r.ItemID)
	if err != nil {
		return fail("%v", err)
	}
	from := authority.State(it.State)
	if from == authority.StateRejected && it.Attempts >= e.cfg.Dispatch.MaxAttempts {
		return fail("%s has already been reworked %d times against a limit of %d. Past the limit an item escalates to a person rather than looping — decide what to change, then raise the limit or supersede the item",
			it.ID, it.Attempts, e.cfg.Dispatch.MaxAttempts)
	}
	if from != authority.StateRejected {
		return fail("%s is in %s, not rejected. A retry re-opens an item review sent back; to re-run a lane, use `adlc dispatch once`", it.ID, from)
	}
	reason := fmt.Sprintf("retried by %s after run %s", e.actor, runID)
	facts, err := authority.Gather(e.led, it.ID, "", nil, "", nowUTC())
	if err != nil {
		return fail("%v", err)
	}
	dec := authority.New(e.cfg).Decide(authority.Request{
		Actor: e.actor, AsPM: true, From: from, To: authority.StateInProgress,
		Reason: reason, Now: nowUTC(),
	}, facts)
	if !dec.Admitted {
		fmt.Fprintf(os.Stderr, "REFUSED [%s] %s\n", dec.Reason, dec.Detail)
		return exitRefused
	}
	if _, err := e.led.Append(e.actor, ledger.KindTransitionAdmitted, it.ID, ledger.TransitionOutcome{
		RunID: runID, ItemID: it.ID, Worker: "", From: string(from), To: string(authority.StateInProgress),
	}); err != nil {
		return fail("%v", err)
	}
	if _, err := e.led.Append(e.actor, ledger.KindItemTransitioned, it.ID, ledger.ItemTransitioned{
		ItemID: it.ID, From: string(from), To: string(authority.StateInProgress),
		RunID: runID, Reason: reason, BumpAttempt: true,
	}); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%s re-opened for rework (attempt %d of %d). The next implement lane will pick it up.\n",
		it.ID, it.Attempts+1, e.cfg.Dispatch.MaxAttempts)
	return exitOK
}

func printStory(st *story.Run) {
	r := st.Run
	fmt.Printf("RUN %s — %s\n", r.RunID, r.WorkerType)
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("\n%s\n\n", st.Headline)

	fmt.Println("WHY THIS MATTERED")
	for _, p := range st.Purpose {
		fmt.Printf("  %s\n", wrapAt(p, 76, "  "))
	}
	fmt.Println()

	fmt.Println("WHAT HAPPENED, IN ORDER")
	for i, s := range st.Steps {
		fmt.Printf("  %d. %s  [%s]\n", i+1, s.Title, s.At.UTC().Format("15:04:05"))
		if s.Detail != "" {
			fmt.Printf("     %s\n", wrapAt(s.Detail, 72, "     "))
		}
	}
	fmt.Println()

	fmt.Println("FACTS")
	fmt.Printf("  duration      %s\n", st.Duration)
	fmt.Printf("  cost          %s\n", st.Cost)
	fmt.Printf("  tokens        %d in, %d out, %d cache read, %d cache write\n",
		r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens)
	fmt.Printf("  prompt        %s %s\n", shortHash(r.PromptSHA), retained(st.PromptRetained))
	fmt.Printf("  envelope      %s %s\n", shortHash(r.EnvelopeSHA), retained(st.EnvRetained))
	fmt.Printf("  base commit   %s\n", shortHash(r.BaseSHA))
	fmt.Printf("  head commit   %s\n", shortHash(r.HeadSHA))
	fmt.Println()

	if st.Reproducible {
		fmt.Println("REPRODUCING THIS")
		fmt.Printf("  adlc run replay %s      re-derive the decision from the record\n", r.RunID)
		fmt.Printf("  adlc run prompt %s      the exact text this run was given\n", r.RunID)
		fmt.Printf("  adlc run envelope %s    exactly what it reported back\n", r.RunID)
	} else {
		fmt.Println("NOT REPRODUCIBLE")
		fmt.Printf("  %s\n", wrapAt(st.WhyNot, 74, "  "))
	}
}

func retained(ok bool) string {
	if ok {
		return "(retained)"
	}
	return "(NOT retained — this part of the run cannot be reproduced)"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func wrapAt(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			b.WriteString(line + "\n" + indent)
			line = w
			continue
		}
		line += " " + w
	}
	b.WriteString(line)
	return b.String()
}

// cmdSegmentAdvance lets an operator move a deliverable by hand.
func cmdSegmentAdvance(e *env, args []string) int {
	fs := sub("segment advance")
	id := fs.String("id", "", "segment id")
	to := fs.String("to", "", "state to move it to")
	why := fs.String("why", "", "why — recorded and shown on the roadmap")
	if fs.Parse(args) != nil {
		return exitUsage
	}
	if !need("segment advance", *id != "" && *to != "" && *why != "",
		"-id, -to and -why are required; a roadmap move with no stated reason is indistinguishable from a mistake") {
		return exitUsage
	}
	if !authority.SegmentState(*to).Known() {
		return fail("%q is not a roadmap state; known: %v %s", *to, authority.SegmentStages(), authority.SegPaused)
	}
	seg, err := e.led.Segment(*id)
	if err != nil {
		return fail("%v", err)
	}
	if _, err := e.led.Append(e.actor, ledger.KindSegmentAdvanced, seg.ID, ledger.SegmentAdvanced{
		SegmentID: seg.ID, From: seg.State, To: *to, Why: *why,
	}); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%s  %s -> %s\n", seg.ID, seg.State, *to)
	return exitOK
}

var _ = config.CapGenerate
