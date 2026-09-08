// Package config is the declared surface that makes this ADLC generic.
//
// So checks are DECLARED here, not compiled in: a command, the channel its
// verdict is read from, and the lifecycle edges it is required for. The two
// judging rules that were earned the hard way survive as verdict RULES rather
// than as special cases —
//
//   - VerdictOutputEmpty: `gofmt -l` exits 0 whether or not it printed the
//     files it would rewrite, so for some commands the OUTPUT is the verdict
//     and the exit code says nothing.
//   - VerdictGoTestJSON / VerdictCountMin: a run that discovered zero tests is
//     a FAILURE, not a pass. A filter that silently matches nothing must never
//     report green.
//
// Anything reading a check's result — the gate, and the claim matcher that
// judges an agent's account of it — must read it through the same rule. Two
// definitions of one verdict is how an honest envelope came to be refused
// while a doctored one was admitted.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the config format this build understands.
const SchemaVersion = 1

// Config is the whole declared surface of a project's delivery system.
type Config struct {
	// Comment lets a config carry its own explanation. Configs are read by people
	// under pressure, and a format that forbids a note pushes the note somewhere
	// nobody looks.
	Comment string `json:"_comment,omitempty"`
	Version int    `json:"version"`
	Project string `json:"project"`

	// SourceRoots scopes every tree-walking guard.
	//
	// It refused real work on a tree nobody had edited. A guard needs a stated
	// scope as much as it needs a firing case, and the scope belongs beside the
	// checks rather than inside four separate walkers each keeping its own skip
	// list.
	RoutingComment string   `json:"_routing_comment,omitempty"`
	SourceRoots    []string `json:"source_roots"`

	Checks   []Check           `json:"checks"`
	Workers  []WorkerDecl      `json:"workers"`
	Routing  map[string]string `json:"routing"` // area tag -> owning worker type
	Blast    BlastPolicy       `json:"blast"`
	Budget   Budget            `json:"budget"`
	Lease    LeasePolicy       `json:"lease"`
	Dispatch DispatchPolicy    `json:"dispatch"`
	Prompts  PromptPolicy      `json:"prompts"`
	Loops    []LoopDecl        `json:"loops,omitempty"`
	Server   ServerPolicy      `json:"server"`

	path string
}

// Check is one executable verification step.
type Check struct {
	Comment string `json:"_comment,omitempty"`
	ID      string `json:"id"`
	Kind    Kind   `json:"kind"`
	// Command is argv. It is never passed through a shell: a shell would make the
	// recorded command and the executed command two different strings.
	Command []string `json:"command"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`

	Verdict VerdictRule `json:"verdict"`
	// ExpectPattern is used by the pattern rules. Anchored as written.
	ExpectPattern string `json:"expect_pattern,omitempty"`
	// AllowedExits is used by VerdictExitIn. `terraform plan -detailed-exitcode`
	// returns 2 for "changes present", which is a success.
	AllowedExits []int `json:"allowed_exits,omitempty"`
	// MinCount is used by VerdictCountMin. Zero discovered units is a failure, so
	// a scanner check declares how many it must find to have run at all.
	MinCount int `json:"min_count,omitempty"`
	// CountPattern must capture one integer group.
	CountPattern string `json:"count_pattern,omitempty"`

	// RequiredFor names the lifecycle edges this check gates, as "from->to".
	RequiredFor []string `json:"required_for"`

	// BindsArtifact marks a behavioural check: one that runs against the
	// assembled artifact (a booted image, a provisioned host) rather than against
	// source. Its evidence is only meaningful next to the digest of what it ran
	// against, so the gate refuses to record it without one.
	BindsArtifact bool `json:"binds_artifact,omitempty"`

	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	compiled *regexp.Regexp
	counter  *regexp.Regexp
}

// Kind separates the depths of verification. It is descriptive, not
// behavioural: it drives reporting and the artifact-binding requirement, never
// the verdict.
type Kind string

const (
	// KindSource judges the source tree: format, lint, unit tests, build.
	KindSource Kind = "source"
	// KindDryRun judges a proposed change without making it: a plan, a diff, a
	// --check run. Its output digest is what an approval approves.
	KindDryRun Kind = "dryrun"
	// KindBehavioural judges the assembled, running artifact.
	KindBehavioural Kind = "behavioural"
)

// VerdictRule names the channel a check's verdict is read from.
type VerdictRule string

const (
	VerdictExitZero       VerdictRule = "exit_zero"
	VerdictExitIn         VerdictRule = "exit_in"
	VerdictOutputEmpty    VerdictRule = "output_empty"
	VerdictOutputNonEmpty VerdictRule = "output_nonempty"
	VerdictOutputMatches  VerdictRule = "output_matches"
	VerdictOutputNotMatch VerdictRule = "output_not_matches"
	VerdictGoTestJSON     VerdictRule = "go_test_json"
	VerdictCountMin       VerdictRule = "count_min"
)

// TurnsOnExitCode reports whether a rule's verdict is the exit code and
// nothing else.
//
// Exported because the claim matcher has to judge an AGENT's account of a
// check in the same channel the gate judged its own observation in. When those
// two differed, an envelope recording a truthful exit 0 over an unformatted
// tree was refused as a discrepancy while one that deleted the line entirely
// was not.
func (r VerdictRule) TurnsOnExitCode() bool {
	switch r {
	case VerdictExitZero, VerdictExitIn:
		return true
	default:
		return false
	}
}

// WorkerDecl registers a dispatchable worker type.
//
// A worker that has produced nothing must still be enumerable, so that its
// silence renders as an alarm instead of as an absence.
type WorkerDecl struct {
	Comment     string   `json:"_comment,omitempty"`
	Type        string   `json:"type"`
	Layer       string   `json:"layer"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Areas       []string `json:"areas,omitempty"`
	// Capabilities are the lifecycle stages this worker may move work through.
	// They are capabilities rather than role names so the authority table does
	// not have to know what any one project calls its agents.
	//
	// The split that matters most is test and judge from implement: a lane that
	// writes work and also judges it will always find it acceptable.
	Capabilities []string `json:"capabilities"`
	// LowCadence marks a worker that is expected to run rarely, so the never-run
	// roll call reports it without raising it as an alarm.
	LowCadence bool `json:"low_cadence,omitempty"`
}

// BlastPolicy decides which changes an agent may apply unattended.
//
// Both state machines assumed every action was undone by `git revert`: a
// rejection routes back to in_progress, a bad merge is a revert commit, and
// the worst outcome of a wrong decision is wasted tokens. None of that holds
// when the artifact is a running machine.
type BlastPolicy struct {
	Comment string `json:"_comment,omitempty"`
	// AutoApplyMax is the highest radius that may be applied with no approval
	// row. Anything above it stops at awaiting_approval.
	AutoApplyMax Radius `json:"auto_apply_max"`
	// NamedApproverMin is the radius from which the approval must name a human
	// rather than merely exist.
	NamedApproverMin Radius `json:"named_approver_min"`
	// TwoApprovalsMin is the radius from which two distinct approvers are
	// required.
	TwoApprovalsMin Radius `json:"two_approvals_min"`
	// ApprovalTTLMinutes expires an approval that has gone stale. An approval
	// approves a specific plan digest; this bounds how long that stays true even
	// when the digest has not moved.
	ApprovalTTLMinutes int `json:"approval_ttl_minutes"`
}

// Radius is the reach of a change if it is wrong.
type Radius string

const (
	RadiusNone   Radius = "none"   // source only; no apply phase exists
	RadiusHost   Radius = "host"   // one machine
	RadiusFleet  Radius = "fleet"  // a group of machines
	RadiusRegion Radius = "region" // a region or an account
	RadiusGlobal Radius = "global" // everything
)

var radiusOrder = map[Radius]int{
	RadiusNone: 0, RadiusHost: 1, RadiusFleet: 2, RadiusRegion: 3, RadiusGlobal: 4,
}

// Rank orders a radius. An unknown radius ranks above global rather than below
// none: an unrecognised value must fail closed, because the one thing this
// policy exists to prevent is an unattended apply.
func (r Radius) Rank() int {
	if n, ok := radiusOrder[r]; ok {
		return n
	}
	return len(radiusOrder)
}

// Known reports whether the radius is one this build understands.
func (r Radius) Known() bool { _, ok := radiusOrder[r]; return ok }

// Radii lists the declared radii, weakest first.
func Radii() []Radius {
	out := make([]Radius, 0, len(radiusOrder))
	for r := range radiusOrder {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rank() < out[j].Rank() })
	return out
}

// Budget caps spend. An autonomous dispatcher with no spend accounting has no
// throttle, so the accounting is wired to a refusal here rather than to a
// chart.
type Budget struct {
	Comment string `json:"_comment,omitempty"`
	// PriceMicrosPerMTok is keyed by model, then by token class: "input",
	// "output", "cache_read", "cache_write".
	PriceMicrosPerMTok map[string]map[string]int64 `json:"price_micros_per_mtok"`
	DefaultModel       string                      `json:"default_model"`
	// PerRunMicros refuses a single run whose recorded usage exceeds it.
	PerRunMicros int64 `json:"per_run_micros"`
	// PerDayMicros refuses a dispatch once the day's recorded spend exceeds it.
	// Zero means unlimited, and is reported as unlimited rather than as zero so
	// that "no budget set" cannot read as "budget exhausted".
	PerDayMicros int64 `json:"per_day_micros"`
	// PerSegmentMicros does the same per build segment.
	PerSegmentMicros int64 `json:"per_segment_micros"`
}

// LeasePolicy configures the ephemeral claim store.
type LeasePolicy struct {
	Comment    string `json:"_comment,omitempty"`
	Dir        string `json:"dir"`
	TTLMinutes int    `json:"ttl_minutes"`
	// StripTokens are words removed before two claims are compared for subject
	// overlap. Without it a shared generic noun refuses unrelated siblings; with
	// too many, two runs hold one subject under keys sharing not one word.
	StripTokens []string `json:"strip_tokens,omitempty"`
}

type DispatchPolicy struct {
	Comment string `json:"_comment,omitempty"`
	// Command is the argv template for invoking an agent. The prompt is written
	// to a file and its path substituted for {{prompt}}; the run's envelope is
	// expected at {{envelope}}.
	Command []string `json:"command"`
	// WorkDirTemplate is where a run's isolated tree is created. It must contain
	// {{run_id}}: a run wears two names, its workspace and its run id, and a run
	// whose two names diverged was invisible to every guard that keyed on either.
	WorkDirTemplate string `json:"workdir_template"`
	MaxConcurrent   int    `json:"max_concurrent"`
	MaxAttempts     int    `json:"max_attempts"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	// Isolation selects how a run's tree is prepared: "worktree" (git worktree
	// per run), "copy", or "none".
	Isolation string `json:"isolation"`
	// Trunk is the branch the merge queue lands on. Empty means "main".
	Trunk string `json:"trunk,omitempty"`
}

// PromptPolicy governs the prompt library.
type PromptPolicy struct {
	Comment string `json:"_comment,omitempty"`
	Dir     string `json:"dir"`
	// PreambleFile is assembled into every prompt, so a fleet-wide policy change
	// is one edit rather than N.
	PreambleFile string `json:"preamble_file"`
	// MandatoryClauses must appear verbatim in every role prompt. The gate checks
	// them, which is what stops a run deleting a safety clause from its own
	// instructions.
	MandatoryClauses []string `json:"mandatory_clauses"`
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(stripBOM(string(raw))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.path = path
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// stripBOM removes a UTF-8 byte-order mark.
//
// Anything read from an agent or edited on Windows gets this treatment.
func stripBOM(s string) string { return strings.TrimPrefix(s, "\ufeff") }

// StripBOM is the exported form, used wherever agent-authored bytes are read.
func StripBOM(b []byte) []byte {
	return []byte(stripBOM(string(b)))
}

func (c *Config) validate() error {
	if c.Version != SchemaVersion {
		return fmt.Errorf("config version %d: this build understands version %d", c.Version, SchemaVersion)
	}
	if strings.TrimSpace(c.Project) == "" {
		return fmt.Errorf("project name is required")
	}
	if len(c.SourceRoots) == 0 {
		return fmt.Errorf("source_roots is required: every tree-walking guard is scoped by it, and an unscoped guard refuses work on files nobody edited")
	}
	if len(c.Checks) == 0 {
		return fmt.Errorf("no checks declared: a gate with no checks reports green over nothing")
	}
	seen := map[string]bool{}
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.ID == "" {
			return fmt.Errorf("checks[%d]: id is required", i)
		}
		if seen[ch.ID] {
			return fmt.Errorf("checks[%d]: duplicate id %q", i, ch.ID)
		}
		seen[ch.ID] = true
		if len(ch.Command) == 0 {
			return fmt.Errorf("check %s: command is required", ch.ID)
		}
		if ch.Kind == "" {
			ch.Kind = KindSource
		}
		switch ch.Kind {
		case KindSource, KindDryRun, KindBehavioural:
		default:
			return fmt.Errorf("check %s: unknown kind %q", ch.ID, ch.Kind)
		}
		if err := ch.compile(); err != nil {
			return err
		}
		for _, e := range ch.RequiredFor {
			if !strings.Contains(e, "->") {
				return fmt.Errorf("check %s: required_for %q is not an edge (want \"from->to\")", ch.ID, e)
			}
		}
		if ch.Kind == KindBehavioural && !ch.BindsArtifact {
			return fmt.Errorf("check %s: a behavioural check must set binds_artifact — its output means nothing without the digest of what it ran against", ch.ID)
		}
	}
	if len(c.Workers) == 0 {
		return fmt.Errorf("no workers declared")
	}
	done := 0
	wseen := map[string]bool{}
	for _, w := range c.Workers {
		if w.Type == "" {
			return fmt.Errorf("worker with no type")
		}
		if wseen[w.Type] {
			return fmt.Errorf("duplicate worker type %q", w.Type)
		}
		wseen[w.Type] = true
		for _, c := range w.Capabilities {
			if !knownCapability(c) {
				return fmt.Errorf("worker %s: unknown capability %q", w.Type, c)
			}
			if c == CapValidate {
				done++
			}
		}
	}
	if done == 0 {
		return fmt.Errorf("no worker declares the %q capability: the terminal edge needs an owner, and it must not be the worker that did the work", CapValidate)
	}
	for area, owner := range c.Routing {
		if !wseen[owner] {
			return fmt.Errorf("routing: area %q is owned by %q, which is not a declared worker — an area tag with no owner strands every item filed under it, and an unreachable item looks exactly like one nobody has got round to", area, owner)
		}
	}
	if !c.Blast.AutoApplyMax.Known() {
		return fmt.Errorf("blast.auto_apply_max %q is not a known radius", c.Blast.AutoApplyMax)
	}
	if c.Lease.TTLMinutes <= 0 {
		c.Lease.TTLMinutes = 180
	}
	if c.Lease.Dir == "" {
		c.Lease.Dir = filepath.Join(".adlc", "leases")
	}
	if c.Dispatch.MaxAttempts <= 0 {
		c.Dispatch.MaxAttempts = 3
	}
	if c.Dispatch.MaxConcurrent <= 0 {
		c.Dispatch.MaxConcurrent = 1
	}
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8099"
	}
	if c.Server.RefreshSeconds <= 0 {
		c.Server.RefreshSeconds = 15
	}
	seenLoop := map[string]bool{}
	for i := range c.Loops {
		lp := &c.Loops[i]
		if lp.Name == "" {
			return fmt.Errorf("loops[%d]: name is required — the tick record keys on it, and an unnamed loop has no liveness history", i)
		}
		if seenLoop[lp.Name] {
			return fmt.Errorf("two loops are named %q; their tick records would merge and neither would be measurable", lp.Name)
		}
		seenLoop[lp.Name] = true
		if lp.EverySeconds <= 0 {
			lp.EverySeconds = 300
		}
		if lp.MaxPerTick <= 0 {
			lp.MaxPerTick = 1
		}
		if lp.Capability != "" && !knownCapability(lp.Capability) {
			return fmt.Errorf("loop %s: unknown capability %q", lp.Name, lp.Capability)
		}
		if lp.Worker != "" && c.Worker(lp.Worker) == nil {
			return fmt.Errorf("loop %s: worker %q is not declared", lp.Name, lp.Worker)
		}
		if lp.Capability != "" && len(c.WorkersWith(lp.Capability)) == 0 {
			return fmt.Errorf("loop %s drains %q work and no declared worker holds that capability, so it would fire forever and find nothing", lp.Name, lp.Capability)
		}
	}
	if c.Blast.ApprovalTTLMinutes <= 0 {
		c.Blast.ApprovalTTLMinutes = 24 * 60
	}
	return nil
}

func (ch *Check) compile() error {
	switch ch.Verdict {
	case VerdictExitZero, VerdictOutputEmpty, VerdictOutputNonEmpty, VerdictGoTestJSON:
	case VerdictExitIn:
		if len(ch.AllowedExits) == 0 {
			return fmt.Errorf("check %s: exit_in needs allowed_exits", ch.ID)
		}
	case VerdictOutputMatches, VerdictOutputNotMatch:
		if ch.ExpectPattern == "" {
			return fmt.Errorf("check %s: %s needs expect_pattern", ch.ID, ch.Verdict)
		}
		re, err := regexp.Compile(ch.ExpectPattern)
		if err != nil {
			return fmt.Errorf("check %s: expect_pattern: %w", ch.ID, err)
		}
		ch.compiled = re
	case VerdictCountMin:
		if ch.CountPattern == "" {
			return fmt.Errorf("check %s: count_min needs count_pattern", ch.ID)
		}
		re, err := regexp.Compile(ch.CountPattern)
		if err != nil {
			return fmt.Errorf("check %s: count_pattern: %w", ch.ID, err)
		}
		if re.NumSubexp() < 1 {
			return fmt.Errorf("check %s: count_pattern must capture one integer group", ch.ID)
		}
		ch.counter = re
		if ch.MinCount <= 0 {
			ch.MinCount = 1
		}
	case "":
		return fmt.Errorf("check %s: verdict rule is required — an unstated verdict channel is how a check comes to be read two different ways", ch.ID)
	default:
		return fmt.Errorf("check %s: unknown verdict rule %q", ch.ID, ch.Verdict)
	}
	return nil
}

// Pattern returns the compiled expectation, if any.
func (ch *Check) Pattern() *regexp.Regexp { return ch.compiled }

// Counter returns the compiled count pattern, if any.
func (ch *Check) Counter() *regexp.Regexp { return ch.counter }

// Timeout is the check's execution budget. A check that hangs is red, never
// pending: "I could not tell" must never read as clean.
func (ch *Check) Timeout() time.Duration {
	if ch.TimeoutSeconds > 0 {
		return time.Duration(ch.TimeoutSeconds) * time.Second
	}
	return 15 * time.Minute
}

// ChecksForEdge returns the checks required for a lifecycle edge, in declared
// order.
func (c *Config) ChecksForEdge(from, to string) []*Check {
	want := from + "->" + to
	var out []*Check
	for i := range c.Checks {
		for _, e := range c.Checks[i].RequiredFor {
			if e == want {
				out = append(out, &c.Checks[i])
				break
			}
		}
	}
	return out
}

// Check returns a declared check by id.
func (c *Config) Check(id string) *Check {
	for i := range c.Checks {
		if c.Checks[i].ID == id {
			return &c.Checks[i]
		}
	}
	return nil
}

// Worker returns a declared worker by type.
func (c *Config) Worker(t string) *WorkerDecl {
	for i := range c.Workers {
		if c.Workers[i].Type == t {
			return &c.Workers[i]
		}
	}
	return nil
}

// Path is where this config was loaded from.
func (c *Config) Path() string { return c.path }

// Owner resolves an area tag to its owning worker type.
func (c *Config) Owner(area string) (string, bool) {
	o, ok := c.Routing[area]
	return o, ok
}

// Capabilities. A worker declares which lifecycle edges it may propose across.
// Naming them as capabilities rather than as role names keeps the authority
// table independent of what any one project calls its agents.
const (
	// CapResearch turns a signed-off intent into a written approach.
	CapResearch = "research"
	// CapPlan decomposes an approach into work items with checkable criteria.
	CapPlan = "plan"
	// CapImplement builds one item, with its tests.
	CapImplement = "implement"
	// CapTest executes the tests. Separate from implement, because a lane that
	// both writes and runs its own tests will always find them acceptable.
	CapTest = "test"
	// CapJudge judges an item against its acceptance criteria.
	CapJudge = "judge"
	// CapValidate reviews adversarially, and validates a plan against the
	// intent it came from. The only capability that can reject.
	CapValidate = "validate"
	// CapCurate is the hygiene pass over what landed.
	CapCurate = "curate"
	// CapArbitrate judges a change against the system rather than against the
	// item — the only role that looks wider than one unit of work.
	CapArbitrate = "arbitrate"
	// CapOperate applies a change to real resources.
	CapOperate = "operate"
	// CapImprove records what was learned and raises self-improvements.
	CapImprove = "improve"
)

func knownCapability(c string) bool {
	switch c {
	case CapResearch, CapPlan, CapImplement, CapTest, CapJudge,
		CapValidate, CapCurate, CapArbitrate, CapOperate, CapImprove:
		return true
	}
	return false
}

// Can reports whether a worker type holds a capability. An unregistered type
// holds none: a proposal from a worker nobody declared is refused rather than
// waved through, because an unregistered worker is also one the never-run roll
// call cannot see.
func (c *Config) Can(workerType, capability string) bool {
	w := c.Worker(workerType)
	if w == nil {
		return false
	}
	for _, x := range w.Capabilities {
		if x == capability {
			return true
		}
	}
	return false
}

// WorkersWith lists every declared worker holding a capability.
func (c *Config) WorkersWith(capability string) []string {
	var out []string
	for _, w := range c.Workers {
		for _, x := range w.Capabilities {
			if x == capability {
				out = append(out, w.Type)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// FromChecks builds a minimal valid config programmatically.
//
// It exists so that a caller — a test, or a tool embedding this module —
// can exercise the check machinery without writing a file, and so that it
// still goes through the same validation a loaded config does. A test that
// constructs a struct the validator would have rejected proves nothing about
// the running system.
func FromChecks(project string, sourceRoots []string, checks []Check) (*Config, error) {
	c := &Config{
		Version: SchemaVersion, Project: project, SourceRoots: sourceRoots, Checks: checks,
		Workers: []WorkerDecl{
			{Type: "researcher", Layer: "planning", Capabilities: []string{CapResearch}},
			{Type: "planner", Layer: "planning", Capabilities: []string{CapPlan}},
			{Type: "performer", Layer: "worker", Capabilities: []string{CapImplement}},
			{Type: "tester", Layer: "verification", Capabilities: []string{CapTest}},
			{Type: "judge", Layer: "verification", Capabilities: []string{CapJudge}},
			{Type: "validator", Layer: "verification", Capabilities: []string{CapValidate}},
			{Type: "janitor", Layer: "stewardship", Capabilities: []string{CapCurate}},
			{Type: "arbiter", Layer: "stewardship", Capabilities: []string{CapArbitrate}},
			{Type: "improver", Layer: "stewardship", Capabilities: []string{CapImprove}},
			{Type: "systems", Layer: "platform", Capabilities: []string{CapOperate}, LowCadence: true},
		},
		Blast: BlastPolicy{AutoApplyMax: RadiusNone, NamedApproverMin: RadiusHost, TwoApprovalsMin: RadiusRegion},
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// LoopDecl is one scheduled dispatch lane.
//
// So both are built in here. Every loop dispatches through the same transition
// authority as everything else, and every firing writes a ledger tick, so a
// silent loop is a derived alarm rather than an absence nobody reads.
type LoopDecl struct {
	Comment string `json:"_comment,omitempty"`
	// Name is the loop's stable id. It is what the tick record keys on, so
	// renaming a loop starts its liveness history over.
	Name string `json:"name"`
	// Enabled is surfaced in the dashboard and is the intended way to park a
	// lane. A disabled loop still appears, because a lane nobody can see is a
	// lane nobody restarts.
	Enabled bool `json:"enabled"`
	// EverySeconds is the cadence.
	EverySeconds int `json:"every_seconds"`
	// OffsetSeconds staggers this loop against its siblings. Two loops firing on
	// the same second contend for leases and waste a pick each.
	OffsetSeconds int `json:"offset_seconds,omitempty"`
	// Capability restricts the loop to one kind of work: implement, verify,
	// validate, operate, generate. Empty means anything.
	Capability string `json:"capability,omitempty"`
	// Areas restricts the loop to items tagged with one of these. Empty means any
	// area.
	Areas []string `json:"areas,omitempty"`
	// Worker pins the loop to one declared worker type, overriding routing.
	Worker string `json:"worker,omitempty"`
	// MaxPerTick is how many dispatches one firing may perform.
	MaxPerTick int `json:"max_per_tick,omitempty"`
}

// Scope renders the loop's filter for the tick record and the dashboard.
func (l LoopDecl) Scope() string {
	parts := []string{}
	if l.Capability != "" {
		parts = append(parts, "cap="+l.Capability)
	}
	if l.Worker != "" {
		parts = append(parts, "worker="+l.Worker)
	}
	if len(l.Areas) > 0 {
		parts = append(parts, "areas="+strings.Join(l.Areas, "|"))
	}
	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, " ")
}

// ServerPolicy configures the dashboard.
type ServerPolicy struct {
	Comment string `json:"_comment,omitempty"`
	// Addr must be a loopback address.
	Addr string `json:"addr"`
	// RefreshSeconds drives the dashboard's own auto-refresh.
	RefreshSeconds int `json:"refresh_seconds,omitempty"`
}

// Loop returns a declared loop by name.
func (c *Config) Loop(name string) *LoopDecl {
	for i := range c.Loops {
		if c.Loops[i].Name == name {
			return &c.Loops[i]
		}
	}
	return nil
}

// OwnerFor resolves which worker should take a piece of work.
//
// Routing is consulted FIRST, because an area exists precisely to say who owns
// that kind of work. Falling straight through to "any worker with the right
// capability" is what made a specialist roster decorative in an earlier build
// of this dispatcher: a security reviewer and a general reviewer both hold
// validate, and without the area the picker just took whichever sorted first.
//
// The order is: the area's declared owner if it holds the capability; then a
// worker holding the capability that lists this area; then any worker holding
// the capability, by name. Nothing at all means the work is unreachable, which
// is a routing bug and is reported as one.
func (c *Config) OwnerFor(area, capability string) (string, bool) {
	if area != "" {
		if owner, ok := c.Routing[area]; ok && c.Can(owner, capability) {
			return owner, true
		}
	}
	candidates := c.WorkersWith(capability)
	if area != "" {
		for _, t := range candidates {
			w := c.Worker(t)
			for _, a := range w.Areas {
				if a == area {
					return t, true
				}
			}
		}
	}
	// Prefer a generalist — one that declares no areas — over an unrelated
	// specialist, so that an item in an area nobody claims does not get handed to
	// whichever specialist happens to sort first.
	for _, t := range candidates {
		if len(c.Worker(t).Areas) == 0 {
			return t, true
		}
	}
	if len(candidates) > 0 {
		return candidates[0], true
	}
	return "", false
}

// KnownArea reports whether an area has been declared by the project —
// either in the routing map or on a worker.
//
// Dispatch and generation ask different questions about an area, and they need
// different answers. Dispatch must never strand a live item, so OwnerFor falls
// back to a generalist: an item that already exists has to reach somebody.
// Generation is the opposite case — it is the one place an agent decides
// what work exists, so it may only file under a taxonomy the project has
// declared. Letting a generator invent an area is how a backlog fills with
// work nobody owns and nobody notices.
func (c *Config) KnownArea(area string) bool {
	if area == "" {
		return false
	}
	if _, ok := c.Routing[area]; ok {
		return true
	}
	for _, w := range c.Workers {
		for _, a := range w.Areas {
			if a == area {
				return true
			}
		}
	}
	return false
}

// Save writes the config back to the file it was loaded from.
//
// Only a narrow set of things is ever edited this way — a lane's cadence, a
// lane's enabled flag — and the reason for the narrowness is that this file
// is the fleet's policy. A surface that can rewrite arbitrary policy from a
// browser is a surface that can quietly widen what agents may do, and the
// whole design rests on that being reviewable. Everything else is edited in
// the file, in a commit, like the gated artefact it is.
func Save(c *Config) error {
	if c.path == "" {
		return fmt.Errorf("this config was not loaded from a file, so there is nowhere to save it")
	}
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	// Replace atomically, so a crash mid-write cannot leave the fleet with a
	// config it will refuse to start against.
	return os.Rename(tmp, c.path)
}

// SetLoop updates the two fields the dashboard may change, and persists.
func (c *Config) SetLoop(name string, enabled bool, everySeconds, maxPerTick int) error {
	l := c.Loop(name)
	if l == nil {
		return fmt.Errorf("no loop named %q is declared", name)
	}
	if everySeconds < 15 {
		return fmt.Errorf("a cadence of %ds would spend more time starting runs than doing them; 15s is the floor", everySeconds)
	}
	if maxPerTick < 1 {
		maxPerTick = 1
	}
	l.Enabled, l.EverySeconds, l.MaxPerTick = enabled, everySeconds, maxPerTick
	return Save(c)
}
