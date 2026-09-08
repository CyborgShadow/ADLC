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

// standardRoster is one role per capability, plus the generalists.
//
// Every capability in the lifecycle gets an owner here, because a capability
// nobody holds strands every item that reaches it — and a stranded item looks
// exactly like one nobody has got round to. Specialists are deliberately absent:
// they are worth adding when a project knows what its risks are, and inventing
// a security reviewer for a project that has not asked for one just produces a
// role that never runs.
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
		{"tester", "verification", "tester",
			"Executes the tests and reports what ran. A suite that matched nothing is a failure.",
			[]string{CapTest}, []string{"testing"}, false},
		{"judge", "verification", "judge",
			"Checks an item against its acceptance criteria by executing commands.",
			[]string{CapJudge}, nil, false},
		{"validator", "verification", "validator",
			"Adversarial review, and validation of a plan against the intent behind it. The only role that may reject.",
			[]string{CapValidate}, nil, false},
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
	order := []lane{
		{"improve", CapImprove, 900, 1},
		{"arbitrate", CapArbitrate, 300, 1},
		{"curate", CapCurate, 900, 1},
		{"review", CapValidate, 240, 2},
		{"judge", CapJudge, 180, 2},
		{"test", CapTest, 180, 2},
		{"build", CapImplement, 300, 1},
		{"apply", CapOperate, 600, 1},
		{"plan", CapPlan, 1800, 1},
		{"research", CapResearch, 1800, 1},
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
			MaxConcurrent:   1, MaxAttempts: 3, TimeoutSeconds: 3600,
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
