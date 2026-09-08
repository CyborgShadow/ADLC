package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scaffold builds a complete, valid config from a handful of answers.
//
// It exists because the alternative is telling people to hand-write JSON, and a
// hand-written config fails in one of two ways: it is refused at load with a
// message about a field, or it loads and then quietly does nothing because a
// capability has no role or a role has no lane. Neither is a good first hour
// with a tool.
//
// What has to be decided is small — what the project is, what already checks
// it, how far its changes reach. Everything else follows from those, and
// following from those deterministically is the whole point.
type ScaffoldOptions struct {
	Project     string
	SourceRoots []string
	Checks      []Check
	AgentsDir   string
	// AgentCommand is argv that starts an agent. Left empty the config still
	// validates and the fleet still refuses to dispatch, which is the honest
	// state for a project that has not chosen an agent yet.
	AgentCommand []string
	AutoApplyMax Radius
	// Console is off, propose, act or full.
	Console string
	Trunk   string
}

// scaffoldRole is one entry in the standard roster.
type scaffoldRole struct {
	Type, Layer, Prompt, Description string
	Capabilities                     []string
	Areas                            []string
	LowCadence                       bool
}

// standardRoster is one role per capability, the generalists, and the
// engineering disciplines.
//
// Every capability in the lifecycle gets an owner here, because a capability
// nobody holds strands every item that reaches it — and a stranded item looks
// exactly like one nobody has got round to.
//
// The disciplines are here for a different reason. A planner may only file work
// under an area the project has declared, so an area that is absent from the
// generated config is one the planner cannot name: the item is filed as core,
// the generalist takes it, and the roster is decorative. Declaring them up front
// makes the taxonomy available from the first item. Each one is a discipline
// that brings different judgement rather than a different file scope — the
// trap each is prone to is written into its prompt — and a discipline a project
// does not have is one entry to delete, which `adlc config check` will point at
// because a role with no runs reports as a red zero rather than an empty row.
//
// Risk reviewers are still deliberately absent. Security, performance and
// resilience specialists are worth adding once a project knows which of those
// is its risk; generating all three for a project that has not asked produces
// three roles that never run.
func standardRoster() []scaffoldRole {
	return []scaffoldRole{
		{"researcher", "planning", "researcher",
			"Turns an intent into a written approach: what exists, what the options are, which one and why.",
			[]string{CapResearch}, nil, false},
		{"planner", "planning", "planner",
			"Decomposes an approach into work items with checkable acceptance criteria.",
			[]string{CapPlan}, nil, false},
		{"engineer", "worker", "implementer",
			"Implements work items. Declares no areas, so it takes anything a specialist does not claim.",
			[]string{CapImplement}, nil, false},
		{"docs-writer", "worker", "implementer",
			"Implements documentation items.",
			[]string{CapImplement}, []string{"docs"}, false},
		{"frontend", "worker", "frontend",
			"Builds the surface a person looks at, including the states other than the populated one.",
			[]string{CapImplement}, []string{"frontend"}, false},
		{"backend", "worker", "backend",
			"Builds the service tier, for a request that arrives twice and concurrently with another.",
			[]string{CapImplement}, []string{"backend"}, false},
		{"api", "worker", "api",
			"Builds the published contract. Owns whether a change is additive or breaking.",
			[]string{CapImplement}, []string{"api"}, false},
		{"database", "worker", "database",
			"Owns the stored shape and the migrations that change it — the work git revert does not undo.",
			[]string{CapImplement}, []string{"database", "migration"}, false},
		{"query", "worker", "query",
			"Owns the access path: the statements issued, the plans they take, the transactions they run in.",
			[]string{CapImplement}, []string{"query"}, false},
		{"sre", "worker", "sre",
			"Writes the infrastructure, the deploy path and the signals. Applying is the operator's step.",
			[]string{CapImplement}, []string{"sre", "infrastructure"}, false},
		{"tester", "verification", "tester",
			"Executes the tests and reports what ran. A suite that matched nothing is a failure.",
			[]string{CapTest}, []string{"testing"}, false},
		{"judge", "verification", "judge",
			"Checks an item against its acceptance criteria by executing commands.",
			[]string{CapJudge}, nil, false},
		{"validator", "verification", "validator",
			"Adversarial review, and validation of a plan against the intent behind it. The only role that may reject.",
			[]string{CapValidate}, nil, false},
		{"architect", "verification", "architecture",
			"Reviews where a change puts things: boundaries, dependency direction, decisions that are expensive to undo.",
			[]string{CapValidate}, []string{"architecture"}, false},
		{"janitor", "stewardship", "janitor",
			"Hygiene only: stale docs, dead references, duplicated statements of one fact.",
			[]string{CapCurate}, []string{"hygiene"}, true},
		{"arbiter", "stewardship", "arbiter",
			"Judges a change against the system rather than against the item.",
			[]string{CapArbitrate}, nil, false},
		{"improver", "stewardship", "improver",
			"Records what a landed item taught and raises self-improvements as their own work.",
			[]string{CapImprove}, nil, true},
		{"operator", "platform", "systems",
			"The build environment, dependencies, CI — and the apply step, after an approval.",
			[]string{CapOperate}, []string{"platform", "ci"}, true},
		{"console", "platform", "console",
			"Answers an operator in the dashboard and drafts actions. Advances no work item on its own.",
			[]string{CapConverse}, nil, false},
	}
}

// standardLanes is one lane per stage, ordered so work nearest the finish line
// drains first and staggered so two never fire on the same second and contend
// for the same claim.
func standardLanes() []LoopDecl {
	type lane struct {
		name       string
		capability string
		every      int
		perTick    int
	}
	// Per tick: how many this lane will offer at once. Higher where a run is
	// short and cheap and the queue is where work piles up; lower where a run
	// is long, expensive, or touches something real. The fleet-wide ceiling
	// bounds the total regardless, so these are a shape rather than a sum.
	//
	// Cadences are short because a tick that finds nothing costs nothing. Only a
	// DISPATCH spends money, and perTick with the fleet ceiling is what bounds
	// that — so a long cadence buys no safety, it only delays. The entry lanes
	// were half an hour apart and a new project sat there looking broken until
	// somebody worked out that nothing was wrong except the wait.
	order := []lane{
		{"improve", CapImprove, 900, 1},
		{"arbitrate", CapArbitrate, 180, 2},
		{"curate", CapCurate, 900, 1},
		{"review", CapValidate, 120, 3},
		{"judge", CapJudge, 90, 3},
		{"test", CapTest, 90, 3},
		{"build", CapImplement, 120, 2},
		// Applies touch real machines one at a time, whatever the ceiling says.
		{"apply", CapOperate, 300, 1},
		{"plan", CapPlan, 120, 1},
		{"research", CapResearch, 120, 1},
	}
	out := make([]LoopDecl, 0, len(order))
	for i, l := range order {
		out = append(out, LoopDecl{
			Name: l.name, Enabled: true, EverySeconds: l.every,
			OffsetSeconds: i * 15, Capability: l.capability, MaxPerTick: l.perTick,
		})
	}
	return out
}

// Scaffold assembles the config and validates it before returning.
//
// A scaffold that can emit something the validator would refuse is worse than
// no scaffold: it moves the failure from "you wrote this wrong" to "the tool
// wrote this wrong", which is much harder to act on.
func Scaffold(o ScaffoldOptions) (*Config, error) {
	if strings.TrimSpace(o.Project) == "" {
		return nil, fmt.Errorf("a project name is required")
	}
	if len(o.Checks) == 0 {
		return nil, fmt.Errorf("at least one check is required: a gate with no checks reports green over nothing")
	}
	if o.AgentsDir == "" {
		o.AgentsDir = "agents"
	}
	if o.AutoApplyMax == "" {
		o.AutoApplyMax = RadiusNone
	}
	if !o.AutoApplyMax.Known() {
		return nil, fmt.Errorf(
			"auto_apply_max %q is not a radius this build knows (none, host, fleet, region, global)", o.AutoApplyMax)
	}
	if o.Trunk == "" {
		o.Trunk = "main"
	}

	c := &Config{
		Comment: fmt.Sprintf(
			"Generated by `adlc config init`. Every value here is editable; run `adlc config check` after changing one, because a config that loads and then quietly does nothing is the failure this tool is trying to avoid."),
		Version:     SchemaVersion,
		Project:     o.Project,
		SourceRoots: o.SourceRoots,
		Checks:      o.Checks,
		Routing:     map[string]string{},
		Blast: BlastPolicy{
			AutoApplyMax: o.AutoApplyMax, NamedApproverMin: RadiusHost,
			TwoApprovalsMin: RadiusRegion, ApprovalTTLMinutes: 24 * 60,
		},
		Budget: Budget{
			Comment:            PricingComment,
			PriceMicrosPerMTok: DefaultPricing(),
			DefaultModel:       "claude-opus-5",
		},
		Lease: LeasePolicy{Dir: filepath.Join(".adlc", "leases"), TTLMinutes: 180},
		Dispatch: DispatchPolicy{
			Command:         o.AgentCommand,
			WorkDirTemplate: filepath.ToSlash(filepath.Join(".adlc", "workspaces", "{{run_id}}")),
			Trunk:           o.Trunk,
			MaxConcurrent:   DefaultMaxConcurrent, MaxAttempts: 3, TimeoutSeconds: 3600,
			Isolation: "worktree",
		},
		Prompts: PromptPolicy{
			Dir:          o.AgentsDir,
			PreambleFile: filepath.ToSlash(filepath.Join(o.AgentsDir, "_preamble.md")),
			MandatoryClauses: []string{
				"You never write the ledger.",
				"You never weaken, skip or delete a check to make a gate pass.",
				"You never mark your own work done.",
				"If you could not run something, say so.",
			},
		},
		Loops:  standardLanes(),
		Server: ServerPolicy{Addr: "127.0.0.1:8099", RefreshSeconds: 15},
	}

	for _, r := range standardRoster() {
		c.Workers = append(c.Workers, WorkerDecl{
			Type: r.Type, Layer: r.Layer, Prompt: r.Prompt, Description: r.Description,
			Capabilities: r.Capabilities, Areas: r.Areas, LowCadence: r.LowCadence,
		})
		// An area is routed to the role that declared it. A role without an area
		// takes whatever no specialist claims, and an area without a role is
		// refused by the validator — so both halves are written together here
		// rather than left for somebody to remember.
		for _, area := range r.Areas {
			c.Routing[area] = r.Type
		}
	}
	// The generalist owns the areas every project turns out to need.
	for _, area := range []string{"core", "cli"} {
		c.Routing[area] = "engineer"
	}

	switch strings.ToLower(strings.TrimSpace(o.Console)) {
	case "", "off", "false", "no":
		c.Console = ConsolePolicy{Enabled: false, Authority: ConsolePropose}
	case string(ConsolePropose), string(ConsoleAct), string(ConsoleFull):
		c.Console = ConsolePolicy{
			Comment:   "authority is propose | act | full. See `adlc config check` for what this project's level means.",
			Enabled:   true,
			Authority: ConsoleAuthority(strings.ToLower(strings.TrimSpace(o.Console))),
			Worker:    "console", TimeoutSeconds: 180, HistoryTurns: 20,
		}
	default:
		return nil, fmt.Errorf(
			"console %q is not off, propose, act or full", o.Console)
	}

	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("the scaffold produced a config this build refuses, which is a defect: %w", err)
	}
	return c, nil
}

// SaveTo writes a config to a path it was not loaded from.
func SaveTo(c *Config, path string) error {
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// VerdictRules lists the verdict rules this build knows, for an error message
// that can name the alternatives rather than just refusing.
func VerdictRules() []string {
	return []string{
		string(VerdictExitZero), string(VerdictExitIn), string(VerdictOutputEmpty),
		string(VerdictOutputNonEmpty), string(VerdictOutputMatches),
		string(VerdictOutputNotMatch), string(VerdictGoTestJSON), string(VerdictCountMin),
	}
}

// Known reports whether a verdict rule is one this build understands.
func (r VerdictRule) Known() bool {
	for _, x := range VerdictRules() {
		if x == string(r) {
			return true
		}
	}
	return false
}
