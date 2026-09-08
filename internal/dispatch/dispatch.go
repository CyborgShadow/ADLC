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
	Priority   int
	Why        string
}

// Key is what a candidate leases: the item id, or the segment for generation,
// so that two planners cannot both fill one backlog.
func (c Candidate) Key() string {
	if c.Kind == KindSegment {
		return "plan-" + c.Segment.ID
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

	var out []Candidate
	for _, it := range items {
		st := authority.State(it.State)
		capability, prio := authority.CapabilityFor(st)
		if capability == "" {
			continue
		}
		if f.Capability != "" && f.Capability != capability {
			continue
		}
		if !f.allowsArea(it.Area) {
			continue
		}
		if it.Attempts >= d.Cfg.Dispatch.MaxAttempts {
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
				// The handoff gate. A breakdown nobody has reviewed does not become work,
				// and the reason is stated rather than the item silently not appearing.
				d.log("HELD %s — %s is %s; the breakdown has not been reviewed against its brief yet",
					it.ID, seg.ID, seg.State)
				continue
			}
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

	for _, s := range segs {
		st := authority.SegmentState(s.State)
		capability, prio := authority.SegmentCapabilityFor(st)
		if capability == "" {
			continue
		}
		if f.Capability != "" && f.Capability != capability {
			continue
		}
		// Planning only produces more work when the deliverable actually wants
		// it. A planner that runs on a timer regardless of backlog depth invents
		// work to justify its own cadence.
		need := 0
		if capability == config.CapPlan {
			need = authority.SegmentNeedsWork(s.TargetOpen, openBySegment[s.ID])
			if need == 0 {
				continue
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
			Worker: worker, Need: need, Priority: prio,
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

func (d *Dispatcher) dispatchOne(ctx context.Context, c Candidate, now time.Time) (TickResult, bool, error) {
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

	ws, err := d.prepareWorkspace(runID)
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
	if _, err := d.Led.Append(d.Actor, ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: c.Worker, ItemID: c.Item.ID, SegmentID: segID,
		PromptID: asm.PromptID, PromptSHA: asm.SHA, BaseSHA: baseSHA, WorkDir: ws.Dir, Branch: ws.Branch,
		Model: d.Cfg.Budget.DefaultModel,
	}); err != nil {
		return TickResult{}, false, err
	}
	started = true
	d.releaseRunID(runID)
	d.log("DISPATCH %s  %s  %s", runID, c.Worker, c.Why)

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
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A slot is held only for as long as an agent is actually running. Taking
	// it earlier would count selection and bookkeeping against a ceiling that
	// exists to bound money and machine load, neither of which they spend.
	if err := d.limit().enter(rctx); err != nil {
		return TickResult{}, false, err
	}
	agentOut, agentErr := d.Runner.Invoke(rctx, Invocation{
		RunID: runID, WorkerType: c.Worker, ItemID: c.Item.ID, SegmentID: segID,
		PromptPath: promptPath, PromptText: asm.Text,
		WorkDir: ws.Dir, EnvelopePath: ws.EnvelopePath, Timeout: timeout,
		LedgerPath: d.Led.Path(),
	})
	d.limit().leave()

	raw := agentOut.Envelope
	if len(raw) == 0 {
		if b, rerr := os.ReadFile(ws.EnvelopePath); rerr == nil {
			raw = b
		}
	}
	if agentErr != nil && len(raw) == 0 {
		d.finish(runID, "fail", "", "", "", ledger.Usage{}, 0)
		res.Reason = string(authority.ReasonNoEnvelope)
		res.Detail = fmt.Sprintf("the agent returned an error and wrote no envelope: %v", agentErr)
		d.log("NO ENVELOPE %s — %s", runID, res.Detail)
		return res, true, nil
	}

	env, err := envelope.Parse(raw)
	if err != nil {
		// A malformed envelope is a refusable proposal, not a dead run.
		d.finish(runID, "fail", "", "", "", ledger.Usage{}, 0)
		d.recordRefusal(runID, c, authority.ReasonMalformedEnvelope, err.Error())
		res.Reason, res.Detail = string(authority.ReasonMalformedEnvelope), err.Error()
		d.log("MALFORMED %s — %s", runID, err.Error())
		return res, true, nil
	}

	usage := ledger.Usage(env.Usage)
	model := env.Model
	if model == "" {
		model = d.Cfg.Budget.DefaultModel
	}
	cost, priced := spend.Cost(d.Cfg.Budget, model, usage)
	if !priced {
		d.log("UNPRICED %s used model %q, which has no price entry; its cost is unknown, not zero", runID, model)
	}
	if _, berr := d.Led.PutBlob(ledger.BlobEnvelope, env.Raw()); berr != nil {
		return res, true, berr
	}
	d.finish(runID, env.Verdict, env.SHA(), env.HeadSHA, env.Artifact, usage, int64(cost))
	if v := spend.CheckRun(d.Cfg.Budget, cost); !v.OK {
		d.log("OVER BUDGET %s — %s", runID, v.Detail)
	}
	d.recordQuestions(runID, c, env)

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
	}
	if c.Kind == KindSegment {
		v["segment_id"] = c.Segment.ID
		v["title"] = c.Segment.Title
		v["brief"] = c.Segment.Brief
		v["state"] = c.Segment.State
		v["needed"] = fmt.Sprintf("%d", c.Need)
		v["rationale"] = c.Segment.Rationale
		v["existing_items"] = d.existingItemSummary(c.Segment.ID)
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
		id := q.ID
		if id == "" {
			id = fmt.Sprintf("Q-%s-%d", runID, i+1)
		}
		_, _ = d.Led.Append(d.Actor, ledger.KindQuestionRaised, id, ledger.QuestionRaised{
			ID: id, ItemID: c.Item.ID, Blocking: q.Blocking, Text: q.Text,
			Lean: q.Lean, Evidence: q.Evidence, RaisedBy: runID,
		})
	}
}

func (d *Dispatcher) runGate(ctx context.Context, dir string, from, to authority.State, artifact string) (*gate.Result, error) {
	r := &gate.Runner{Cfg: d.Cfg, Dir: dir, Artifact: artifact,
		Vars: map[string]string{"workdir": dir, "artifact": artifact}}
	return r.RunEdge(ctx, string(from), string(to))
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
	return moved + resumed + reworked, nil
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
