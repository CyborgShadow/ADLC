package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// The roadmap.
//
// The first version showed a title, a state and a progress bar, which answered
// "how far along is this" and nothing else. That is the wrong question to
// answer first. Somebody opening a roadmap wants to know what a deliverable IS,
// what work it turned out to contain, and which of that work is finished — and
// a title alone ("Control plane") tells them none of it.
//
// So each deliverable now shows its intent in the words somebody wrote, and its
// items split three ways: still to do, being worked on, finished. Three groups
// rather than a percentage, because "eleven items, four done" is a fact and
// "36%" is a summary of a fact that hides which four.

// itemLine is one work item as the roadmap lists it.
type itemLine struct {
	ledger.Item
	// Stage is the phase a person reads, rather than the machine state.
	Stage string
	// State is the machine state, kept because it is what every other page and
	// every command names.
	Says  string
	Class string
	Note  string
	// Waiting are the dependencies this item cannot start without, each with
	// where it stands. Empty for anything not queued.
	Waiting []depLine
}

// bucket is one of the three groups a person actually sorts work into.
type bucket struct {
	Key   string
	Label string
	Why   string
	Items []itemLine
}

type roadmapRow struct {
	ledger.Segment
	Progress authority.SegmentProgress
	Stage    string
	Class    string
	Next     string

	// Buckets are to-do / in progress / done, in that order.
	Buckets []bucket
	// Trouble is the items that are stuck rather than progressing. They are
	// separated because a blocked item inside "in progress" reads as work
	// happening, and it is the opposite.
	Trouble []itemLine

	NeedsYou    bool
	SignTo      string
	SignVerb    string
	DeclineVerb string
	AskLabel    string
	AskWhy      string
	WhyHint     string
	Parked      bool

	// Updated is when this deliverable last moved at all.
	Updated string
}

func (s *Server) roadmap(*http.Request) (string, any, error) {
	segs, err := s.Led.Segments()
	if err != nil {
		return "", nil, err
	}
	all, err := s.Led.Items("")
	if err != nil {
		return "", nil, err
	}
	bySeg := map[string][]ledger.Item{}
	for _, it := range all {
		bySeg[it.SegmentID] = append(bySeg[it.SegmentID], it)
	}

	// Every item, so a dependency in another deliverable is still named rather
	// than reported as "something".
	byItem := map[string]ledger.Item{}
	byID := map[string]authority.State{}
	for _, it := range all {
		byID[it.ID] = authority.State(it.State)
		byItem[it.ID] = it
	}
	var rows []roadmapRow
	for _, sg := range segs {
		items := bySeg[sg.ID]
		r := roadmapRow{Segment: sg, Progress: authority.Progress(items), Stage: sg.State}
		r.explain()
		r.fill(items, byItem, byID)
		rows = append(rows, r)
	}
	return "Roadmap", struct {
		Rows []roadmapRow
		Any  bool
	}{rows, len(rows) > 0}, nil
}

// fill sorts a deliverable's items into the three groups and pulls out the
// stuck ones.
func (r *roadmapRow) fill(items []ledger.Item, byItem map[string]ledger.Item, state map[string]authority.State) {
	todo := bucket{Key: "todo", Label: "Still to do",
		Why: "written down, nobody has started"}
	doing := bucket{Key: "doing", Label: "Being worked on",
		Why: "somewhere between a builder and the merge queue"}
	done := bucket{Key: "done", Label: "Finished",
		Why: "merged, and what it taught was written down"}

	sorted := append([]ledger.Item(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	for _, it := range sorted {
		st := authority.State(it.State)
		line := itemLine{
			Item:  it,
			Stage: authority.StageOf(st).Label,
			Says:  humanState(st),
			Class: itemClass(st),
		}
		if st == authority.StateQueued {
			// It knows which items and what state each is in. Saying "something"
			// sends somebody hunting through the list for what the page has.
			line.Says, line.Waiting = blockedOn(it, byItem, state)
		}
		switch {
		case st == authority.StateBlocked:
			line.Note = "waiting on an answer"
			r.Trouble = append(r.Trouble, line)
		case st == authority.StateRejected:
			line.Note = "review sent it back"
			r.Trouble = append(r.Trouble, line)
		case st == authority.StateAwaitingApproval:
			line.Note = "waiting for a person to approve it"
			r.Trouble = append(r.Trouble, line)
		case st == authority.StateDone:
			done.Items = append(done.Items, line)
		case st == authority.StateCancelled || st == authority.StateSuperseded:
			line.Note = "closed without being built"
			done.Items = append(done.Items, line)
		case st == authority.StateQueued || st == authority.StateReady:
			todo.Items = append(todo.Items, line)
		default:
			doing.Items = append(doing.Items, line)
		}
	}
	r.Buckets = []bucket{todo, doing, done}
}

// humanState is what a machine state means, in a phrase a person can act on.
//
// The state name is exact and every command uses it, so it stays on the row.
// This is the gloss beside it — the two are not redundant, because "arbitrating"
// is precise and tells a newcomer nothing.
func humanState(st authority.State) string {
	switch st {
	case authority.StateQueued:
		return "waiting on something it depends on"
	case authority.StateReady:
		return "ready for a builder to pick up"
	case authority.StateInProgress:
		return "being built"
	case authority.StateReadyForTesting:
		return "built, waiting for the tests to be run"
	case authority.StateTesting:
		return "tests running"
	case authority.StateReadyForReview:
		return "tests passed, waiting to be judged"
	case authority.StateJudging:
		return "being checked against its acceptance criteria"
	case authority.StateReadyForValidation:
		return "judged, waiting for adversarial review"
	case authority.StateValidating:
		return "under adversarial review"
	case authority.StateReviewed:
		return "review passed, waiting for the hygiene pass"
	case authority.StateJanitoring:
		return "hygiene pass"
	case authority.StateReadyForArbitration:
		return "waiting to be judged against the system as a whole"
	case authority.StateArbitrating:
		return "being judged against the system as a whole"
	case authority.StateAwaitingApproval:
		return "stopped for a person to approve it"
	case authority.StateApplying:
		return "being applied to something real"
	case authority.StateConfirming:
		return "checking what the apply produced"
	case authority.StateReadyToMerge:
		return "cleared to land, waiting for the merge queue"
	case authority.StateMerging:
		return "in the merge queue"
	case authority.StateMerged:
		return "landed, waiting for the lessons pass"
	case authority.StateImproving:
		return "recording what it taught"
	case authority.StateDone:
		return "done"
	case authority.StateBlocked:
		return "stopped on a question nobody has answered"
	case authority.StateRejected:
		return "sent back by review"
	case authority.StateCancelled:
		return "withdrawn"
	case authority.StateSuperseded:
		return "replaced by other work"
	}
	return string(st)
}

func itemClass(st authority.State) string {
	switch {
	case st == authority.StateDone:
		return "ok"
	case st == authority.StateBlocked, st == authority.StateRejected:
		return "bad"
	case st == authority.StateAwaitingApproval:
		return "warn"
	case st == authority.StateQueued, st == authority.StateCancelled, st == authority.StateSuperseded:
		return "mute"
	}
	return "live"
}

// explain fills in what this deliverable's state means and what it is waiting
// for, including the two gates a person owns.
func (r *roadmapRow) explain() {
	switch authority.SegmentState(r.State) {
	case authority.SegTheory:
		r.Class, r.Next = "mute", "An idea. Nothing is committed to it yet."
		r.NeedsYou, r.SignTo = true, string(authority.SegRoadmap)
		r.SignVerb, r.DeclineVerb = "Put on the roadmap", "Not now"
		r.AskLabel = "Is this worth carrying?"
		r.AskWhy = "Nothing is spent either way — putting it on the roadmap only says it is worth deciding about."
		r.WhyHint = "why this is worth carrying, or why it is not"
	case authority.SegRoadmap:
		r.Class, r.Next = "warn", "On the roadmap, waiting for you to sign off the intent. Nothing moves until you do."
		r.NeedsYou, r.SignTo = true, string(authority.SegSignedOff)
		r.SignVerb, r.DeclineVerb = "Sign it off", "Send it back"
		r.AskLabel = "Should the fleet start spending on this?"
		r.AskWhy = "Signing off puts a researcher on it, then a planner, then a plan review — all of which cost runs. This is the gate that decides whether that is worth it."
		r.WhyHint = "what makes this worth doing now — or what would have to be true first"
	case authority.SegSignedOff:
		r.Class, r.Next = "live", "Signed off. A researcher will turn the intent into an approach."
	case authority.SegResearching:
		r.Class, r.Next = "live", "A researcher is working out the approach."
	case authority.SegResearched:
		r.Class, r.Next = "live", "The approach is written. A planner will decompose it into work."
	case authority.SegPlanning:
		r.Class, r.Next = "live", "A planner is decomposing the approach."
	case authority.SegPlanned:
		r.Class, r.Next = "warn", "The plan is written and nobody has checked it against the intent yet. No work starts until they do."
	case authority.SegValidating:
		r.Class, r.Next = "live", "A validator is checking the plan against the intent."
	case authority.SegReady:
		r.Class, r.Next = "live", "The plan was accepted. Work can start."
	case authority.SegBuilding:
		r.Class, r.Next = "live", "Work is in flight."
	case authority.SegDelivered:
		r.Class, r.Next = "ok", "Delivered."
	case authority.SegPaused:
		r.Class, r.Next = "mute", "Parked. Somebody decided not to carry this for now, and said why."
		r.Parked = true
	}
}

// Undescribed reports a deliverable nobody wrote an intent for.
//
// Said plainly rather than papered over. An id and a title tell a reader
// nothing, and generating a description from the title would be worse — it
// would read as though somebody had decided what this was for.
func (r roadmapRow) Undescribed() bool {
	return strings.TrimSpace(r.Brief) == "" && strings.TrimSpace(r.Rationale) == ""
}

// blockedOn names what an item is waiting for, all the way down.
//
// "waiting on something it depends on" was all a row said, on a page whose
// whole job is to explain where work is. Naming the immediate dependency was
// better and still not enough: S1-012 waits on S1-009, which waits on S1-007,
// which waits on S1-006, which waits on S1-002 — four levels, and only the
// bottom one is actually holding anything up. Showing the first link tells you
// where to click next, four times.
//
// So the whole chain is returned, depth-tagged, and the summary names the item
// at the bottom: that is the one whose completion releases the rest.
func blockedOn(it ledger.Item, items map[string]ledger.Item, state map[string]authority.State) (string, []depLine) {
	if len(it.DependsOn) == 0 {
		return "queued with nothing to wait for — this should have become ready, and its not doing so is a defect worth reporting", nil
	}
	seen := map[string]bool{it.ID: true}
	chain := walkDeps(it, items, state, seen, 0)
	if len(chain) == 0 {
		return "every dependency is done — this should have become ready, and its not doing so is a defect worth reporting", nil
	}
	// The deepest thing that is not waiting on anything else is what actually
	// has to happen first.
	root := chain[0]
	for _, d := range chain {
		if d.Root {
			root = d
			break
		}
	}
	levels := 1
	for _, d := range chain {
		if d.Depth+1 > levels {
			levels = d.Depth + 1
		}
	}
	says := fmt.Sprintf("waiting on %s", root.ID)
	if levels > 1 {
		says = fmt.Sprintf("waiting on %s, %d levels down", root.ID, levels)
	}
	return says, chain
}

// depWalkMax stops a chain that has become pathological rather than rendering
// it. A tree this deep is a planning defect, and saying so beats printing it.
const depWalkMax = 12

// walkDeps returns the unfinished dependencies below an item, depth first.
func walkDeps(it ledger.Item, items map[string]ledger.Item, state map[string]authority.State, seen map[string]bool, depth int) []depLine {
	if depth >= depWalkMax {
		return []depLine{{ID: "…", State: "too deep", Class: "bad", Depth: depth,
			Says: "the dependency chain is deeper than anything a person can hold, which is a planning defect rather than a queue"}}
	}
	var out []depLine
	for _, dep := range it.DependsOn {
		st, known := state[dep]
		switch {
		case !known:
			out = append(out, depLine{ID: dep, State: "not on the record", Class: "bad",
				Depth: depth, Root: true,
				Says: "this dependency does not exist, so the item can never start"})
			continue
		case st == authority.StateDone:
			continue
		case seen[dep]:
			// A cycle can never resolve itself. Naming it beats recursing into
			// it, and beats the item simply never moving with no reason given.
			out = append(out, depLine{ID: dep, State: string(st), Class: "bad",
				Depth: depth, Root: true,
				Says: "already above this in the chain — these items depend on each other and neither can ever start"})
			continue
		}
		seen[dep] = true
		line := depLine{ID: dep, State: string(st), Class: itemClass(st),
			Says: humanState(st), Title: items[dep].Title, Depth: depth}
		below := walkDeps(items[dep], items, state, seen, depth+1)
		line.Root = len(below) == 0
		out = append(out, line)
		out = append(out, below...)
	}
	return out
}

// depLine is one item in a dependency chain, as a row renders it.
type depLine struct {
	ID    string
	State string
	Class string
	Says  string
	// Title is what the item is, so the tree reads without a second lookup.
	Title string
	// Depth indents it under whatever depends on it.
	Depth int
	// Root marks something waiting on nothing else: the work that actually has
	// to happen before any of the rest can.
	Root bool
}

// Indent is the width the template uses, so the arithmetic is not in the HTML.
func (d depLine) Indent() int { return d.Depth * 18 }
