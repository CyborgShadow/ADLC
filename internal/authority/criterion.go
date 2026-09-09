package authority

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// Acceptance criteria the control plane can run itself.
//
// This is the one place the project took an agent's word for a verification.
// Everywhere else the rule holds: the gate executes the declared commands in
// the run's own tree, and an envelope is a declaration rather than evidence.
// Acceptance criteria were prose — "run this and you will see that" — so the
// only way to know whether an item met its specification was to dispatch a
// judge, hand it the prose, and believe what it reported. A full agent run,
// ten to fifteen minutes, to execute commands that were already written down.
//
// A criterion may now carry its own check. When it does, the control plane runs
// it the moment the work is ready: no dispatch, no lane wait, no claim to
// compare, and the result is an observation rather than a report of one. When
// it does not — because what it asserts genuinely needs a person's or an
// agent's judgement — a judge is dispatched for exactly those, and says so.
//
// The syntax is deliberately the same vocabulary the gate already uses, so
// there is one definition of what "passing" means and not two:
//
//	AC-1 [exit_zero] go run ./cmd/adlc config check
//	AC-2 [output_matches: DIRTY] go run ./cmd/adlc gate run -workdir .
//	AC-3 the page is readable by a five-year-old        <- no check; needs judgement
//
// Prose without a bracket is not a defect. Some things are not mechanically
// checkable and pretending otherwise would be worse than admitting it.

// Criterion is one acceptance criterion, as the control plane reads it.
type Criterion struct {
	// Text is the criterion exactly as written, always.
	Text string
	// Rule and Command are set when this criterion can be executed.
	Rule    config.VerdictRule
	Command []string
	// Want is the argument some rules take: the string to match, the minimum
	// count. Empty for rules that need none.
	Want string
}

// Executable reports whether the control plane can settle this criterion
// itself, without asking anybody.
func (c Criterion) Executable() bool { return len(c.Command) > 0 && c.Rule != "" }

// criterionForm matches a leading [rule] or [rule: argument] followed by a
// command. Anchored at the start so prose that merely mentions a bracket is not
// mistaken for a check.
var criterionForm = regexp.MustCompile(`^\s*(?:AC-\d+\s+)?\[([a-z_]+)(?::\s*([^\]]*))?\]\s*(.+)$`)

// criterionShape is criterionForm with the command made optional, and it exists
// only for criterionFault. The parser must not accept a bracket with no command
// — there is nothing to run — but the admission gate has to SEE one, because
// "AC-1 [exit_zero]" is a planner that believed it wrote a check. Parsed as
// prose it would be filed silently and settled by a judge reading a rule name.
var criterionShape = regexp.MustCompile(`^\s*(?:AC-\d+\s+)?\[([a-z_]+)(?::\s*([^\]]*))?\]\s*(.*)$`)

// ParseCriterion reads one criterion. A criterion this build cannot execute
// comes back as prose, never as an error: an unrecognised rule must not turn a
// criterion into nothing, because a criterion nobody checks and a criterion
// nobody knows about look identical afterwards.
func ParseCriterion(text string) Criterion {
	c := Criterion{Text: strings.TrimSpace(text)}
	m := criterionForm.FindStringSubmatch(c.Text)
	if m == nil {
		return c
	}
	rule := config.VerdictRule(m[1])
	if !knownRule(rule) {
		return c
	}
	cmd := strings.Fields(m[3])
	if len(cmd) == 0 {
		return c
	}
	c.Rule, c.Want, c.Command = rule, strings.TrimSpace(m[2]), cmd
	return c
}

// ParseCriteria reads a whole item's criteria.
func ParseCriteria(texts []string) []Criterion {
	out := make([]Criterion, 0, len(texts))
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			continue
		}
		out = append(out, ParseCriterion(t))
	}
	return out
}

// NeedsJudgement returns the criteria no command can settle. An item whose
// criteria are all executable needs no judge at all; one with any prose
// criterion needs a judge for those, and only those.
func NeedsJudgement(cs []Criterion) []Criterion {
	var out []Criterion
	for _, c := range cs {
		if !c.Executable() {
			out = append(out, c)
		}
	}
	return out
}

// criterionRules are the verdict rules a criterion can actually carry.
//
// Two of the gate's eight are missing on purpose. exit_in needs allowed_exits
// and count_min needs count_pattern and min_count, and the criterion syntax has
// one argument slot, which dispatch fills as the expect_pattern. A criterion
// naming either of those describes a check that cannot be assembled at all —
// which is an invocation fault, not a failing assertion, and is refused as one.
func criterionRules() []config.VerdictRule {
	return []config.VerdictRule{
		config.VerdictExitZero, config.VerdictOutputEmpty, config.VerdictOutputNonEmpty,
		config.VerdictOutputMatches, config.VerdictOutputNotMatch, config.VerdictGoTestJSON,
	}
}

// ruleList renders criterionRules for a refusal message.
func ruleList() string {
	out := make([]string, 0, 6)
	for _, r := range criterionRules() {
		out = append(out, string(r))
	}
	return strings.Join(out, ", ")
}

// shellOnly are the characters that mean something to a shell and nothing to
// the executor. Criteria run through exec directly — argv[0] with argv[1:] —
// exactly as the declared checks do, so a pipe or an && is a literal argument
// handed to the program rather than syntax.
const shellOnly = "|&;<>`"

// criterionFault names why a criterion that was WRITTEN as a check cannot be
// run as one, or returns "" when it can be — or when it is honest prose.
//
// The distinction it draws is the one cmd/sitecheck states in its exit codes: 1
// is a rule that fired, 2 is an invocation that was wrong, and a caller that
// cannot tell them apart treats its own typo as a failing site and fixes the
// site. Here the same split decides what may be admitted. A criterion whose
// command runs and reports a failing assertion is EXPECTED at admission — the
// work does not exist yet, so of course it fails — and says something true
// about the item from the moment it is filed. A criterion that cannot be
// started reports nothing, ever, and its silence looks identical to work that
// is merely not done.
func criterionFault(text string) string {
	m := criterionShape.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "" // prose, and prose is allowed
	}
	rule, want, cmd := config.VerdictRule(m[1]), strings.TrimSpace(m[2]), strings.Fields(m[3])
	allowed := false
	for _, r := range criterionRules() {
		if r == rule {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Sprintf("names verdict rule %q, which a criterion cannot carry; the rules are %s", rule, ruleList())
	}
	if len(cmd) == 0 {
		return fmt.Sprintf("names the rule %s and then no command to read it from", rule)
	}
	if (rule == config.VerdictOutputMatches || rule == config.VerdictOutputNotMatch) && want == "" {
		return fmt.Sprintf("names %s and no pattern; write it as [%s: PATTERN]", rule, rule)
	}
	for _, arg := range cmd {
		if i := strings.IndexAny(arg, shellOnly); i >= 0 {
			return fmt.Sprintf("carries %q, which is shell syntax; the command is executed directly, so it would be passed to %s as a literal argument",
				string(arg[i]), cmd[0])
		}
		if strings.Contains(arg, "$(") {
			return fmt.Sprintf("carries a $( ) substitution, which is shell syntax; the command is executed directly, so it would reach %s as literal text", cmd[0])
		}
	}
	if cmd[0] == "cd" || cmd[0] == "export" || cmd[0] == "source" {
		return fmt.Sprintf("starts with the shell builtin %q, which is not a program that can be executed; a criterion runs in the item's own tree already", cmd[0])
	}
	return ""
}

// knownRule reads criterionRules and nothing else. Two lists of the rules a
// criterion may carry would drift, and the drift is silent in the worst
// direction: the admission gate would accept a criterion that the executor then
// could not assemble, so the item would be filed with a check that never runs
// and never says why.
func knownRule(r config.VerdictRule) bool {
	for _, k := range criterionRules() {
		if k == r {
			return true
		}
	}
	return false
}
