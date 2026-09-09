// Package dispatch selects the work, mints the run's identity, isolates it,
// invokes an agent, and puts the result to the authority.
//
// Three scheduling rules here are load-bearing rather than incidental.
//
// Work is picked from the FINISHED end. Verification is the next step of an
// item already in flight, not a competing job — which is why the
// verification layer here cannot go dark the way a separately scheduled one
// did.
//
// The routing map decides WHO, not just what. A specialist roster is
// decorative if the picker takes whichever worker with the right capability
// sorts first, so the item's area resolves the worker before anything else.
//
// A run wears ONE name. The run id, the workspace directory and the lease key
// are the same string, collision-checked before the work is picked. A run
// whose two names diverged was invisible to the guard that keyed on the
// workspace and to the guard that keyed on the id, and both reported clean.
package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/gate"
	"github.com/CyborgShadow/ADLC/internal/lease"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// Invocation is everything an agent runner is told.
type Invocation struct {
	RunID        string
	WorkerType   string
	ItemID       string
	SegmentID    string
	PromptPath   string
	PromptText   string
	WorkDir      string
	EnvelopePath string
	Timeout      time.Duration
	// LedgerPath is the absolute path of the real ledger, so an agent running in
	// an isolated worktree reads the project's record rather than creating an
	// empty one beside itself.
	LedgerPath string
	// OnOutput, when set, receives the agent's output as it arrives rather than
	// once at the end. It exists for the console, where a person is waiting on
	// the other side of a turn; a lane leaves it nil and nothing changes.
	//
	// It is a view, never a result. A runner that ignores it entirely is still a
	// correct runner — the envelope is read from a file, as it always was.
	OnOutput func(text string)
}

// Result is what a runner produced.
type Result struct {
	Envelope []byte
	ExitCode int
	Stderr   string
	// Usage is what the agent tool itself reported spending, read off its own
	// output rather than taken from the envelope. Measured reports whether
	// anything was measured at all: absence must not render as zero, because
	// "nobody counted" and "it cost nothing" are opposite facts. A run that
	// died before writing an envelope still consumed tokens, and recording
	// ledger.Usage{} for those runs put eleven minutes of real agent work on
	// the bill at zero.
	Usage    ledger.Usage
	Measured bool
	// CostUSD is the figure the harness itself reported, kept beside the
	// re-priced one rather than instead of it: one is what somebody was
	// charged, the other is what this build believes the tokens are worth, and
	// a surface that shows only the second cannot say which it is showing.
	//
	// CostKnown rather than a zero cost meaning free, for the same reason
	// Measured exists — "the harness did not report a cost" and "this run was
	// free" are opposite facts, and a runner that reports neither leaves both
	// false.
	CostUSD   float64
	CostKnown bool
}

// Runner invokes one agent. It is an interface so that the control plane has
// no opinion about which agent, which vendor, or which transport — and so
// that the whole dispatcher is testable without one.
type Runner interface {
	Invoke(ctx context.Context, in Invocation) (Result, error)
}

// Dispatcher drives the loop.
type Dispatcher struct {
	Cfg    *config.Config
	Led    *ledger.Ledger
	Lib    *prompt.Library
	Leases *lease.Store
	// refreshMu serialises Refresh: its moves are read-check-write against the
	// ledger, and two concurrent passes recorded the same transition twice.
	refreshMu sync.Mutex
	// Watch, when set, is told about a run's output as it happens. It is a view
	// for whoever is waiting, never an input: see watch.go.
	Watch  Watcher
	Runner Runner
	Repo   string
	Actor  string
	Now    func() time.Time
	Log    func(string)

	// The fleet-wide ceiling on agents running at once, built once from the
	// config on first use.
	slots     *slots
	slotsOnce sync.Once

	// Ids handed out but not yet on the ledger. See mintRunID.
	mintMu sync.Mutex
	minted map[string]bool

	// noEnv counts consecutive dispatches that produced no envelope at all.
	// See breaker.go: a fleet whose agents have stopped starting keeps firing
	// on cadence at a hundred per cent failure otherwise.
	noEnv atomic.Int64

	// id names this Dispatcher instance within this process, minted once. Two
	// instances hold two slots channels and therefore two ceilings, and a
	// record that cannot tell them apart cannot show which one let a run in.
	idOnce sync.Once
	id     string
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) log(f string, a ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(f, a...))
	}
}

// Kind separates the two things a run can be dispatched to do.
type Kind string

const (
	// KindItem advances one work item along the lifecycle.
	KindItem Kind = "item"
	// KindSegment advances a deliverable through planning: research, plan, or
	// validate the plan against the intent it came from.
	KindSegment Kind = "planning"
)

// Candidate is one dispatchable piece of work.
type Candidate struct {
	Kind       Kind
	Item       ledger.Item
	Segment    ledger.Segment
	From       authority.State
	Capability string
	Worker     string
	Need       int
	// Repairing marks a planning run that is fixing a REJECTED breakdown
	// rather than adding to an accepted one. The two want opposite things: one
	// amends what exists, the other files what is missing.
	Repairing bool
	Priority  int
	Why       string
}

// Key is the claim a candidate takes before it runs.
//
// One item, one worker — except in a stage whose tasks are meant to run
// together. Testing, judging and adversarial review examine the same commit and
// do not conflict, so keying all three on the item id alone would serialise
// exactly the work that was collapsed into one stage to be parallel. The
// capability is appended by the control plane from values it declares, never
// from anything an agent composed.
func (c Candidate) Key() string {
	if c.Kind == KindSegment {
		return "plan-" + c.Segment.ID
	}
	if c.From == authority.StateVerifying {
		return c.Item.ID + "#" + c.Capability
	}
	return c.Item.ID
}

// Filter scopes a dispatch pass to one lane.
type Filter struct {
	Capability string
	Areas      []string
	Worker     string
	Limit      int
}

func (f Filter) allowsArea(area string) bool {
	if len(f.Areas) == 0 {
		return true
	}
	for _, a := range f.Areas {
		if a == area {
			return true
		}
	}
	return false
}

// Candidates lists dispatchable work, most nearly finished first, generation
// last.
//
// Generation sorts behind every piece of live work on purpose. A fleet that
// generates while items are waiting grows a backlog faster than it drains one,
// and a long backlog of unstarted work is indistinguishable from progress
// until somebody counts.
func (d *Dispatcher) Candidates(f Filter) ([]Candidate, error) {
	items, err := d.Led.Items("")
	if err != nil {
		return nil, err
	}
	state := map[string]authority.State{}
	openBySegment := map[string]int{}
	for _, it := range items {
		state[it.ID] = authority.State(it.State)
		if !authority.State(it.State).Terminal() {
			openBySegment[it.SegmentID]++
		}
	}

	segs, err := d.Led.Segments()
	if err != nil {
		return nil, err
	}
	segByID := map[string]ledger.Segment{}
	for _, s := range segs {
		segByID[s.ID] = s
	}

	// advised keeps the "the gate is advising here" note to once per deliverable
	// per pass, so a real signal does not become a wall of repeated lines.
	advised := map[string]bool{}
	var out []Candidate
	for _, it := range items {
		st := authority.State(it.State)
		capability, prio := authority.CapabilityFor(st)
		if capability == "" {
			continue
		}
		// The lane filter is applied per TASK, below, and not here.
		//
		// A stage with several outstanding tasks has no single capability, and
		// CapabilityFor answers with the first of them. Filtering on that
		// answer threw the whole item away before its other tasks were ever
		// considered: a judge lane asked for judge work, was compared against
		// test, and skipped every item in verification. The judge lane ticked
		// 269 times and dispatched nothing while three items sat waiting for a
		// judge, and every liveness figure stayed green throughout.
		if f.Capability != "" && f.Capability != capability &&
			st != authority.StateVerifying {
			continue
		}
		if !f.allowsArea(it.Area) {
			continue
		}
		// The rework budget bounds BUILDING, not reviewing.
		//
		// Applied to the whole item it removed an at-limit item from every
		// lane at once — test, judge, validate, curate, arbitrate and improve
		// as well as implement — so work that had already been done could not
		// even be looked at, and the item disappeared from the board without a
		// word. Two items sat in in_progress at 3 of 3 that way, invisible to
		// every lane and escalated to nobody, while the transition table has
		// declared rejected -> blocked ("the attempt limit is reached; a person
		// decides") the whole time and nothing proposed it. escalateAtLimit
		// does now.
		//
		// Verifying work already produced spends no further attempt, so it is
		// not bounded here. And it is said out loud, like UNREACHABLE and HELD
		// beside it: an item that stops silently is indistinguishable from one
		// nobody has got to yet, which is the failure this tool exists to make
		// impossible.
		if capability == config.CapImplement && it.Attempts >= d.Cfg.Dispatch.MaxAttempts {
			d.log("AT LIMIT %s is in %s having used %d of %d rework attempts, so no further building is dispatched. Its finished work can still be reviewed, and escalateAtLimit parks it for a person.",
				it.ID, st, it.Attempts, d.Cfg.Dispatch.MaxAttempts)
			continue
		}
		qs, err := d.Led.Questions(it.ID, true)
		if err != nil {
			return nil, err
		}
		blocked := false
		for _, q := range qs {
			if q.Blocking {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		deps := true
		for _, dep := range it.DependsOn {
			if state[dep] != authority.StateDone {
				deps = false
			}
		}
		if !deps {
			continue
		}

		if seg, ok := segByID[it.SegmentID]; ok {
			if st := authority.SegmentState(seg.State); st.Known() && !st.OpenForWork() {
				// The handoff gate. A breakdown nobody has reviewed does not become
				// work — for work that reaches something real. Below the declared
				// threshold the review still runs and still records everything it
				// finds; it simply does not hold the item, because a contested plan
				// for a page that reaches a file is not worth stopping twelve items
				// for. Said out loud the first time, so an advised gate is never a
				// silent one.
				if authority.PlanGateHolds(it.Radius, d.Cfg.Blast) {
					d.log("HELD %s — %s is %s; the breakdown has not been reviewed against its brief yet",
						it.ID, seg.ID, seg.State)
					continue
				}
				if !advised[it.SegmentID] {
					advised[it.SegmentID] = true
					d.log("ADVISED %s is %s and its breakdown is still under review, but this work is blast radius %q — below plan_gate_min %q, so the review advises rather than holds. Its objections are still recorded and still reach the next planner.",
						seg.ID, seg.State, orNone(it.Radius), d.Cfg.Blast.PlanGateMin)
				}
			}
		}
		// A stage with several tasks offers one candidate per task still
		// outstanding, so they run together rather than one after another.
		// Every other state has exactly one, and the loop below is the same
		// either way.
		wanted := []string{capability}
		if st == authority.StateVerifying {
			passed, failed, verr := d.Led.VerificationReportsFor(it.ID, it.Attempts)
			if verr != nil {
				return nil, verr
			}
			// A task that has already reported is not offered again this round,
			// pass or fail. Re-dispatching a failure while its siblings are still
			// running is a loop, not a retry: the tree it examines has not moved.
			wanted = authority.VerificationOutstanding(authority.VerificationReported(passed, failed))
		}
		for _, capability := range wanted {
			if f.Capability != "" && f.Capability != capability {
				continue
			}
			worker, ok := d.Cfg.OwnerFor(it.Area, capability)
			if !ok {
				// An item that needs a capability nobody declares is unreachable, and an
				// unreachable item looks exactly like one nobody has got round to. Say so
				// rather than skip it silently.
				d.log("UNREACHABLE %s is in %s and needs %q work in area %q, which no declared worker can take",
					it.ID, st, capability, it.Area)
				continue
			}
			if f.Worker != "" && f.Worker != worker {
				continue
			}
			out = append(out, Candidate{
				Kind: KindItem, Item: it, From: st, Capability: capability,
				Worker: worker, Priority: prio,
				Why: fmt.Sprintf("%s is in %s and needs %s work", it.ID, st, capability),
			})
		}
	}

	for _, s := range segs {
		st := authority.SegmentState(s.State)
		capability, prio := authority.SegmentCapabilityFor(st)
		if capability == "" {
			continue
		}
		if f.Capability != "" && f.Capability != capability {
			continue
		}
		// The backlog check is about TOPPING UP a plan somebody has accepted:
		// a planner that runs on a timer regardless of backlog depth invents
		// work to justify its own cadence.
		//
		// Before a plan is accepted it is exactly the wrong question. A
		// validator that rejects a breakdown sends the deliverable back to
		// researched with the rejected items still there, still counting as
		// open work — so this gate refused to let a planner near the very plan
		// that had just been rejected, and the deliverable stopped for good.
		need := 0
		repairing := false
		if capability == config.CapPlan {
			need = authority.SegmentNeedsWork(s.TargetOpen, openBySegment[s.ID])
			if need == 0 && authority.SegmentPlanAccepted(st) {
				continue
			}
			// Repairing a rejected breakdown, not adding to an accepted one.
			// The shortfall is genuinely zero and the planner is told so: asked
			// for a number instead, it files that many NEW items to satisfy the
			// rejection rather than fixing the ones that were rejected. That is
			// exactly what happened — four items became eight, then twelve, and
			// the validator's second review said so in as many words.
			repairing = need == 0
			// A repair that keeps being rejected is a disagreement two agents
			// will not settle by trying again. Stop, ask a person, and dispatch
			// nothing further on it — an unanswered blocking question parks the
			// deliverable visibly, which is the point of raising one.
			if repairing {
				if n, looping := d.planIsLooping(s.ID); looping {
					d.stopTheLoop(s, n)
					continue
				}
			}
		}
		worker, ok := d.Cfg.OwnerFor("", capability)
		if !ok {
			d.log("UNREACHABLE %s is %s and needs %q work, which no declared worker can take", s.ID, st, capability)
			continue
		}
		if f.Worker != "" && f.Worker != worker {
			continue
		}
		out = append(out, Candidate{
			Kind: KindSegment, Segment: s, Capability: capability,
			Worker: worker, Need: need, Repairing: repairing, Priority: prio,
			Why: fmt.Sprintf("%s is %s and needs %s", s.ID, st, capability),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].Key() < out[j].Key()
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// TickResult reports what one pass did.
type TickResult struct {
	Dispatched bool
	RunID      string
	ItemID     string
	Worker     string
	Admitted   bool
	Reason     string
	Detail     string
	Idle       string
	Created    int
}

// Tick performs at most one dispatch across every lane.
func (d *Dispatcher) Tick(ctx context.Context) (TickResult, error) {
	return d.TickScoped(ctx, Filter{})
}

// TickScoped performs at most one dispatch within one lane.
func (d *Dispatcher) TickScoped(ctx context.Context, f Filter) (TickResult, error) {
	now := d.now()

	// Only the DAY cap can be answered here, because a lane has no segment: the
	// per-segment arm is asked in dispatchOne, where the candidate names one.
	// Passing a literal 0 and "" for it, as this call used to, made
	// PerSegmentMicros unfirable at every configuration — a declared cap that
	// cannot refuse is worse than no cap, because it still gets believed.
	today, err := d.Led.SpendMicros(now.Add(-24*time.Hour), "")
	if err != nil {
		return TickResult{}, err
	}
	if v := spend.CheckDispatch(d.Cfg.Budget, spend.Micros(today), 0, ""); !v.OK {
		return TickResult{Idle: "budget: " + v.Detail}, nil
	}

	if _, err := d.Refresh(); err != nil {
		return TickResult{}, err
	}
	cands, err := d.Candidates(f)
	if err != nil {
		return TickResult{}, err
	}
	if len(cands) == 0 {
		return TickResult{Idle: "nothing dispatchable in this lane: every item is done, blocked, awaiting approval, or past its rework limit"}, nil
	}
	for _, c := range cands {
		res, taken, err := d.dispatchOne(ctx, c, now)
		if err != nil {
			return TickResult{}, err
		}
		if taken {
			return res, nil
		}
	}
	return TickResult{Idle: "every candidate in this lane is leased by a live sibling run"}, nil
}

// segmentOf names the deliverable a candidate's spend belongs to. A planning
// run is spend on the deliverable itself; an item's run is spend on the
// deliverable it was decomposed from.
func segmentOf(c Candidate) string {
	if c.Kind == KindSegment {
		return c.Segment.ID
	}
	return c.Item.SegmentID
}

func (d *Dispatcher) dispatchOne(ctx context.Context, c Candidate, now time.Time) (TickResult, bool, error) {
	// The per-segment cap, asked where a segment exists to ask about.
	//
	// Refusing this candidate rather than the whole pass: a deliverable that has
	// spent its budget must not stop the ones that have not, and the day cap
	// above already covers the fleet-wide case. Checked before the run id is
	// minted, so an exhausted segment costs nothing to skip.
	if seg := segmentOf(c); seg != "" && d.Cfg.Budget.PerSegmentMicros > 0 {
		spent, err := d.Led.SpendMicros(time.Time{}, seg)
		if err != nil {
			return TickResult{}, false, err
		}
		if v := spend.CheckDispatch(d.Cfg.Budget, 0, spend.Micros(spent), seg); !v.OK {
			d.log("OVER SEGMENT BUDGET %s — %s. Nothing further is dispatched for this deliverable; the rest of the fleet is unaffected.", seg, v.Detail)
			return TickResult{Idle: "budget: " + v.Detail}, false, nil
		}
	}

	runID, err := d.mintRunID(c.Worker, now)
	if err != nil {
		return TickResult{}, false, err
	}
	// Released once the ledger knows the id, or immediately if this dispatch
	// never gets that far.
	started := false
	defer func() {
		if !started {
			d.releaseRunID(runID)
		}
	}()

	// Claim before working. A claim taken at the end records a collision instead
	// of preventing one.
	out, err := d.Leases.Acquire(lease.Lease{
		Key: c.Key(), RunID: runID, Worker: c.Worker,
		Resources: c.Item.Resources,
		Subject:   strings.TrimSpace(c.Item.Title + " " + c.Segment.Title),
	})
	if err != nil {
		return TickResult{}, false, err
	}
	if !out.Granted {
		d.log("SKIP %s — %s", c.Key(), out.Detail)
		return TickResult{}, false, nil
	}
	if out.Advisory != "" {
		d.log("%s", out.Advisory)
	}
	defer d.Leases.Release(c.Key(), runID)

	if err := d.claimForWork(runID, &c, now); err != nil {
		d.log("CANNOT START %s — %v", c.Item.ID, err)
		return TickResult{}, false, nil
	}

	// A run on an item continues that item's work rather than starting again
	// from the trunk. For a verification task this is the difference between
	// reviewing the change and reviewing main; for a builder retrying after a
	// rejection it is the difference between fixing what it wrote and writing
	// it again.
	base := ""
	if c.Kind == KindItem {
		base = d.workspaceBase(c.Item.ID)
	}
	ws, err := d.prepareWorkspace(runID, base)
	if err != nil {
		return TickResult{}, false, fmt.Errorf("isolate %s: %w", runID, err)
	}
	defer ws.Cleanup()

	worker := d.Cfg.Worker(c.Worker)
	asm, err := d.Lib.Assemble(worker.Prompt, d.promptVars(c, runID, ws))
	if err != nil {
		return TickResult{}, false, err
	}

	// Retain the exact text this run is given. Without the bytes, "we know what
	// this run was told" is a claim about a hash nobody can check.
	if _, berr := d.Led.PutBlob(ledger.BlobPrompt, asm.Bytes()); berr != nil {
		return TickResult{}, false, berr
	}
	baseSHA, _ := gate.HeadSHA(ws.Dir)
	if _, err := d.Led.Append(d.Actor, ledger.KindPromptPinned, worker.Prompt, ledger.PromptPinned{
		PromptID: asm.PromptID, Version: asm.Version, SHA: asm.SHA,
		Path: filepath.Join(d.Cfg.Prompts.Dir, worker.Prompt+".md"),
	}); err != nil {
		return TickResult{}, false, err
	}
	segID := c.Item.SegmentID
	if c.Kind == KindSegment {
		segID = c.Segment.ID
	}
	// The run's actor names the PROCESS and the Dispatcher instance, not just
	// the command. Every run in a 660-run ledger carried the bare string "cli",
	// so when observed concurrency reached 17 against a declared ceiling of 8
	// the record could not separate the two explanations — two control planes,
	// or two Dispatchers in one — and a per-process cap is not a cap.
	if _, err := d.Led.Append(d.processActor(), ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: c.Worker, ItemID: c.Item.ID, SegmentID: segID,
		PromptID: asm.PromptID, PromptSHA: asm.SHA, BaseSHA: baseSHA, WorkDir: ws.Dir, Branch: ws.Branch,
		Model: d.Cfg.Budget.DefaultModel,
	}); err != nil {
		return TickResult{}, false, err
	}
	started = true
	d.releaseRunID(runID)
	d.log("DISPATCH %s  %s  %s", runID, c.Worker, c.Why)

	// A deliverable that an agent is working on says so.
	//
	// The roadmap declares researching, planning and validating, names them in
	// its reading order, and drains them with the same capability as the state
	// before — and nothing put a deliverable into one. So it jumped signed_off
	// straight to researched, and for the whole time a researcher was actually
	// working the roadmap showed a deliverable sitting still. Every "where is
	// the work" surface then reported zero while the fleet was busy.
	//
	// Safe to leave behind if this run dies: the -ing states are drained by the
	// same lane as the states before them, so a deliverable stuck in one is
	// picked up again rather than stranded.
	if c.Kind == KindSegment {
		if to, moved := authority.SegmentPickedUp(authority.SegmentState(c.Segment.State)); moved {
			if _, err := d.Led.Append(d.Actor, ledger.KindSegmentAdvanced, c.Segment.ID, ledger.SegmentAdvanced{
				SegmentID: c.Segment.ID, From: c.Segment.State, To: string(to), RunID: runID,
				Why: c.Worker + " is working on it",
			}); err != nil {
				return TickResult{}, false, err
			}
			d.log("ROADMAP %s  %s -> %s  (%s is working on it)", c.Segment.ID, c.Segment.State, to, c.Worker)
		}
	}

	res := TickResult{Dispatched: true, RunID: runID, ItemID: c.Item.ID, Worker: c.Worker}
	timeout := time.Duration(d.Cfg.Dispatch.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Hour
	}
	promptPath := filepath.Join(ws.Dir, ".adlc-prompt.md")
	if abs, aerr := filepath.Abs(promptPath); aerr == nil {
		promptPath = abs
	}
	if err := os.WriteFile(promptPath, []byte(asm.Text), 0o644); err != nil {
		return res, true, err
	}
	// The heartbeat starts before the agent does and stops however this run
	// ends. While it is warm, some process is waiting on this run; once it goes
	// cold, nobody is, and the reaper closes the run rather than leaving the
	// item claimed by something that no longer exists.
	hb := d.startBeat(beatFile{
		RunID: runID, ItemID: c.Item.ID, LeaseKey: c.Key(), Worker: c.Worker,
		Workspace: ws.Dir, Envelope: ws.EnvelopePath,
	})
	defer hb.done()

	// Whoever is waiting on this run can watch it happen. Opened next to the
	// heartbeat because they answer the same question from two sides: the beat
	// says somebody is still waiting, this says what they are waiting on.
	w := d.watch()
	w.Open(runID, c.Worker, c.Item.ID)
	defer w.Close(runID)

	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A slot is held only for as long as an agent is actually running. Taking
	// it earlier would count selection and bookkeeping against a ceiling that
	// exists to bound money and machine load, neither of which they spend.
	//
	// Wall clock, not the ledger clock: this is how long a process waited, not
	// a fact about the record, and a test that pins the ledger clock would
	// otherwise report every queue wait as zero.
	queued := time.Now()
	if err := d.limit().enter(rctx); err != nil {
		// The run row already exists, so leaving here without an end would put a
		// run on every surface as "still working" that nobody is waiting on. It
		// is UNKNOWN rather than a failure: nothing was ever asked of an agent.
		d.finish(runID, "unknown", "", "", "", ledger.Usage{}, 0)
		d.log("QUEUE ABANDONED %s waited %s for a concurrency slot and the context ended first; it is recorded as unknown, never as a failure of the work",
			runID, time.Since(queued).Round(time.Millisecond))
		return TickResult{}, false, err
	}
	d.noteSlot(runID, time.Since(queued))
	agentOut, agentErr := d.Runner.Invoke(rctx, Invocation{
		RunID: runID, WorkerType: c.Worker, ItemID: c.Item.ID, SegmentID: segID,
		PromptPath: promptPath, PromptText: asm.Text,
		WorkDir: ws.Dir, EnvelopePath: ws.EnvelopePath, Timeout: timeout,
		LedgerPath: d.Led.ReadOnlyDSN(),
		OnOutput:   func(line string) { w.Line(runID, line) },
	})
	d.limit().leave()

	raw := agentOut.Envelope
	if len(raw) == 0 {
		if b, rerr := os.ReadFile(ws.EnvelopePath); rerr == nil {
			raw = b
		}
	}
	if agentErr != nil && len(raw) == 0 {
		// UNKNOWN, not fail. "fail" is the word a verifier uses for work that is
		// genuinely broken, and a dispatch that never reached an agent is not a
		// verdict about the work at all — 295 of one night's 297 failures were
		// this, and the report read them as 295 broken changes.
		//
		// The usage and cost carried here are whatever the runner could measure.
		// A run that consumed tokens and then died is not a free run, and
		// hardcoding an empty Usage put real agent minutes on the bill at zero.
		d.finish(runID, "unknown", "", "", "", agentOut.Usage,
			d.priced(runID, "", agentOut.Usage))
		d.noteReportedCost(runID, agentOut)
		res.Reason = string(authority.ReasonNoEnvelope)
		res.Detail = d.recordNoEnvelope(runID, c, agentErr, agentOut)
		d.log("NO ENVELOPE %s — %s", runID, res.Detail)
		// Counted only for a run that produced nothing at all. See breaker.go:
		// the lanes fired for ninety minutes at a hundred per cent failure
		// because a dispatch that never started cost three seconds and left no
		// trace the scheduler could read.
		if n := d.sawNoEnvelope(); n >= maxNoEnvelopeStreak {
			d.tripBreaker(n)
		}
		return res, true, nil
	}
	// Whatever the verdict says, the invocation works. Only the absence of an
	// envelope is evidence that it does not.
	d.sawEnvelope()

	env, err := envelope.Parse(raw)
	if err != nil {
		// A malformed envelope is a refusable proposal, not a dead run — so it
		// keeps the verdict "fail".
		//
		// The bytes are retained here because the PutBlob further down is
		// never reached on this path: a run refused for malformed output kept
		// no copy of the output it was refused for, so `adlc run envelope`
		// answered "no digest given" and the one artefact that explains the
		// refusal was readable nowhere. Retaining decides nothing — the
		// verdict is still fail, the refusal is still recorded, and every
		// reader of this blob parses it defensively, so a stored malformed
		// envelope is never read back as a proposal.
		envSHA, berr := d.Led.PutBlob(ledger.BlobEnvelope, raw)
		if berr != nil {
			// Failing to keep the copy must not also lose the refusal, which
			// is the part somebody is owed.
			d.log("UNRETAINED %s — the malformed envelope could not be stored: %v", runID, berr)
		}
		// What it must not keep is a cost of zero: an envelope this parser
		// refused still describes a run that spent tokens, so the harness's
		// measurement is preferred and salvageUsage reads the counts back out
		// of the refused bytes rather than inventing an empty account.
		usage := agentOut.Usage
		if usage == (ledger.Usage{}) {
			usage = salvageUsage(raw)
		}
		d.finish(runID, "fail", envSHA, "", "", usage, d.priced(runID, "", usage))
		d.noteReportedCost(runID, agentOut)
		d.recordRefusal(runID, c, authority.ReasonMalformedEnvelope, err.Error())
		d.learnFromMalformed(runID, c, err.Error())
		res.Reason, res.Detail = string(authority.ReasonMalformedEnvelope), err.Error()
		d.log("MALFORMED %s — %s", runID, err.Error())
		return res, true, nil
	}

	for _, n := range env.Normalised() {
		d.log("RESHAPED %s — %s. The envelope on the chain is still exactly what the agent wrote", runID, n)
	}
	// What the tool reported spending outranks what the agent declared. An
	// envelope's usage block is a claim like every other field in it, and it is
	// the one no agent fills in — 157 finished runs in one night reported no
	// tokens at all, so every spend figure was a floor and the per-run cap
	// could not fire. The gate runs the checks itself for the same reason.
	usage := ledger.Usage(env.Usage)
	if agentOut.Measured {
		usage = agentOut.Usage
	}
	model := env.Model
	if model == "" {
		model = d.Cfg.Budget.DefaultModel
	}
	recorded, forCap, unpriced := runCost(d.Cfg.Budget, model, usage)
	if unpriced {
		d.log("UNPRICED %s used model %q, which has no price entry; its cost is unknown, not zero", runID, model)
	}
	if _, berr := d.Led.PutBlob(ledger.BlobEnvelope, env.Raw()); berr != nil {
		return res, true, berr
	}
	d.finish(runID, env.Verdict, env.SHA(), env.HeadSHA, env.Artifact, usage, int64(recorded))
	d.noteReportedCost(runID, agentOut)
	if v := spend.CheckRun(d.Cfg.Budget, forCap); !v.OK {
		// The reason is in the line because CheckRun has two of them and they
		// send a reader to different places: a run over the cap has a bill
		// somebody can go and look at, and a run nobody measured has none.
		// Logging both as OVER BUDGET buries the second, which is the one that
		// used to pass silently.
		d.log("SPEND REFUSED %s (%s) — %s", runID, v.Reason, v.Detail)
	}
	d.recordQuestions(runID, c, env)
	d.recordLessons(runID, c, env)

	if c.Kind == KindSegment {
		created := 0
		if c.Capability == config.CapPlan {
			var gerr error
			if created, gerr = d.admitProposedItems(runID, c, env); gerr != nil {
				return res, true, gerr
			}
		}
		res.Created = created
		res.Admitted = env.Verdict == "pass" || created > 0
		d.log("PLANNING %s  %s on %s: %s", runID, c.Capability, c.Segment.ID, env.Verdict)
		if env.Verdict == "reject" {
			d.learnFromRejection(runID, c, env)
		}
		return res, true, d.advanceSegment(c.Segment.ID, runID, c.Capability, env.Verdict, created)
	}

	// An improver raises self-improvements as work items, and until this existed
	// nothing looked at them: they were parsed, stored in the envelope blob, and
	// dropped. The prompt asked for them, the agent produced them, and the fleet
	// silently did nothing — which is the exact failure this system is built to
	// make impossible everywhere else.
	//
	// They face the identical admission rules a planner's proposals face. An item
	// that is easier to create because of who proposed it is how a backlog fills
	// with work nobody can act on.
	if c.Capability == config.CapImprove && len(env.Outputs.WorkItems) > 0 {
		if n, gerr := d.admitProposedItems(runID, c, env); gerr != nil {
			return res, true, gerr
		} else {
			res.Created = n
		}
	}

	// The tool decides where the item goes. The agent reported a verdict; it was
	// never asked to name a state, so it cannot name a wrong one.
	//
	// A verification task that did NOT clear is recorded here, on the claim,
	// because a report of failure grants the item nothing. It can only hold the
	// stage back, and the stage has to settle with everything it observed even
	// when the transition off the back of that report is itself refused — a
	// failure the gate never got to confirm still has to reach the builder. A
	// claimed pass is the opposite: it clears a task, so it is recorded below,
	// after the gate has run the checks and the authority has admitted the edge.
	if c.From == authority.StateVerifying && authority.IsVerification(c.Capability) &&
		(env.Verdict == "fail" || env.Verdict == "reject") {
		if _, err := d.Led.Append(d.Actor, ledger.KindVerificationFailed, c.Item.ID,
			ledger.VerificationFailed{
				ItemID: c.Item.ID, Capability: c.Capability, RunID: runID,
				Worker: c.Worker, Round: c.Item.Attempts,
				Detail: oneLine(env.Summary, 300),
			}); err != nil {
			return res, true, err
		}
		d.log("NOT VERIFIED %s  %s failed under %s; the item waits for its siblings to report",
			c.Item.ID, c.Capability, c.Worker)
	}
	adv := authority.NextState(c.From, c.Capability, env.Verdict,
		config.Radius(c.Item.Radius), d.Cfg.Blast)
	if !adv.Inferred {
		d.log("NO MOVE %s reported %q from %s — %s", runID, env.Verdict, c.From, adv.Stall)
		res.Detail = adv.Stall
		return res, true, nil
	}
	to := adv.To
	gres, err := d.runGate(rctx, ws.Dir, c.From, to, env.Artifact)
	if err != nil {
		return res, true, err
	}
	if _, err := d.Led.Append(d.Actor, ledger.KindGateObserved, runID, ledger.GateObserved{
		RunID: runID, ItemID: c.Item.ID, Edge: gres.Edge, TreeSHA: gres.TreeSHA,
		Dirty: gres.Dirty, Artifact: gres.Artifact, Status: string(gres.Status), Checks: gres.JSON(),
	}); err != nil {
		return res, true, err
	}

	facts, err := authority.Gather(d.Led, c.Item.ID, runID, func(sha string) bool {
		return gate.CommitExists(d.Repo, sha)
	}, env.HeadSHA, now)
	if err != nil {
		return res, true, err
	}
	dec := authority.New(d.Cfg).Decide(authority.Request{
		Actor: d.Actor, Worker: c.Worker, RunID: runID,
		From: c.From, To: to, Env: env, Gate: gres,
		Reason: adv.Why, Now: now,
	}, facts)

	res.Admitted, res.Reason, res.Detail = dec.Admitted, string(dec.Reason), dec.Detail
	if !dec.Admitted {
		d.recordRefusal(runID, c, dec.Reason, dec.Detail)
		d.log("REFUSED %s  %s -> %s  [%s] %s", c.Item.ID, c.From, to, dec.Reason, dec.Detail)
		return res, true, nil
	}
	if err := d.admit(runID, c, to, env, adv.Why); err != nil {
		return res, true, err
	}

	// A verification task clears on the admitted self-edge and moves nothing:
	// the stage is left only when every task has reported, and Refresh derives
	// that from the record for the reason given there.
	//
	// It is recorded here, after the decision, because it was once recorded from
	// env.Verdict alone: before the gate ran and before the authority decided
	// anything. A tester whose envelope claimed a pass over a tree whose checks
	// the gate then observed RED had its task marked cleared anyway — the run was
	// refused, in the same chain, and the stage counted it regardless. Three of
	// those clear the whole stage, and the item leaves verification on nothing
	// but three claims. A refusal is evidence the claim was wrong; a task cleared
	// by one is not verified at all.
	//
	// The verdict is still a term, because the destination stopped carrying it.
	// A setback takes the SAME self-edge a pass does (authority/advance.go: a
	// failing task must not eject its in-flight siblings), and the tester's
	// verifying->verifying edge requires a green gate and matching claims but
	// not ReqVerdictPass — so `to == StateVerifying` alone admits a task that
	// reported its own failure over a green tree and then marks it cleared.
	// That is this same defect inverted: three tasks reporting fail would
	// complete the stage and send the item to reviewed with every verification
	// question answered no. The gate's answer and the claim have to agree, and
	// each is one term here.
	if c.From == authority.StateVerifying && to == authority.StateVerifying &&
		authority.IsVerification(c.Capability) && env.Verdict == "pass" {
		if _, err := d.Led.Append(d.Actor, ledger.KindVerificationPassed, c.Item.ID,
			ledger.VerificationPassed{
				ItemID: c.Item.ID, Capability: c.Capability, RunID: runID,
				Worker: c.Worker, Round: c.Item.Attempts,
			}); err != nil {
			return res, true, err
		}
		d.log("VERIFIED %s  %s cleared by %s", c.Item.ID, c.Capability, c.Worker)
	}
	d.log("ADVANCED %s  %s -> %s  (%s)", c.Item.ID, c.From, to, adv.Why)
	return res, true, d.advanceSegment(c.Item.SegmentID, runID, "", "", 0)
}

// admitProposedItems puts each generated item to the admission rules and
// records both answers.
func (d *Dispatcher) admitProposedItems(runID string, c Candidate, env *envelope.Envelope) (int, error) {
	all, err := d.Led.Items("")
	if err != nil {
		return 0, err
	}
	existing := map[string]bool{}
	for _, it := range all {
		existing[it.ID] = true
	}
	// A planning run carries its segment on the candidate; an improver run
	// carries an item, so the segment is the one that item belongs to. Filing a
	// self-improvement under the deliverable whose work provoked it keeps the
	// reason and the work in the same place.
	seg := c.Segment
	if seg.ID == "" && c.Item.SegmentID != "" {
		if s, err := d.Led.Segment(c.Item.SegmentID); err == nil {
			seg = s
		}
	}
	facts := authority.GenerationFacts{
		SegmentID: seg.ID, SegmentBrief: seg.Brief, ExistingIDs: existing,
	}
	created := 0
	for _, p := range env.Outputs.WorkItems {
		// Repairing a rejected breakdown: a proposal carrying an id that already
		// exists is an AMENDMENT to that item, not a duplicate of it.
		//
		// There was no other way to say it. An agent has no path that revises an
		// item, so a planner told to fix four rejected items did the only thing
		// the envelope allowed and filed four new ones — leaving the rejected
		// four in place, doubling the plan, and earning the same rejection with
		// more to read. Four became eight, then twelve, over two hours in which
		// nothing was built.
		//
		// Still a proposal, still decided here. The control plane admits the
		// amendment or refuses it exactly as it would a new item; what changed is
		// that "the same item, corrected" is now something an agent can express.
		if c.Repairing && existing[p.ID] {
			if err := d.amendProposedItem(runID, c, seg, p); err != nil {
				return created, err
			}
			created++
			continue
		}
		dec := authority.AdmitItem(d.Cfg, p, facts)
		if _, err := d.Led.Append(d.Actor, ledger.KindItemProposed, p.ID, ledger.ItemProposed{
			RunID: runID, Worker: c.Worker, SegmentID: seg.ID, ProposedID: p.ID,
			Title: p.Title, Admitted: dec.Admitted,
			Reason: string(dec.Reason), Detail: dec.Detail,
		}); err != nil {
			return created, err
		}
		if !dec.Admitted {
			d.log("REFUSED item %s  [%s] %s", p.ID, dec.Reason, dec.Detail)
			continue
		}
		radius := p.Radius
		if radius == "" {
			radius = string(config.RadiusNone)
		}
		if _, err := d.Led.Append(d.Actor, ledger.KindItemCreated, p.ID, ledger.ItemCreated{
			ID: p.ID, SegmentID: seg.ID, Title: p.Title, Area: p.Area, Radius: radius,
			Resources: p.Resources, FileScope: p.FileScope, DependsOn: p.DependsOn, Criteria: p.Criteria,
		}); err != nil {
			return created, err
		}
		existing[p.ID] = true
		created++
		d.log("CREATED %s  [%s]  %s", p.ID, p.Area, p.Title)
	}
	if created == 0 && len(env.Outputs.WorkItems) > 0 {
		d.log("GENERATION EMPTY %s proposed %d item(s) and every one was refused", runID, len(env.Outputs.WorkItems))
	}
	return created, nil
}

func (d *Dispatcher) promptVars(c Candidate, runID string, ws *Workspace) map[string]string {
	v := map[string]string{
		"run_id": runID, "worker_type": c.Worker,
		"workdir": ws.Dir, "envelope": ws.EnvelopePath,
		"areas": strings.Join(d.areaList(), ", "),
		// Every key a prompt may reference is always present, so an unfilled
		// placeholder never reaches an agent as literal template text.
		"work_item_id": "", "segment_id": "", "title": "", "state": "",
		"blast_radius": "", "resources": "", "file_scope": "", "criteria": "",
		"brief": "", "needed": "", "existing_items": "", "rationale": "",
		// What already went wrong, and what the fleet has learned. Empty is the
		// normal case and reads as nothing rather than as a heading with no
		// content under it.
		"what_went_wrong": "", "lessons": "", "attempt": "1", "rigour": "",
	}
	v["lessons"] = d.lessons(c.Worker, c.Item.Area)
	v["rigour"] = d.howMuchRigour(c)
	if c.Kind == KindSegment {
		v["segment_id"] = c.Segment.ID
		v["title"] = c.Segment.Title
		v["brief"] = c.Segment.Brief
		v["state"] = c.Segment.State
		v["needed"] = fmt.Sprintf("%d", c.Need)
		v["rationale"] = c.Segment.Rationale
		v["existing_items"] = d.existingItemSummary(c.Segment.ID)
		v["what_went_wrong"] = d.whyThePlanCameBack(c.Segment.ID)
		if c.Repairing {
			// Said in the one variable the prompt already builds its instruction
			// around. A planner told "produce up to 4" while repairing produces
			// four more; told "produce 0", it has to fix what is there.
			v["needed"] = "0"
		}
		if c.Capability != config.CapPlan {
			v["criteria"] = d.existingItemSummary(c.Segment.ID)
		}
		return v
	}
	v["work_item_id"] = c.Item.ID
	v["segment_id"] = c.Item.SegmentID
	v["title"] = c.Item.Title
	v["state"] = c.Item.State
	v["blast_radius"] = c.Item.Radius
	v["resources"] = strings.Join(c.Item.Resources, ", ")
	v["file_scope"] = strings.Join(c.Item.FileScope, ", ")
	v["criteria"] = renderCriteria(c.Item.Criteria)
	v["rationale"] = c.Item.Rationale
	v["attempt"] = fmt.Sprintf("%d", c.Item.Attempts+1)
	// One slot in the preamble, two headed sections: what went wrong, and what
	// has already been settled. A run that cannot see a decision re-asks it.
	v["what_went_wrong"] = joinSections(d.whatWentWrong(c.Item.ID, c.Item.Attempts),
		d.decisionsTaken(c.Item.ID))
	return v
}

func (d *Dispatcher) areaList() []string {
	var out []string
	for a := range d.Cfg.Routing {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (d *Dispatcher) existingItemSummary(segmentID string) string {
	items, err := d.Led.Items(segmentID)
	if err != nil || len(items) == 0 {
		return "(none yet)"
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "- %s [%s] %s — %s\n", it.ID, it.State, it.Area, it.Title)
	}
	return b.String()
}

func (d *Dispatcher) recordQuestions(runID string, c Candidate, env *envelope.Envelope) {
	for i, q := range env.Questions {
		// The control plane's own id for this question, and the one id that
		// cannot already be taken: a run id is minted unique and the index is
		// unique within the run. It is the fallback below as well as the name
		// for a question the agent did not name at all.
		derived := fmt.Sprintf("Q-%s-%d", runID, i+1)
		id := q.ID
		if id == "" {
			id = derived
		}
		// A question with no question in it is still recorded, because losing a
		// blocking one is worse than showing a defective one — but it is said
		// out loud rather than discovered by somebody staring at a card that
		// recommends something about nothing. It happened: two questions were
		// raised carrying only a lean, and the page rendered a blank heading
		// above a recommendation nobody could evaluate.
		if strings.TrimSpace(q.Text) == "" {
			d.log("MALFORMED QUESTION %s from %s (%s) — no `text`, so nothing states what is being asked. Whoever answers it is guessing at the question. Fix the role's prompt: `text` is the question, `lean` is only what the agent would do about it.",
				id, runID, c.Worker)
		}
		p := ledger.QuestionRaised{
			ID: id, ItemID: c.Item.ID, Blocking: q.Blocking, Text: q.Text,
			Lean: q.Lean, Evidence: q.Evidence, RaisedBy: runID,
		}
		_, err := d.Led.Append(d.Actor, ledger.KindQuestionRaised, id, p)
		if err == nil {
			continue
		}
		// The id an agent composes is not unique and was never guaranteed to
		// be: the preamble's own worked example handed every run the same one,
		// and `adlc_question.id` is a PRIMARY KEY, so the second question to
		// claim a taken id was refused by the chain and then dropped with a
		// log line nobody reads. A fully-formed non-blocking question went that
		// way. The question is the thing worth keeping; the id is not, so the
		// question is re-raised under the control plane's own id.
		//
		// Retried on any append failure rather than on a detected collision:
		// the append is what decides, and a question recorded under a second
		// name beats a question that only a log line remembers.
		if id != derived {
			p.ID = derived
			_, second := d.Led.Append(d.Actor, ledger.KindQuestionRaised, derived, p)
			if second == nil {
				d.log("QUESTION ID TAKEN %s from %s claimed id %q, which the chain refused (%v); it is recorded as %s instead, with its text intact",
					c.Item.ID, runID, id, err, derived)
				continue
			}
			err = second
		}
		// Swallowing this left a blocking question that nobody would ever
		// see, on an item that would sit still with no stated reason.
		d.log("QUESTION %s from %s could not be recorded: %v — the item it was raised against will look idle for no reason", id, runID, err)
	}
}

func (d *Dispatcher) runGate(ctx context.Context, dir string, from, to authority.State, artifact string) (*gate.Result, error) {
	r := &gate.Runner{Cfg: d.Cfg, Dir: dir, Artifact: artifact,
		Vars: map[string]string{"workdir": dir, "artifact": artifact}}
	return r.RunEdge(ctx, string(from), string(to))
}

// runCost prices one finished run for the two surfaces that read the figure,
// which need opposite things from a run nobody measured.
//
// Only a caller holding the usage block can tell an absent count from a counted
// one — ledger.Usage.Measured says why four zeros are not a count — and
// spend.CostOf is where that distinction is made at the pricing boundary. This
// is the dispatcher handing it what it alone knows.
//
// recorded goes in the ledger's cost column, which is read back and summed as
// money, so an unmeasured run records zero there and is counted as unmeasured
// from its usage block instead — a sentinel in that column would be totalled.
// forCap goes to the per-run cap, which must not clear a run whose cost nobody
// can bound; spend.Unknown carries what clearing one costs.
//
// unpriced is the model question alone, and is false for an unmeasured run
// because nothing was priced there. Reporting it true would print "no price
// entry" about a model that has one, and send an operator to add a price that
// is already in the table.
func runCost(b config.Budget, model string, u ledger.Usage) (recorded, forCap spend.Micros, unpriced bool) {
	measured := u.Measured()
	c, priced := spend.CostOf(b, model, u, measured)
	if !measured {
		return c, spend.Unknown, false
	}
	return c, c, !priced
}

func (d *Dispatcher) finish(runID, verdict, envSHA, headSHA, artifact string, u ledger.Usage, cost int64) {
	_, _ = d.Led.Append(d.Actor, ledger.KindRunFinished, runID, ledger.RunFinished{
		RunID: runID, Verdict: verdict, EnvelopeSHA: envSHA, HeadSHA: headSHA,
		Artifact: artifact, Model: d.Cfg.Budget.DefaultModel, Usage: u, CostMicros: cost,
	})
}

func (d *Dispatcher) recordRefusal(runID string, c Candidate, reason authority.Reason, detail string) {
	_, _ = d.Led.Append(d.Actor, ledger.KindTransitionRefused, c.Item.ID, ledger.TransitionOutcome{
		RunID: runID, ItemID: c.Item.ID, Worker: c.Worker,
		From: string(c.From), To: "", Reason: string(reason), Detail: detail,
	})
}

func (d *Dispatcher) admit(runID string, c Candidate, to authority.State, env *envelope.Envelope, why string) error {
	if _, err := d.Led.Append(d.Actor, ledger.KindTransitionAdmitted, c.Item.ID, ledger.TransitionOutcome{
		RunID: runID, ItemID: c.Item.ID, Worker: c.Worker, From: string(c.From), To: string(to),
	}); err != nil {
		return err
	}
	var blockedWhy string
	if to == authority.StateBlocked {
		if q, ok := env.BlockingQuestion(); ok {
			blockedWhy = q.Text
		} else {
			blockedWhy = env.EnvironmentFailure
		}
	}
	_, err := d.Led.Append(d.Actor, ledger.KindItemTransitioned, c.Item.ID, ledger.ItemTransitioned{
		ItemID: c.Item.ID, From: string(c.From), To: string(to), RunID: runID,
		Reason: why, PlanDigest: env.PlanDigest, BlockedWhy: blockedWhy,
		BumpAttempt: to == authority.StateInProgress && c.From != authority.StateReady,
	})
	return err
}

// mintRunID makes a run's single name, and refuses one already taken.
// mintRunID gives a run the one name it wears everywhere — its id, its
// workspace and its lease key.
//
// The check and the claim are one critical section, and ids handed out but not
// yet written to the ledger are remembered until they are. Asking the ledger
// alone is a check-then-act race: two dispatches in the same second both see
// the same id free and both take it, and the second one dies on the unique
// constraint after it has already taken a lease and built a worktree. That
// never fired while a lane dispatched one at a time, and fires immediately once
// it does not — which is the shape of most concurrency bugs, and the reason
// this is a lock rather than a retry.
func (d *Dispatcher) mintRunID(worker string, now time.Time) (string, error) {
	d.mintMu.Lock()
	defer d.mintMu.Unlock()
	if d.minted == nil {
		d.minted = map[string]bool{}
	}
	prefix := initials(worker)
	stamp := now.UTC().Format("20060102T150405Z")
	for n := 1; n <= 99; n++ {
		id := fmt.Sprintf("%s-%s-%02d", prefix, stamp, n)
		if d.minted[id] {
			continue
		}
		taken, err := d.Led.RunExists(id)
		if err != nil {
			return "", err
		}
		if !taken {
			d.minted[id] = true
			return id, nil
		}
	}
	return "", fmt.Errorf("could not mint a free run id for %s at %s after 99 attempts", worker, stamp)
}

// releaseRunID forgets an id once the ledger knows about it, or once the
// dispatch that reserved it gave up. Holding them all would grow without bound
// in a long-running scheduler.
func (d *Dispatcher) releaseRunID(id string) {
	d.mintMu.Lock()
	defer d.mintMu.Unlock()
	delete(d.minted, id)
}

func initials(worker string) string {
	parts := strings.FieldsFunc(worker, func(r rune) bool { return r == '-' || r == '_' })
	var b strings.Builder
	for _, p := range parts {
		if p != "" {
			b.WriteByte(p[0])
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return strings.ToLower(b.String())
}

func renderCriteria(cs []string) string {
	if len(cs) == 0 {
		return "(none declared — this item cannot be verified until it has some)"
	}
	var b strings.Builder
	for i, c := range cs {
		fmt.Fprintf(&b, "AC-%d. %s\n", i+1, c)
	}
	return b.String()
}

// advanceSegment recomputes a deliverable's roadmap position and records the
// move if there is one.
//
// It runs after every event that could change the answer, so the roadmap is a
// projection of what has happened rather than a board somebody remembers to
// update.
func (d *Dispatcher) advanceSegment(segmentID, runID, capability, verdict string, created int) error {
	if segmentID == "" {
		return nil
	}
	seg, err := d.Led.Segment(segmentID)
	if err != nil {
		return nil // a run against no segment is not an error here
	}
	items, err := d.Led.Items(segmentID)
	if err != nil {
		return err
	}
	from := authority.SegmentState(seg.State)
	adv := authority.NextSegmentState(from, capability, verdict, created)
	if !adv.Inferred {
		adv = authority.SegmentFromItems(from, items)
	}
	if !adv.Inferred || string(adv.To) == seg.State {
		return nil
	}
	if _, err := d.Led.Append(d.Actor, ledger.KindSegmentAdvanced, seg.ID, ledger.SegmentAdvanced{
		SegmentID: seg.ID, From: seg.State, To: string(adv.To), RunID: runID, Why: adv.Why,
	}); err != nil {
		return err
	}
	d.log("ROADMAP %s  %s -> %s  (%s)", seg.ID, seg.State, adv.To, adv.Why)
	return nil
}

// Refresh advances every status the control plane can compute for itself,
// before it looks for work.
//
// Readiness is computed, never proposed. An item is queued because a
// dependency is open, and it becomes ready the moment that stops being true
// — there is no agent involved, no judgement, and nothing to get wrong.
// Leaving it to a run would mean an item could sit
// dispatchable-but-not-dispatched for as long as it took somebody to notice,
// which is exactly the kind of silent stall that is impossible to distinguish
// from an empty backlog.
func (d *Dispatcher) Refresh() (int, error) {
	// One at a time. Refresh is called from every lane goroutine, and the moves
	// it makes are read-check-write against the ledger: two passes both saw a
	// verification stage complete, both appended the transition out of it, and
	// the chain recorded a move FROM a state the item had already left.
	//
	// The item's state came out right, which is the dangerous part — the defect
	// was visible only as a duplicated line in a log. An append-only record
	// cannot take that back, and a transition that did not happen is exactly
	// the kind of entry that makes the rest of the chain untrustworthy.
	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()
	items, err := d.Led.Items("")
	if err != nil {
		return 0, err
	}
	state := map[string]authority.State{}
	for _, it := range items {
		state[it.ID] = authority.State(it.State)
	}
	moved := 0
	for _, it := range items {
		// A stage whose tasks all cleared is a computed fact, not a proposal.
		// Readiness is derived here for the same reason dependency readiness is:
		// leaving it to whichever run finished last would make the item's state
		// depend on a race between three runs that all passed.
		if authority.State(it.State) == authority.StateVerifying {
			passed, failed, verr := d.Led.VerificationReportsFor(it.ID, it.Attempts)
			if verr != nil {
				return moved, verr
			}
			if !authority.VerificationSettled(passed, failed) {
				continue
			}
			to, reason, detail := string(authority.StateReviewed), "verification_complete",
				"every task in the stage passed: "+strings.Join(authority.VerificationCapabilities(), ", ")
			if len(failed) > 0 {
				// Backward, once, with everything the stage observed. The
				// builder gets all three complaints at once instead of one per
				// round, and adversarial review still outranks the rest: a
				// blocker is a rejection whoever else agreed.
				to, reason = string(authority.StateInProgress), "verification_failed"
				if _, blocked := failed[config.CapValidate]; blocked {
					to = string(authority.StateRejected)
				}
				detail = "the stage reported and " + plural(len(failed), "task", "tasks") +
					" did not clear — " + failedTasks(failed)
			}
			if _, err := d.Led.Append(d.Actor, ledger.KindTransitionAdmitted, it.ID, ledger.TransitionOutcome{
				ItemID: it.ID, From: it.State, To: to, Reason: reason, Detail: detail,
			}); err != nil {
				return moved, err
			}
			if _, err := d.Led.Append(d.Actor, ledger.KindItemTransitioned, it.ID, ledger.ItemTransitioned{
				ItemID: it.ID, From: it.State, To: to, Reason: reason,
				// A new round, so this round's verdicts stop counting and every
				// task is asked again of the work that comes back.
				BumpAttempt: to == string(authority.StateInProgress),
			}); err != nil {
				return moved, err
			}
			if len(failed) > 0 {
				d.log("NOT VERIFIED %s — %s", it.ID, detail)
			} else {
				d.log("VERIFIED %s — every task in the stage passed; it leaves verification", it.ID)
			}
			moved++
			continue
		}
		if authority.State(it.State) != authority.StateQueued {
			continue
		}
		ready := true
		var waiting []string
		for _, dep := range it.DependsOn {
			if state[dep] != authority.StateDone {
				ready = false
				waiting = append(waiting, dep)
			}
		}
		if !ready {
			continue
		}
		why := "dependencies are done"
		if len(it.DependsOn) == 0 {
			why = "this item waits on nothing"
		}
		_ = waiting
		if _, err := d.Led.Append(d.Actor, ledger.KindTransitionAdmitted, it.ID, ledger.TransitionOutcome{
			ItemID: it.ID, From: string(authority.StateQueued), To: string(authority.StateReady),
		}); err != nil {
			return moved, err
		}
		if _, err := d.Led.Append(d.Actor, ledger.KindItemTransitioned, it.ID, ledger.ItemTransitioned{
			ItemID: it.ID, From: string(authority.StateQueued), To: string(authority.StateReady), Reason: why,
		}); err != nil {
			return moved, err
		}
		moved++
	}
	if moved > 0 {
		d.log("READY %d item(s) became dispatchable", moved)
	}

	// The other two states nothing polls for. A lane drains a state its
	// capability answers for; these two are answered by a person, and until
	// this pass existed the work simply stopped there.
	resumed, err := d.resumeBlocked(items)
	if err != nil {
		return moved, err
	}
	reworked, err := d.routeRework(items)
	if err != nil {
		return moved + resumed, err
	}
	// And the third: an item that has run out of attempts. The limit used to be
	// enforced by making the item invisible to every lane, which is a stall
	// wearing a policy's clothes.
	escalated, err := d.escalateAtLimit(items)
	if err != nil {
		return moved + resumed + reworked, err
	}
	return moved + resumed + reworked + escalated, nil
}

// claimForWork records the waiting -> working edge before a run starts.
//
// Dispatching IS that transition: the moment a run holds the lease and has a
// workspace, the item is being worked on, and saying so immediately means an
// operator watching the board sees the state the fleet is actually in rather
// than the state it was in before the run began.
//
// Which working state that is comes from authority.PickedUp, so every waiting
// state in the lifecycle is claimed the same way and a stage added later cannot
// quietly keep showing as queued while a run works it.
func (d *Dispatcher) claimForWork(runID string, c *Candidate, now time.Time) error {
	if c.Kind != KindItem {
		return nil
	}
	to, ok := authority.PickedUp(c.From)
	if !ok {
		return nil
	}
	reason := "dispatched to " + c.Worker
	facts, err := authority.Gather(d.Led, c.Item.ID, "", nil, "", now)
	if err != nil {
		return err
	}
	dec := authority.New(d.Cfg).Decide(authority.Request{
		Actor: d.Actor, RunID: runID, From: c.From, To: to,
		Reason: reason, Now: now,
	}, facts)
	if !dec.Admitted {
		d.recordRefusal(runID, *c, dec.Reason, dec.Detail)
		return fmt.Errorf("[%s] %s", dec.Reason, dec.Detail)
	}
	if _, err := d.Led.Append(d.Actor, ledger.KindTransitionAdmitted, c.Item.ID, ledger.TransitionOutcome{
		RunID: runID, ItemID: c.Item.ID, Worker: c.Worker,
		From: string(c.From), To: string(to),
	}); err != nil {
		return err
	}
	if _, err := d.Led.Append(d.Actor, ledger.KindItemTransitioned, c.Item.ID, ledger.ItemTransitioned{
		ItemID: c.Item.ID, From: string(c.From), To: string(to),
		RunID: runID, Reason: reason,
	}); err != nil {
		return err
	}
	c.From = to
	c.Item.State = string(to)
	return nil
}
