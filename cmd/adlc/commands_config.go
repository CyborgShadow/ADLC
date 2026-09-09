package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/prompt"
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
	// A check bound to an edge that does not exist never runs, so the edge it
	// was meant to gate refuses every proposal for having no checks — while
	// this command reports the config valid. Refused here, at the point where
	// somebody is asking whether the config is usable.
	if eerr := authority.CheckEdgesExist(cfg); eerr != nil {
		fmt.Fprintf(os.Stderr, "%s is not usable:\n\n  %v\n\n", flagConfig, eerr)
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
			// A capability the lifecycle never asks for cannot leave work
			// sitting, because no item ever reaches a state that wants it. Only
			// a capability some state DOES dispatch, with no lane to dispatch
			// it, strands anything.
			//
			// Warning on both read the same and meant opposite things: one is a
			// misconfiguration that stops a fleet, the other is a role kept on
			// the roster after the lifecycle moved past it. A warning that fires
			// on a deliberate choice every single run is one people learn to
			// scroll past, and then it costs the one time it was real.
			if !drained[c] && authority.DispatchesCapability(c) {
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
	reportAbsentPrompts(cfg)
	return exitOK
}

// reportAbsentPrompts names the prompt files the config's roles resolve through
// that are not on disk.
//
// It lives here rather than in the validator on purpose. config.validate() is
// the declaration's own check and reads no files: prompts.dir is relative to a
// working directory the config knows nothing about, so a filesystem test inside
// Load would refuse the config anywhere it is read from elsewhere — and would
// refuse `config init`'s own output, which is written before the prompts exist
// because writing them is the next step it tells you to take. So the question
// is asked by the commands that are already standing in the project: reported
// here, and refused by `adlc prompt check`, which is what CI runs.
func reportAbsentPrompts(cfg *config.Config) {
	inv := prompt.Survey(cfg.Prompts, cfg.Workers)
	if inv.Missing == 0 {
		return
	}
	fmt.Println("\nThese are declared and not on disk. A role whose prompt is absent cannot be dispatched:")
	if inv.DirErr != "" {
		fmt.Printf("  - %s is not there yet; `adlc prompt list` names every prompt these roles want\n", inv.Dir)
		return
	}
	for _, p := range inv.Prompts {
		if p.Missing() {
			fmt.Printf("  - no prompt %q in %s, named by %s\n", p.ID, inv.Dir, strings.Join(p.Roles, ", "))
		}
	}
	if inv.PreambleFile != "" && !inv.PreamblePresent {
		fmt.Printf("  - the shared preamble %s is not there, so no role can be assembled\n", inv.PreambleFile)
	}
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
	fs.Var(&checks, "check", checkFlagHelp)
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

// checkRuleParams names the parameters each verdict rule reads.
//
// One table, so the flag's help, the message that names a parameter somebody
// left out, and the parser that fills the fields cannot drift apart. A rule
// this build knows but that is absent here is refused rather than half-built:
// handing the scaffold a check it will reject is what made a missing
// count_pattern read as a defect in the tool.
var checkRuleParams = map[config.VerdictRule]struct{ required, optional []string }{
	config.VerdictExitZero:       {},
	config.VerdictExitIn:         {required: []string{"allowed_exits"}},
	config.VerdictOutputEmpty:    {},
	config.VerdictOutputNonEmpty: {},
	config.VerdictOutputMatches:  {required: []string{"expect_pattern"}},
	config.VerdictOutputNotMatch: {required: []string{"expect_pattern"}},
	config.VerdictGoTestJSON:     {},
	config.VerdictCountMin:       {required: []string{"count_pattern"}, optional: []string{"min_count"}},
}

// checkFlagHelp is the -check flag's own description. It carries an example of
// the parameterised form because the rule that needs it most — count_min over a
// test runner's own count — is the one a project cannot express any other way
// through this command.
const checkFlagHelp = `a check as id:verdict:command (repeatable), e.g. test:go_test_json:go test -json ./...
a rule that reads parameters takes them in brackets after its name:
  tests:count_min[count_pattern="numTotalTests":(\d+)]:npm test
  plan:exit_in[allowed_exits=0,2]:terraform plan -detailed-exitcode
  lint:output_matches[expect_pattern=(?m)^0 problems]:npx eslint .
two parameters are separated by a semicolon: count_min[count_pattern=…;min_count=20]`

// parseCheckSpec reads id:verdict:command, where the verdict may carry the
// parameters its rule reads as verdict[key=value;key=value].
//
// The command keeps its colons — a Windows path or a URL has them and splitting
// on every one would mangle exactly the commands people are most likely to
// declare. The bracketed field is read the same way, by scanning to the bracket
// that closes it rather than to the first colon inside it, because the pattern
// that counts a JavaScript suite's tests ("numTotalTests":(\d+)) contains one.
//
// Nothing half-built is returned. The check is put through the same compile the
// loader uses, so a parameter a rule needs and has not got is named here, next
// to the spec that was typed.
func parseCheckSpec(spec string) (config.Check, error) {
	id, rest, ok := strings.Cut(spec, ":")
	if !ok {
		return config.Check{}, checkShapeError(spec)
	}
	id = strings.TrimSpace(id)
	field, command, err := cutVerdictField(rest)
	if err != nil {
		return config.Check{}, fmt.Errorf("check %q: %w", spec, err)
	}
	command = strings.TrimSpace(command)
	if id == "" || command == "" {
		return config.Check{}, fmt.Errorf("check %q needs an id and a command", spec)
	}

	name, params, err := splitVerdictField(field)
	if err != nil {
		return config.Check{}, fmt.Errorf("check %s: %w", id, err)
	}
	rule := config.VerdictRule(name)
	if !rule.Known() {
		return config.Check{}, fmt.Errorf(
			"check %s: %q is not a verdict rule this build knows. The rules are: %s",
			id, name, strings.Join(config.VerdictRules(), ", "))
	}
	takes, declared := checkRuleParams[rule]
	if !declared {
		return config.Check{}, fmt.Errorf(
			"check %s: this command cannot declare %q yet, so it will not write a config that only half-declares it. Add the check to %s by hand and run `adlc config check`",
			id, name, flagConfig)
	}

	ch := config.Check{
		ID: id, Kind: config.KindSource, Command: strings.Fields(command), Verdict: rule,
		// Gating the edges work actually crosses. A check declared against no
		// edge runs nowhere, which is the same as not declaring it — and a
		// check declared against an edge that does not EXIST is worse, because
		// the edge it was meant to gate then reports zero checks and refuses
		// every proposal.
		//
		// These are read from the transition table rather than written out, so
		// renaming a state cannot leave the generator emitting a config that
		// `adlc config check` refuses. It did: every new project was dead on
		// arrival at the first command, and the generator was the last place
		// anybody thought to look.
		RequiredFor: authority.GatedEdges(),
	}
	for _, p := range params {
		if !allowedParam(takes.required, takes.optional, p.key) {
			return config.Check{}, fmt.Errorf(
				"check %s: %s does not read %q. %s", id, rule, p.key, readsWhat(rule))
		}
		if err := applyCheckParam(&ch, p.key, p.value); err != nil {
			return config.Check{}, fmt.Errorf("check %s: %w", id, err)
		}
	}
	// The loader's own compile, not a second opinion of it. Two definitions of
	// what a rule needs is how a spec came to be accepted here and refused three
	// lines later.
	if err := ch.Compile(); err != nil {
		if missing := firstMissing(&ch, takes.required); missing != "" {
			return config.Check{}, fmt.Errorf(
				"%w. Declare it after the rule: %s:%s[%s=…]:%s", err, id, rule, missing, command)
		}
		return config.Check{}, err
	}
	return ch, nil
}

func checkShapeError(spec string) error {
	return fmt.Errorf(
		"check %q is not id:verdict:command — e.g. test:go_test_json:go test -json ./...", spec)
}

// cutVerdictField splits the verdict field from the command.
//
// The field ends at its closing bracket when it has one and at the next colon
// when it does not, which is what lets a parameter hold a colon and a command
// hold one too.
func cutVerdictField(rest string) (field, command string, err error) {
	colon := strings.IndexByte(rest, ':')
	open := strings.IndexByte(rest, '[')
	if open < 0 || (colon >= 0 && colon < open) {
		if colon < 0 {
			return "", "", fmt.Errorf("there is no command after the verdict rule")
		}
		return strings.TrimSpace(rest[:colon]), rest[colon+1:], nil
	}
	end := matchBracket(rest, open)
	if end < 0 {
		return "", "", fmt.Errorf("the [ after the verdict rule is never closed")
	}
	after := rest[end+1:]
	if !strings.HasPrefix(after, ":") {
		return "", "", fmt.Errorf("the command must follow the verdict rule's ] after a colon")
	}
	return strings.TrimSpace(rest[:end+1]), after[1:], nil
}

// matchBracket returns the index of the ] that closes the [ at open, counting
// nesting so that a character class inside a pattern does not end the field.
func matchBracket(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

type checkParam struct{ key, value string }

// splitVerdictField reads rule and rule[key=value;key=value].
//
// Semicolon separates parameters rather than comma because a list of allowed
// exit codes is written with commas and a repetition count inside a pattern
// often is too.
func splitVerdictField(field string) (name string, params []checkParam, err error) {
	open := strings.IndexByte(field, '[')
	if open < 0 {
		return field, nil, nil
	}
	name = strings.TrimSpace(field[:open])
	body := field[open+1 : len(field)-1]
	if strings.TrimSpace(body) == "" {
		return name, nil, fmt.Errorf("%s[] declares no parameter. %s", name, readsWhat(config.VerdictRule(name)))
	}
	for _, part := range splitTop(body, ';') {
		if strings.TrimSpace(part) == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return "", nil, fmt.Errorf("parameter %q is not key=value", strings.TrimSpace(part))
		}
		params = append(params, checkParam{key: strings.TrimSpace(k), value: strings.TrimSpace(v)})
	}
	return name, params, nil
}

// splitTop splits on sep only outside brackets, so a pattern that contains one
// stays whole.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func applyCheckParam(ch *config.Check, key, value string) error {
	switch key {
	case "expect_pattern":
		ch.ExpectPattern = value
	case "count_pattern":
		ch.CountPattern = value
	case "min_count":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("min_count %q is not a positive whole number — it is how many units the run must have found to have run at all", value)
		}
		ch.MinCount = n
	case "allowed_exits":
		for _, f := range strings.Split(value, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			n, err := strconv.Atoi(f)
			if err != nil {
				return fmt.Errorf("allowed_exits %q is not a comma-separated list of exit codes", value)
			}
			ch.AllowedExits = append(ch.AllowedExits, n)
		}
		if len(ch.AllowedExits) == 0 {
			return fmt.Errorf("allowed_exits is empty, so no exit code would count as a pass")
		}
	default:
		return fmt.Errorf("%q is not a check parameter this build knows", key)
	}
	return nil
}

func allowedParam(required, optional []string, key string) bool {
	for _, k := range append(append([]string{}, required...), optional...) {
		if k == key {
			return true
		}
	}
	return false
}

// readsWhat says which parameters a rule reads, for the message somebody gets
// when they name one it does not.
func readsWhat(rule config.VerdictRule) string {
	takes, ok := checkRuleParams[rule]
	if !ok {
		return ""
	}
	all := append(append([]string{}, takes.required...), takes.optional...)
	if len(all) == 0 {
		return fmt.Sprintf("%s reads no parameters", rule)
	}
	return fmt.Sprintf("%s reads %s", rule, strings.Join(all, " and "))
}

// firstMissing names the required parameter that is still unset, so the error
// points at the thing to type rather than at the rule.
func firstMissing(ch *config.Check, required []string) string {
	for _, k := range required {
		switch k {
		case "expect_pattern":
			if ch.ExpectPattern == "" {
				return k
			}
		case "count_pattern":
			if ch.CountPattern == "" {
				return k
			}
		case "allowed_exits":
			if len(ch.AllowedExits) == 0 {
				return k
			}
		}
	}
	return ""
}
