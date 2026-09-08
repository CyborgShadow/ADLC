// Package report renders the ledger. Every figure here is a projection of the
// chain; nothing is self-reported and nothing is hand-maintained.
//
// The rule that shapes this package: render from the store, never edit the
// render. Anything hand-edited downstream of the record is a second answer to
// a question the record already answers, and the two diverge the moment either
// changes. Both were honest. Only one was measured.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// Fleet renders the whole-project report.
func Fleet(l *ledger.Ledger, cfg *config.Config, now time.Time) (string, error) {
	var b strings.Builder
	headSeq, headHash, err := l.Head()
	if err != nil {
		return "", err
	}
	segs, err := l.Segments()
	if err != nil {
		return "", err
	}
	items, err := l.Items("")
	if err != nil {
		return "", err
	}
	runs, err := l.Runs("", 0)
	if err != nil {
		return "", err
	}
	refusals, err := l.Proposals("", true, 0)
	if err != nil {
		return "", err
	}
	openQ, err := l.Questions("", true)
	if err != nil {
		return "", err
	}
	sp, err := spend.Summarise(l, cfg.Budget, now)
	if err != nil {
		return "", err
	}

	fmt.Fprintf(&b, "ADLC FLEET REPORT — %s\n", cfg.Project)
	b.WriteString(strings.Repeat("=", 24+len(cfg.Project)) + "\n\n")
	fmt.Fprintf(&b, "ledger head: seq %d, %s\n\n", headSeq, shortHash(headHash))

	done := countState(items, authority.StateDone)
	fmt.Fprintf(&b, "  segments        %d\n", len(segs))
	fmt.Fprintf(&b, "  work items      %d\n", len(items))
	fmt.Fprintf(&b, "  done            %d/%d\n", done, len(items))
	fmt.Fprintf(&b, "  runs            %d\n", len(runs))
	fmt.Fprintf(&b, "  refusals        %d\n", len(refusals))
	fmt.Fprintf(&b, "  open questions  %d\n", len(openQ))
	fmt.Fprintf(&b, "  spend (24h)     %s%s\n", sp.Today, capNote(sp.DayCap))
	fmt.Fprintf(&b, "  spend (total)   %s\n", sp.AllTime)
	if sp.Unpriced > 0 {
		fmt.Fprintf(&b, "  UNPRICED        %d finished run(s) used tokens under a model with no price entry, starting with %s.\n"+
			"                  Their cost is unknown, not zero, and is NOT included above.\n", sp.Unpriced, sp.UnpricedRun)
	}
	b.WriteString("\n")

	b.WriteString(awaitingApproval(l, items))
	b.WriteString(segmentTable(l, segs, items))
	stats, err := l.WorkerStats("")
	if err != nil {
		return "", err
	}
	b.WriteString(workerActivity(stats))
	b.WriteString(neverRun(cfg, stats, ""))
	b.WriteString(openQuestions(openQ))
	b.WriteString(refusalSummary(refusals))
	return b.String(), nil
}

// Segment renders one segment.
func Segment(l *ledger.Ledger, cfg *config.Config, segmentID string) (string, error) {
	seg, err := l.Segment(segmentID)
	if err != nil {
		return "", err
	}
	items, err := l.Items(segmentID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ADLC SEGMENT REPORT — %s %s\n", seg.ID, seg.Title)
	b.WriteString(strings.Repeat("=", 30) + "\n\n")
	fmt.Fprintf(&b, "  work items   %d\n", len(items))
	fmt.Fprintf(&b, "  done         %d/%d\n\n", countState(items, authority.StateDone), len(items))

	b.WriteString("WORK ITEMS\n----------\n")
	for _, it := range items {
		fmt.Fprintf(&b, "  %-14s %-18s %-8s %s\n", it.ID, it.State, it.Radius, truncate(it.Title, 70))
		if it.Attempts >= cfg.Dispatch.MaxAttempts {
			fmt.Fprintf(&b, "  %-14s ATTENTION: %d rework attempts against a limit of %d — escalate rather than re-dispatch\n",
				"", it.Attempts, cfg.Dispatch.MaxAttempts)
		}
		if it.BlockedWhy != "" {
			fmt.Fprintf(&b, "  %-14s blocked: %s\n", "", truncate(it.BlockedWhy, 90))
		}
	}
	b.WriteString("\n")
	stats, err := l.WorkerStats(segmentID)
	if err != nil {
		return "", err
	}
	b.WriteString(workerActivity(stats))
	b.WriteString(neverRun(cfg, stats, segmentID))
	return b.String(), nil
}

func segmentTable(l *ledger.Ledger, segs []ledger.Segment, items []ledger.Item) string {
	var b strings.Builder
	b.WriteString("SEGMENTS\n--------\n")
	for _, s := range segs {
		var in []ledger.Item
		for _, it := range items {
			if it.SegmentID == s.ID {
				in = append(in, it)
			}
		}
		done := countState(in, authority.StateDone)
		var active, blocked, waiting int
		for _, it := range in {
			switch {
			case authority.State(it.State) == authority.StateBlocked:
				blocked++
			case authority.State(it.State) == authority.StateAwaitingApproval:
				waiting++
			case authority.State(it.State).Active():
				active++
			}
		}
		fmt.Fprintf(&b, "  %-6s %-30s %2d/%-3d done  %2d active  %2d blocked  %2d awaiting approval\n",
			s.ID, truncate(s.Title, 30), done, len(in), active, blocked, waiting)
	}
	b.WriteString("\n")
	return b.String()
}

func awaitingApproval(l *ledger.Ledger, items []ledger.Item) string {
	var waiting []ledger.Item
	for _, it := range items {
		if authority.State(it.State) == authority.StateAwaitingApproval {
			waiting = append(waiting, it)
		}
	}
	if len(waiting) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("AWAITING YOUR APPROVAL\n----------------------\n")
	b.WriteString("  These changes are reviewed, green, and STOPPED. Nothing applies them until you say so.\n\n")
	for _, it := range waiting {
		fmt.Fprintf(&b, "  %-14s radius %-7s plan %s\n", it.ID, it.Radius, shortHash(it.PlanDigest))
		fmt.Fprintf(&b, "  %-14s %s\n", "", truncate(it.Title, 92))
		aps, err := l.Approvals(it.ID)
		if err == nil {
			for _, ap := range aps {
				if !ap.Decided() {
					fmt.Fprintf(&b, "  %-14s open request %s: %s\n", "", ap.ID, truncate(ap.Summary, 80))
				}
			}
		}
		fmt.Fprintf(&b, "  %-14s approve with: adlc approve %s --approver <you> --note \"...\"\n\n", "", it.ID)
	}
	return b.String()
}

func workerActivity(stats []ledger.WorkerStat) string {
	var b strings.Builder
	b.WriteString("WORKER ACTIVITY (measured from the ledger, not self-reported)\n")
	b.WriteString(strings.Repeat("-", 60) + "\n")
	fmt.Fprintf(&b, "  %-18s %-14s %5s %5s %5s %5s %5s %5s %10s\n",
		"worker", "layer", "runs", "pass", "fail", "rej", "blk", "ref", "cost")
	sort.Slice(stats, func(i, j int) bool { return stats[i].Type < stats[j].Type })
	for _, s := range stats {
		fmt.Fprintf(&b, "  %-18s %-14s %5d %5d %5d %5d %5d %5d %10s\n",
			s.Type, s.Layer, s.Runs, s.Pass, s.Fail, s.Reject, s.Blocked, s.Refusals,
			spend.Micros(s.CostMicros))
		if s.Unfinished > 0 {
			fmt.Fprintf(&b, "  %-18s %d run(s) started and never recorded an end — not a pass, not a failure, an unknown\n",
				"", s.Unfinished)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// neverRun is the roll call.
//
// It reads from the REGISTRY and subtracts activity, rather than reading from
// activity and listing what it finds. That inversion is the entire mechanism:
// a worker that has never run files nothing, so listing what filed renders its
// absence as silence, and silence reads as fine.
func neverRun(cfg *config.Config, stats []ledger.WorkerStat, scope string) string {
	byType := map[string]ledger.WorkerStat{}
	for _, s := range stats {
		byType[s.Type] = s
	}
	var dark, quiet []config.WorkerDecl
	for _, w := range cfg.Workers {
		if byType[w.Type].Runs > 0 {
			continue
		}
		if w.LowCadence {
			quiet = append(quiet, w)
		} else {
			dark = append(dark, w)
		}
	}
	var b strings.Builder
	b.WriteString("NEVER RUN\n---------\n")
	where := "the whole ledger"
	if scope != "" {
		where = "segment " + scope
	}
	if len(dark) == 0 && len(quiet) == 0 {
		fmt.Fprintf(&b, "  Every declared worker has at least one run across %s.\n\n", where)
		return b.String()
	}
	if len(dark) > 0 {
		fmt.Fprintf(&b, "  %d of %d declared workers have produced NOTHING in %s.\n", len(dark), len(cfg.Workers), where)
		b.WriteString("  This is absence, not health. Do not read a missing verdict as a pass.\n\n")
		for _, w := range dark {
			fmt.Fprintf(&b, "    %-18s %-14s 0 runs   caps: %s\n", w.Type, w.Layer, strings.Join(w.Capabilities, ","))
			if w.Description != "" {
				fmt.Fprintf(&b, "    %-18s %s\n", "", truncate(w.Description, 88))
			}
		}
		b.WriteString("\n")
	}
	if len(quiet) > 0 {
		fmt.Fprintf(&b, "  %d declared low-cadence worker(s) have no runs in %s. Expected, and still stated.\n",
			len(quiet), where)
		for _, w := range quiet {
			fmt.Fprintf(&b, "    %-18s %s\n", w.Type, truncate(w.Description, 80))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func openQuestions(qs []ledger.Question) string {
	if len(qs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("OPEN QUESTIONS\n--------------\n")
	for _, q := range qs {
		flag := "        "
		if q.Blocking {
			flag = "BLOCKING"
		}
		fmt.Fprintf(&b, "  %-22s %s %s\n", q.ID, flag, truncate(q.Text, 88))
		if q.Lean != "" {
			fmt.Fprintf(&b, "  %-22s lean: %s\n", "", truncate(q.Lean, 92))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func refusalSummary(refusals []ledger.Proposal) string {
	if len(refusals) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, r := range refusals {
		counts[r.Reason]++
	}
	type kv struct {
		k string
		n int
	}
	var rows []kv
	for k, n := range counts {
		rows = append(rows, kv{k, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].k < rows[j].k
	})
	var b strings.Builder
	b.WriteString("REFUSED TRANSITIONS, BY REASON\n------------------------------\n")
	b.WriteString("  A refusal is a recorded outcome, not an error that went away.\n\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-32s %d\n", r.k, r.n)
	}
	b.WriteString("\n  most recent:\n")
	for i, r := range refusals {
		if i >= 5 {
			break
		}
		fmt.Fprintf(&b, "    %-14s %-16s %s -> %s  [%s]\n", r.ItemID, r.Worker, r.From, r.To, r.Reason)
		fmt.Fprintf(&b, "    %-14s %s\n", "", truncate(r.Detail, 100))
	}
	b.WriteString("\n")
	return b.String()
}

func countState(items []ledger.Item, s authority.State) int {
	n := 0
	for _, it := range items {
		if authority.State(it.State) == s {
			n++
		}
	}
	return n
}

func capNote(cap spend.Micros) string {
	if cap == 0 {
		return "  (no daily cap configured — unlimited, which is not the same as zero)"
	}
	return fmt.Sprintf("  of %s", cap)
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
