package main

import (
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// The -check spec is the whole setup path for a project's gate. What it cannot
// express is what a project cannot declare without hand-writing JSON, so these
// tests pin that every verdict rule this build knows can be reached from here,
// and that a spec it cannot fully build is refused at the point it was typed.

func parse(t *testing.T, spec string) config.Check {
	t.Helper()
	ch, err := parseCheckSpec(spec)
	if err != nil {
		t.Fatalf("parse %q: %v", spec, err)
	}
	return ch
}

func TestTheThreeFieldFormIsUnchanged(t *testing.T) {
	ch := parse(t, "test:go_test_json:go test -json ./...")
	if ch.ID != "test" {
		t.Errorf("id: %q", ch.ID)
	}
	if ch.Verdict != config.VerdictGoTestJSON {
		t.Errorf("verdict: %q", ch.Verdict)
	}
	if strings.Join(ch.Command, " ") != "go test -json ./..." {
		t.Errorf("command: %q", ch.Command)
	}
	if ch.Kind != config.KindSource {
		t.Errorf("kind: %q", ch.Kind)
	}
	if len(ch.RequiredFor) == 0 {
		t.Error("a check declared against no edge runs nowhere")
	}
}

// TestACommandKeepsItsColons is the reason the spec is not split on every
// colon: a Windows path and a URL both have one.
func TestACommandKeepsItsColons(t *testing.T) {
	ch := parse(t, `smoke:exit_zero:curl -fsS http://127.0.0.1:8099/healthz`)
	if got := strings.Join(ch.Command, " "); got != "curl -fsS http://127.0.0.1:8099/healthz" {
		t.Fatalf("the command was mangled: %q", got)
	}
	if ch.Verdict != config.VerdictExitZero {
		t.Fatalf("verdict: %q", ch.Verdict)
	}
}

// TestEveryParameterisedRuleCanBeDeclared is the defect this syntax exists for.
// count_min over a JavaScript runner's own test count was the check the setup
// path could not generate at all.
func TestEveryParameterisedRuleCanBeDeclared(t *testing.T) {
	t.Run("count_min", func(t *testing.T) {
		ch := parse(t, `tests:count_min[count_pattern="numTotalTests":(\d+)]:npm test`)
		if ch.CountPattern != `"numTotalTests":(\d+)` {
			t.Fatalf("count_pattern: %q", ch.CountPattern)
		}
		if strings.Join(ch.Command, " ") != "npm test" {
			t.Fatalf("command: %q", ch.Command)
		}
		if ch.MinCount != 1 {
			t.Errorf("an unstated minimum is one: zero discovered units must not read as a pass, got %d", ch.MinCount)
		}
		if ch.Counter() == nil {
			t.Error("the count pattern should be compiled by the time the check is returned")
		}
	})

	t.Run("count_min with a stated minimum", func(t *testing.T) {
		ch := parse(t, `tests:count_min[count_pattern=ok (\d+) tests;min_count=20]:make test`)
		if ch.MinCount != 20 {
			t.Fatalf("min_count: %d", ch.MinCount)
		}
		if ch.CountPattern != `ok (\d+) tests` {
			t.Fatalf("count_pattern: %q", ch.CountPattern)
		}
	})

	t.Run("exit_in", func(t *testing.T) {
		ch := parse(t, "plan:exit_in[allowed_exits=0,2]:terraform plan -detailed-exitcode")
		if len(ch.AllowedExits) != 2 || ch.AllowedExits[0] != 0 || ch.AllowedExits[1] != 2 {
			t.Fatalf("allowed_exits: %v", ch.AllowedExits)
		}
	})

	t.Run("output_matches", func(t *testing.T) {
		ch := parse(t, `lint:output_matches[expect_pattern=(?m)^0 problems]:npx eslint .`)
		if ch.ExpectPattern != "(?m)^0 problems" {
			t.Fatalf("expect_pattern: %q", ch.ExpectPattern)
		}
		if ch.Pattern() == nil {
			t.Error("the expectation should be compiled by the time the check is returned")
		}
	})

	t.Run("output_not_matches", func(t *testing.T) {
		ch := parse(t, `vet:output_not_matches[expect_pattern=(?i)\bTODO\b]:go vet ./...`)
		if ch.ExpectPattern != `(?i)\bTODO\b` {
			t.Fatalf("expect_pattern: %q", ch.ExpectPattern)
		}
	})

	// Every rule this build knows must be reachable from this flag, or the
	// setup path silently has a hole in it exactly where count_min was.
	for _, rule := range config.VerdictRules() {
		if _, ok := checkRuleParams[config.VerdictRule(rule)]; !ok {
			t.Errorf("verdict rule %q cannot be declared through -check", rule)
		}
	}
}

// TestAPatternKeepsItsMetacharacters covers the case the bracket scan exists
// for: a character class inside the parameter must not end the field.
func TestAPatternKeepsItsMetacharacters(t *testing.T) {
	ch := parse(t, `tests:count_min[count_pattern=([0-9]+) passed]:pytest -q`)
	if ch.CountPattern != "([0-9]+) passed" {
		t.Fatalf("count_pattern: %q", ch.CountPattern)
	}
	if strings.Join(ch.Command, " ") != "pytest -q" {
		t.Fatalf("command: %q", ch.Command)
	}
}

// TestAMissingParameterIsNamedHere is the split of responsibility that was the
// actual bug: this used to accept the spec and let the scaffold refuse the
// config, which reports a defect in the tool instead of the parameter somebody
// left out.
func TestAMissingParameterIsNamedHere(t *testing.T) {
	for _, tc := range []struct{ spec, want string }{
		{"tests:count_min:npm test", "count_pattern"},
		{"plan:exit_in:terraform plan", "allowed_exits"},
		{"lint:output_matches:npx eslint .", "expect_pattern"},
	} {
		_, err := parseCheckSpec(tc.spec)
		if err == nil {
			t.Fatalf("%q must be refused: the scaffold would reject it", tc.spec)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: the error should name %s, got %v", tc.spec, tc.want, err)
		}
		if strings.Contains(err.Error(), "which is a defect") {
			t.Errorf("%q: this must not defer to the scaffold's defect message", tc.spec)
		}
	}
}

func TestASpecThatCannotBeBuiltIsRefused(t *testing.T) {
	for name, spec := range map[string]string{
		"unknown rule":                   "t:no_such_rule:make",
		"unknown parameter":              "t:count_min[nope=1]:make",
		"parameter wrong rule":           "t:exit_zero[expect_pattern=x]:make",
		"unclosed bracket":               "t:count_min[count_pattern=x:make",
		"no command":                     "t:exit_zero",
		"empty command":                  "t:exit_zero:",
		"empty parameters":               "t:count_min[]:make",
		"not key=value":                  "t:count_min[count_pattern]:make",
		"min_count not a number":         "t:count_min[count_pattern=(\\d+);min_count=lots]:make",
		"bad regex":                      "t:count_min[count_pattern=(\\d+]:make",
		"count_pattern captures nothing": "t:count_min[count_pattern=tests]:make",
	} {
		if ch, err := parseCheckSpec(spec); err == nil {
			t.Errorf("%s: %q was accepted as %+v", name, spec, ch)
		}
	}
}

// TestTheShippedExampleMatchesRealToolOutput runs the example out of the help
// text itself rather than a copy of it, because the defect was that the two
// could differ and nothing noticed: `^0 problems` anchors to the start of the
// whole output, so an operator who copied it got a lint check that never went
// green — eslint prints a file count first, and Go's `^` is not per-line
// without `(?m)`.
//
// Both halves are asserted. A pattern that matched every output would pass the
// first check and be worse than one that matched nothing.
func TestTheShippedExampleMatchesRealToolOutput(t *testing.T) {
	var spec string
	for _, line := range strings.Split(checkFlagHelp, "\n") {
		if strings.Contains(line, "output_matches") {
			spec = strings.TrimSpace(line)
		}
	}
	if spec == "" {
		t.Fatal("the -check help no longer carries an output_matches example; this test has nothing to check")
	}

	ch := parse(t, spec)
	re := ch.Pattern()
	if re == nil {
		t.Fatalf("%q compiled to no pattern", spec)
	}

	const clean = "3 files checked\n0 problems\n"
	if !re.MatchString(clean) {
		t.Errorf("%q does not match a clean run:\n%s", re, clean)
	}

	const dirty = "3 files checked\n7 problems\n"
	if re.MatchString(dirty) {
		t.Errorf("%q matches a run with 7 problems, so the check can never go red:\n%s", re, dirty)
	}
}
