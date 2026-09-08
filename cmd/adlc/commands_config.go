package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// The config commands exist so that nothing has to hand-assemble JSON and hope.
//
// A skill or a person answers questions; `config init` turns the answers into a
// declaration and puts it through the same validator a running fleet uses;
// `config check` says, in words, what is wrong with one. The interview is the
// part that needs judgement. Everything after it is deterministic, and
// deterministic things belong in the tool.

func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: adlc config init|check|show")
		return exitUsage
	}
	switch args[0] {
	case "init":
		return cmdConfigInit(args[1:])
	case "check":
		return cmdConfigCheck(args[1:])
	case "show":
		return cmdConfigShow(args[1:])
	}
	fmt.Fprintf(os.Stderr, "adlc config: unknown subcommand %q\n", args[0])
	return exitUsage
}

// ---------------------------------------------------------------- check

func cmdConfigCheck(args []string) int {
	fs := sub("config check")
	quiet := fs.Bool("quiet", false, "print nothing on success; the exit code is the answer")
	if fs.Parse(args) != nil {
		return exitUsage
	}
	cfg, err := config.Load(flagConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s is not usable:\n\n  %v\n\n", flagConfig, err)
		fmt.Fprintln(os.Stderr, "Nothing will start against it. Fix that and run this again.")
		return exitUsage
	}
	if *quiet {
		return exitOK
	}
	fmt.Printf("%s is valid.\n\n", flagConfig)
	fmt.Printf("  project        %s\n", cfg.Project)
	fmt.Printf("  source roots   %s\n", strings.Join(cfg.SourceRoots, ", "))
	fmt.Printf("  checks         %d\n", len(cfg.Checks))
	fmt.Printf("  roles          %d\n", len(cfg.Workers))
	fmt.Printf("  areas routed   %d\n", len(cfg.Routing))
	fmt.Printf("  lanes          %d\n", len(cfg.LoopList()))
	fmt.Printf("  auto-apply max %s (anything above stops for a named person)\n", cfg.Blast.AutoApplyMax)
	if cfg.Console.Enabled {
		fmt.Printf("  console        on, authority %s — it %s\n",
			cfg.Console.Authority, cfg.Console.Authority.Describe())
	} else {
		fmt.Printf("  console        off\n")
	}

	// Things that are legal but will disappoint. The validator refuses what
	// cannot work; this reports what will work and then sit there.
	var warn []string
	for _, capability := range []string{
		config.CapResearch, config.CapPlan, config.CapImplement, config.CapTest,
		config.CapJudge, config.CapValidate, config.CapCurate, config.CapArbitrate,
		config.CapOperate, config.CapImprove,
	} {
		if len(cfg.WorkersWith(capability)) == 0 {
			warn = append(warn, fmt.Sprintf(
				"no role holds %q, so every item that reaches that stage stops there", capability))
		}
	}
	drained := map[string]bool{}
	for _, l := range cfg.LoopList() {
		if l.Enabled && l.Capability != "" {
			drained[l.Capability] = true
		}
	}
	for _, w := range cfg.Workers {
		for _, c := range w.Capabilities {
			if c == config.CapConverse {
				continue
			}
			if !drained[c] {
				warn = append(warn, fmt.Sprintf(
					"%q work has a role (%s) but no enabled lane, so nothing will ever dispatch it", c, w.Type))
			}
		}
	}
	if len(warn) > 0 {
		fmt.Println("\nThis config is valid, and these will still leave work sitting:")
		for _, w := range dedupe(warn) {
			fmt.Printf("  - %s\n", w)
		}
	}
	return exitOK
}

func dedupe(v []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range v {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------- show

func cmdConfigShow(args []string) int {
	fs := sub("config show")
	if fs.Parse(args) != nil {
		return exitUsage
	}
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return fail("%v", err)
	}
	// The EFFECTIVE config, with defaults filled in. Reading the file tells you
	// what was written; this tells you what is running, and the gap between
	// them is where a surprising afternoon comes from.
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(string(b))
	return exitOK
}

// ---------------------------------------------------------------- init

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func cmdConfigInit(args []string) int {
	fs := sub("config init")
	project := fs.String("project", "", "what this project is called")
	var roots stringList
	fs.Var(&roots, "source-root", "a directory the fleet's guards are scoped to (repeatable)")
	var checks stringList
	fs.Var(&checks, "check", "a check as id:verdict:command (repeatable), e.g. test:go_test_json:go test -json ./...")
	agentsDir := fs.String("agents", "agents", "where the role prompts live")
	command := fs.String("agent-command", "", "argv that starts an agent, with {{prompt}} and {{envelope}}")
	radius := fs.String("auto-apply-max", "none", "the widest blast radius applied without a person: none|host|fleet|region|global")
	consoleAuth := fs.String("console", "off", "operator console: off|propose|act|full")
	trunk := fs.String("trunk", "main", "the branch the merge queue lands on")
	force := fs.Bool("force", false, "overwrite an existing config")
	if fs.Parse(args) != nil {
		return exitUsage
	}
	if strings.TrimSpace(*project) == "" {
		return fail("-project is required: it names the project in every report and in the console's own session")
	}
	if len(roots) == 0 {
		return fail("at least one -source-root is required: every tree-walking guard is scoped by it, and an unscoped guard refuses work on files nobody edited")
	}
	if len(checks) == 0 {
		return fail("at least one -check is required: a gate with no checks reports green over nothing, which is the most expensive way for this system to be wrong")
	}
	if _, err := os.Stat(flagConfig); err == nil && !*force {
		return fail("%s already exists. Pass -force to overwrite it, or point -config somewhere else", flagConfig)
	}

	parsed := make([]config.Check, 0, len(checks))
	for _, spec := range checks {
		ch, err := parseCheckSpec(spec)
		if err != nil {
			return fail("%v", err)
		}
		parsed = append(parsed, ch)
	}

	cfg, err := config.Scaffold(config.ScaffoldOptions{
		Project:      *project,
		SourceRoots:  roots,
		Checks:       parsed,
		AgentsDir:    *agentsDir,
		AgentCommand: strings.Fields(*command),
		AutoApplyMax: config.Radius(*radius),
		Console:      *consoleAuth,
		Trunk:        *trunk,
	})
	if err != nil {
		return fail("%v", err)
	}
	if err := config.SaveTo(cfg, flagConfig); err != nil {
		return fail("%v", err)
	}
	// Loaded back rather than trusted, so what is reported is what a running
	// fleet would actually read.
	back, err := config.Load(flagConfig)
	if err != nil {
		return fail("the config was written but does not load, which is a defect in this command: %v", err)
	}
	fmt.Printf("wrote %s\n", flagConfig)
	fmt.Printf("  project        %s\n", back.Project)
	fmt.Printf("  checks         %d\n", len(back.Checks))
	fmt.Printf("  roles          %d across %d areas\n", len(back.Workers), len(back.Routing))
	fmt.Printf("  lanes          %d\n", len(back.LoopList()))
	fmt.Printf("  auto-apply max %s\n", back.Blast.AutoApplyMax)
	if back.Console.Enabled {
		fmt.Printf("  console        on, authority %s\n", back.Console.Authority)
	}
	fmt.Println()
	fmt.Printf("Next: put the role prompts in %s/ (`adlc prompt list` says which are missing),\n", back.Prompts.Dir)
	fmt.Println("then `adlc init` to create the ledger.")
	return exitOK
}

// parseCheckSpec reads id:verdict:command.
//
// The command keeps its colons — a Windows path or a URL has them and splitting
// on every one would mangle exactly the commands people are most likely to
// declare.
func parseCheckSpec(spec string) (config.Check, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) != 3 {
		return config.Check{}, fmt.Errorf(
			"check %q is not id:verdict:command — e.g. test:go_test_json:go test -json ./...", spec)
	}
	id, verdict, command := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	if id == "" || command == "" {
		return config.Check{}, fmt.Errorf("check %q needs an id and a command", spec)
	}
	rule := config.VerdictRule(verdict)
	if !rule.Known() {
		return config.Check{}, fmt.Errorf(
			"check %s: %q is not a verdict rule this build knows. The rules are: %s",
			id, verdict, strings.Join(config.VerdictRules(), ", "))
	}
	return config.Check{
		ID: id, Kind: config.KindSource, Command: strings.Fields(command), Verdict: rule,
		// Gating the edges work actually crosses. A check declared against no
		// edge runs nowhere, which is the same as not declaring it.
		RequiredFor: []string{
			"in_progress->ready_for_testing",
			"testing->ready_for_review",
			"judging->ready_for_validation",
			"merging->merged",
		},
	}, nil
}
