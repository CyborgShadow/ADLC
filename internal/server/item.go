package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// The item page.
//
// It used to render one flat "Decisions" table containing every proposal the
// authority ever answered, newest first, with the refusals in red. On a
// finished item that reads as a contradiction — the badge says done and the
// table is full of failures — and an operator's reasonable conclusion is that
// something is broken.
//
// Nothing was broken. A refusal is the system working: a run proposed something
// it had not earned, the authority said no and said why, the work was corrected
// and came back. Recording refusals is deliberate, because a record that keeps
// only what it accepted cannot answer the question it will actually be asked.
//
// So the page now tells that story instead of dumping the table. What happened
// reads forwards, in order. What was refused is separate, framed as the guards
// doing their job, with each reason explained in words.

// stepRow is one accepted move, in the order it happened.
type stepRow struct {
	N     int
	Seq   int64
	From  string
	To    string
	Says  string
	Role  string
	RunID string
	When  string
}

// refusalRow is one proposal the authority turned down.
type refusalRow struct {
	N       int
	Seq     int64
	Edge    string
	Role    string
	RunID   string
	Reason  string
	Means   string
	Detail  string
	When    string
	Guarded string
	// Next is what happens after this refusal — the part a reader actually
	// needs, because "it was refused" without "and then what" leaves them
	// unable to tell a self-healing hiccup from something waiting on them.
	Next string
	// NeedsPerson is true when the fleet cannot clear this on its own.
	NeedsPerson bool
	// Cleared is true when the item moved past this point afterwards.
	Cleared bool
}

type itemView struct {
	Item      ledger.Item
	Segment   ledger.Segment
	Stage     authority.Stage
	Says      string
	Class     string
	Criteria  []string
	Steps     []stepRow
	Refusals  []refusalRow
	Runs      []ledger.Run
	Questions []ledger.Question
	Approvals []ledger.Approval

	Tries, Passed, Failed int
	// Outstanding counts refusals that are still standing and need a person.
	Outstanding int
	// Headline is the one sentence that reconciles the badge with the table.
	Headline string
	// NoRuns explains an item with decisions but no run rows, which is what a
	// hand-driven or seeded item looks like.
	NoRuns bool
}

func (s *Server) item(r *http.Request) (string, any, error) {
	id := strings.TrimPrefix(r.URL.Path, "/item/")
	it, err := s.Led.Item(id)
	if err != nil {
		// An id can be referenced all over the record and still never have
		// become an item: a planner proposed it and the admission rules refused
		// it. Every one of those references is a link, and a link that 404s
		// tells the reader nothing about why. So the refusal answers the click.
		if props, perr := s.Led.Proposals(id, false, 50); perr == nil && len(props) > 0 {
			return s.refusedItem(id, props)
		}
		return "", nil, err
	}
	seg, _ := s.Led.Segment(it.SegmentID)
	runs, _ := s.Led.Runs(id, 50)
	props, _ := s.Led.Proposals(id, false, 200)
	qs, _ := s.Led.Questions(id, false)
	aps, _ := s.Led.Approvals(id)
	tries, passed, failed, _ := s.Led.AttemptsFor(id)

	st := authority.State(it.State)
	v := itemView{
		Item: it, Segment: seg, Stage: authority.StageOf(st),
		Says: humanState(st), Class: itemClass(st), Criteria: it.Criteria,
		Runs: runs, Questions: qs, Approvals: aps,
		Tries: tries, Passed: passed, Failed: failed,
		NoRuns: len(runs) == 0 && len(props) > 0,
	}

	// Oldest first. A history that reads backwards is one nobody can follow.
	sort.SliceStable(props, func(i, j int) bool { return props[i].Seq < props[j].Seq })
	n := 0
	for _, p := range props {
		when := time.UnixMilli(p.TsMS).Format("Jan 2 15:04:05")
		if p.Admitted {
			n++
			v.Steps = append(v.Steps, stepRow{
				N: n, Seq: p.Seq, From: p.From, To: p.To, Role: p.Worker, RunID: p.RunID, When: when,
				Says: movedBecause(authority.State(p.From), authority.State(p.To)),
			})
			continue
		}
		edge := p.From + " → " + p.To
		if p.To == "" {
			edge = "out of " + p.From
		}
		reason := authority.Reason(p.Reason)
		next, needsPerson := refusalNext(reason)
		v.Refusals = append(v.Refusals, refusalRow{
			N: len(v.Refusals) + 1, Seq: p.Seq, Edge: edge, Role: p.Worker,
			RunID: p.RunID, When: when, Reason: p.Reason, Means: refusalMeans(reason),
			Detail: p.Detail, Guarded: refusalGuards(reason),
			Next: next, NeedsPerson: needsPerson,
		})
	}
	// A refusal is only outstanding if nothing was accepted after it. Computed
	// rather than asserted: the alternative is a page that says a refusal was
	// handled without knowing whether it was.
	var lastAccepted int64 = -1
	if len(v.Steps) > 0 {
		lastAccepted = v.Steps[len(v.Steps)-1].Seq
	}
	for i := range v.Refusals {
		v.Refusals[i].Cleared = v.Refusals[i].Seq < lastAccepted
		if v.Refusals[i].Cleared {
			v.Refusals[i].NeedsPerson = false
			continue
		}
		if v.Refusals[i].NeedsPerson {
			v.Outstanding++
		}
	}
	v.Headline = itemHeadline(st, len(v.Steps), len(v.Refusals), v.Outstanding)
	return "Item " + id, v, nil
}

// refusedItem renders an id that was proposed and never admitted.
func (s *Server) refusedItem(id string, props []ledger.Proposal) (string, any, error) {
	sort.SliceStable(props, func(i, j int) bool { return props[i].Seq < props[j].Seq })
	v := itemView{
		Item:  ledger.Item{ID: id, State: "never created"},
		Class: "mute",
		Says:  "this was proposed and refused, so no such item exists",
		Headline: "Nothing under this id was ever created. A run proposed it and the admission " +
			"rules turned it down, and the proposal is kept — with the reason — because a backlog " +
			"that silently drops what it refused cannot tell you what it decided not to build.",
	}
	for _, p := range props {
		if p.Admitted {
			continue
		}
		reason := authority.Reason(p.Reason)
		next, needsPerson := refusalNext(reason)
		v.Refusals = append(v.Refusals, refusalRow{
			N: len(v.Refusals) + 1, Seq: p.Seq, Edge: "proposed as a new item",
			Role: p.Worker, RunID: p.RunID,
			When:   time.UnixMilli(p.TsMS).Format("Jan 2 15:04:05"),
			Reason: p.Reason, Means: refusalMeans(reason), Detail: p.Detail,
			Guarded: refusalGuards(reason), Next: next, NeedsPerson: needsPerson,
		})
	}
	return "Item " + id, v, nil
}

// refusalNext says what happens after a refusal, and whether the fleet can get

// there on its own.
//
// Most refusals are self-clearing: the run is re-dispatched, or the item goes
// back a stage, and the next attempt does the thing properly. A few cannot be —
// a spent rework budget, an unanswered question, a missing approval, a
// misconfiguration — and those sit until somebody acts. Rendering the two
// identically is what makes a page of red rows unreadable, because the reader
// cannot tell which ones are theirs.
func refusalNext(r authority.Reason) (next string, needsPerson bool) {
	switch r {
	case authority.ReasonGateFailed, authority.ReasonGateUnknown, authority.ReasonGateNotRun:
		return "the item stays where it is and is re-dispatched; the next run has to make the checks pass", false
	case authority.ReasonClaimDiscrepancy, authority.ReasonClaimOmission:
		return "the run is discarded and the item re-dispatched — nothing it claimed was taken on trust", false
	case authority.ReasonSelfCertified:
		return "a different run picks the work up, because the one that wrote it may not judge it", false
	case authority.ReasonNoSuchEdge, authority.ReasonWrongProposer, authority.ReasonStaleFromState:
		return "no state changed; the lane dispatches the stage the item is actually in", false
	case authority.ReasonUncommittedTree, authority.ReasonCommitUnreachable:
		return "re-dispatched; the next run has to commit its work before claiming anything about it", false
	case authority.ReasonMissingEvidence:
		return "re-dispatched, and the next run has to cite what it ran", false
	case authority.ReasonRunNotStarted:
		return "nothing changed; a proposal with no registered run behind it is discarded", false
	case authority.ReasonMergeConflict, authority.ReasonTrunkMoved:
		return "it goes back into the merge queue and is tried again against the current trunk", false
	case authority.ReasonMergeGateFailed, authority.ReasonStaleBaseClobber:
		return "the work goes back to a builder with the conflicting paths named", false
	case authority.ReasonDependenciesUnmet:
		return "it opens on its own once what it depends on is done", false

	// --- the ones that wait for somebody
	case authority.ReasonOpenQuestion:
		return "nothing moves until the blocking question is answered on the Questions page", true
	case authority.ReasonAttemptLimit:
		return "the rework budget is spent; a person decides whether to raise it, rescope the item, or drop it", true
	case authority.ReasonApprovalMissing, authority.ReasonRadiusMismatch:
		return "it waits on the Approvals page for a named person to clear this exact plan", true
	case authority.ReasonApprovalStale:
		return "the approval no longer covers this plan, so a fresh one is needed on the Approvals page", true
	case authority.ReasonBudgetExhausted:
		return "nothing dispatches until the spend window resets or the cap is raised in the config", true
	case authority.ReasonZeroChecks, authority.ReasonUnknownWorker, authority.ReasonRadiusUndeclared:
		return "a configuration problem rather than a work problem — it will refuse identically until the config is fixed", true
	case authority.ReasonPlanDigestMissing:
		return "an operator run has to produce a dry-run plan before anybody can approve it", true
	case authority.ReasonNoReason:
		return "nothing changed; whoever proposed it has to say why", true
	}
	return "the item is re-dispatched", false
}

// itemHeadline reconciles the badge with the table, in one sentence.
func itemHeadline(st authority.State, steps, refusals, outstanding int) string {
	switch {
	case refusals == 0:
		return "This item went straight through: " + plural(steps, "step", "steps") + ", nothing refused."
	case outstanding > 0:
		return plural(outstanding, "refusal is", "refusals are") + " still standing and " +
			"cannot be cleared by the fleet on its own — they are marked below and they are yours."
	case st == authority.StateDone:
		return "Finished. It took " + plural(steps, "accepted step", "accepted steps") + ", and " +
			plural(refusals, "proposal was", "proposals were") + " refused on the way — every one of " +
			"them since cleared. A refusal here is a run asking for something it had not earned yet, " +
			"so these are the guards working rather than the work failing."
	case st == authority.StateBlocked || st == authority.StateRejected:
		return "Not moving. " + plural(refusals, "proposal was", "proposals were") +
			" refused; the last one below says what would clear it."
	default:
		return "In flight: " + plural(steps, "accepted step", "accepted steps") + " so far, and " +
			plural(refusals, "refused proposal", "refused proposals") + ", all since cleared."
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// movedBecause says what an accepted edge meant, in words.
func movedBecause(from, to authority.State) string {
	switch {
	case from == authority.StateQueued && to == authority.StateReady:
		return "everything it depended on was done, so it opened for work"
	case to == authority.StateInProgress && from == authority.StateReady:
		return "a builder picked it up"
	case to == authority.StateInProgress:
		return "it went back to a builder"
	case to == authority.StateBlocked:
		return "a run stopped on something it could not decide"
	case to == authority.StateRejected:
		return "review found a blocker"
	case to == authority.StateDone:
		return "everything it taught was recorded, and it closed"
	}
	return humanState(to)
}

// refusalMeans translates a refusal reason into what actually happened.
//
// The reason codes are a closed vocabulary because agents read them too, and a
// refusal an agent cannot classify is one it works around rather than fixes.
// That makes them precise and unhelpful to a person reading the page, so each
// one gets a sentence.
func refusalMeans(r authority.Reason) string {
	switch r {
	case authority.ReasonNoSuchEdge:
		return "that move does not exist in the lifecycle — the run tried to skip a stage"
	case authority.ReasonWrongProposer:
		return "the role that asked is not allowed to make that move"
	case authority.ReasonStaleFromState:
		return "the run was working from an out-of-date view; the item had already moved"
	case authority.ReasonGateFailed:
		return "the control plane ran the project's own checks and one of them came back red"
	case authority.ReasonGateUnknown:
		return "a check could not be run at all, and a check that did not run is not a check that passed"
	case authority.ReasonGateNotRun:
		return "no checks were run for that move"
	case authority.ReasonZeroChecks:
		return "no checks are declared for that move, so the gate would have been green over nothing"
	case authority.ReasonClaimDiscrepancy:
		return "what the agent said it observed and what the control plane observed disagreed"
	case authority.ReasonClaimOmission:
		return "the agent left something out of its account that the gate saw"
	case authority.ReasonSelfCertified:
		return "the run judging the work was the run that did the work"
	case authority.ReasonUncommittedTree:
		return "the evidence described a tree with uncommitted changes, which nobody can check out again"
	case authority.ReasonCommitUnreachable:
		return "the commit the work named no longer exists"
	case authority.ReasonMissingEvidence:
		return "the claim came with nothing behind it"
	case authority.ReasonOpenQuestion:
		return "a blocking question on this item is still unanswered"
	case authority.ReasonDependenciesUnmet:
		return "something this item depends on is not done"
	case authority.ReasonRadiusUndeclared:
		return "the blast radius was not one this build knows, so it failed closed"
	case authority.ReasonRadiusMismatch:
		return "the change reaches further than policy applies without a person"
	case authority.ReasonApprovalMissing:
		return "nobody has approved this yet"
	case authority.ReasonApprovalStale:
		return "the approval no longer covers this plan — either it expired or the plan changed after it was given"
	case authority.ReasonArtifactMissing:
		return "behavioural evidence did not say what it ran against"
	case authority.ReasonAttemptLimit:
		return "this item has used up its rework budget and needs a person"
	case authority.ReasonNoReason:
		return "a decision was proposed with no stated reason"
	case authority.ReasonRunNotStarted:
		return "the proposal came from a run nothing ever registered"
	case authority.ReasonUnknownWorker:
		return "the role that asked is not declared in the config"
	case authority.ReasonBudgetExhausted:
		return "the spend cap for this period was already reached"
	case authority.ReasonMergeConflict:
		return "the branch does not rebase onto the trunk cleanly"
	case authority.ReasonStaleBaseClobber:
		return "the rebase would have reverted work that landed after this branch started"
	case authority.ReasonMergeGateFailed:
		return "the checks passed before the rebase and failed after it"
	case authority.ReasonTrunkMoved:
		return "the trunk moved while the checks were running, so what was checked is not what would land"
	case authority.ReasonPlanDigestMissing:
		return "there was no dry-run plan for a person to approve"
	}
	return ""
}

// refusalGuards names what would have happened without the refusal. It is the
// part that makes a red row readable as a good outcome.
func refusalGuards(r authority.Reason) string {
	switch r {
	case authority.ReasonSelfCertified:
		return "work marking itself correct"
	case authority.ReasonClaimDiscrepancy, authority.ReasonClaimOmission:
		return "an inaccurate account being taken as evidence"
	case authority.ReasonGateFailed, authority.ReasonGateUnknown, authority.ReasonGateNotRun, authority.ReasonZeroChecks:
		return "work advancing on checks that did not pass, or did not run"
	case authority.ReasonNoSuchEdge, authority.ReasonWrongProposer:
		return "a stage being skipped"
	case authority.ReasonUncommittedTree, authority.ReasonCommitUnreachable:
		return "evidence about a tree nobody can check out again"
	case authority.ReasonRadiusMismatch, authority.ReasonApprovalMissing, authority.ReasonApprovalStale:
		return "something irreversible happening without a person"
	case authority.ReasonStaleBaseClobber:
		return "a colleague's landed work being silently reverted"
	case authority.ReasonOpenQuestion:
		return "an item finishing over a question nobody answered"
	}
	return ""
}

var _ = http.StatusOK
