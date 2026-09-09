package authority

import (
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
)

// genCfg is a config with one area somebody can implement, so that the routing
// rules pass and the tests below are about the rule each one names.
func genCfg(t *testing.T) *config.Config {
	t.Helper()
	c := cfg(t)
	c.Routing = map[string]string{"core": "performer"}
	return c
}

// good is a proposal that every admission rule accepts. Each test below breaks
// exactly one thing about it, which is what keeps the tests honest: a rule that
// started refusing everything would fail here first.
func good() envelope.ProposedItem {
	return envelope.ProposedItem{
		ID: "S1-001", Title: "Render the sources section", Area: "core",
		Radius:    string(config.RadiusNone),
		FileScope: []string{"site/cats/index.html"},
		Criteria: []string{
			"AC-1 [exit_zero] npx html-validate public/index.html",
			"the page reads well to somebody who has never seen it",
		},
	}
}

func genFacts() GenerationFacts {
	return GenerationFacts{
		SegmentID:    "S1",
		SegmentBrief: "A one-page site about cats under site/cats, with photographs and sources.",
		ExistingIDs:  map[string]bool{},
	}
}

// TestAWellShapedItemIsAdmitted is the clean case for every rule in AdmitItem
// at once. Without it, each firing case below would pass just as happily on the
// day one of these rules started refusing everything.
func TestAWellShapedItemIsAdmitted(t *testing.T) {
	d := AdmitItem(genCfg(t), good(), genFacts())
	if !d.Admitted {
		t.Fatalf("a well-shaped item must be admitted, got [%s] %s", d.Reason, d.Detail)
	}
}

// --- an item must carry something the control plane can run -----------------

func TestAnItemOfPureProseIsRefused(t *testing.T) {
	p := good()
	p.Criteria = []string{
		"the sources section lists every photograph",
		"the page is readable on a phone",
	}
	d := AdmitItem(genCfg(t), p, genFacts())
	if d.Admitted || d.Reason != ReasonNoExecutableAC {
		t.Fatalf("want no_executable_criterion, got admitted=%v [%s] %s", d.Admitted, d.Reason, d.Detail)
	}
	// The refusal has to be actionable, or it is worked around rather than
	// fixed: it names the form and the rules that form accepts.
	for _, want := range []string{"[exit_zero]", "output_matches"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("the refusal should show how to write one; %q missing from %q", want, d.Detail)
		}
	}
}

// TestOneExecutableCriterionIsEnough is the clean case for the rule above.
// Prose is not a defect — criterion.go is right that some things need
// judgement — so an item that carries one runnable check and three sentences is
// admissible, and a rule that demanded all four would be worse than the one it
// replaced.
func TestOneExecutableCriterionIsEnough(t *testing.T) {
	p := good()
	p.Criteria = []string{
		"the page is readable on a phone",
		"AC-2 [output_nonempty] npx html-validate public/index.html",
		"the tone matches the rest of the site",
	}
	if d := AdmitItem(genCfg(t), p, genFacts()); !d.Admitted {
		t.Fatalf("one executable criterion among prose is enough, got [%s] %s", d.Reason, d.Detail)
	}
}

// TestACriterionThatCannotBeInvokedIsRefusedAndOneThatMerelyFailsIsNot is the
// distinction a well-behaved checker states in its exit codes, applied at
// admission. A
// criterion that runs and fails says something true about work that does not
// exist yet. One that cannot start says nothing, ever.
func TestACriterionThatCannotBeInvokedIsRefusedAndOneThatMerelyFailsIsNot(t *testing.T) {
	firing := map[string]string{
		"a shell pipeline":                      "AC-1 [exit_zero] npx html-validate public/index.html | grep ok",
		"a shell conjunction":                   "AC-1 [exit_zero] go build ./... && go test ./...",
		"a redirection":                         "AC-1 [output_empty] gofmt -l ./internal > /tmp/out",
		"a shell builtin":                       "AC-1 [exit_zero] cd site/cats",
		"a substitution":                        "AC-1 [exit_zero] go test $(go list ./...)",
		"a rule nobody has":                     "AC-1 [looks_right] go build ./...",
		"a rule with no place for its argument": "AC-1 [exit_in: 0,1] go build ./...",
		"a rule and no command":                 "AC-1 [exit_zero]",
		"a pattern rule with no pattern":        "AC-1 [output_matches] go build ./...",
	}
	for name, text := range firing {
		t.Run(name, func(t *testing.T) {
			p := good()
			p.Criteria = []string{text, "AC-2 [exit_zero] go build ./..."}
			d := AdmitItem(genCfg(t), p, genFacts())
			if d.Admitted || d.Reason != ReasonUnrunnableAC {
				t.Fatalf("want criterion_not_invocable, got admitted=%v [%s] %s", d.Admitted, d.Reason, d.Detail)
			}
		})
	}

	// The clean cases, and they are the point of the rule: a command that will
	// exit non-zero today because the work is not built, and plain prose, are
	// both fine. Refusing either would make the guard useless.
	clean := map[string]string{
		"an assertion that fails today":     "AC-1 [exit_zero] npx html-validate public/index.html",
		"a command that does not exist yet": "AC-1 [exit_zero] go run ./cmd/adlc prompt assemble narrator",
		"a pattern rule with a pattern":     "AC-1 [output_matches: sources] go run ./cmd/adlc config check",
		"prose that mentions a bracket":     "the alt text is written as [subject, setting]",
		"plain prose":                       "the page reads well",
	}
	for name, text := range clean {
		t.Run(name, func(t *testing.T) {
			if fault := criterionFault(text); fault != "" {
				t.Fatalf("%q must be admissible, got fault %q", text, fault)
			}
		})
	}
}

// --- the item has to be about what the brief asked for ----------------------

// TestWorkOutsideTheBriefIsRefused is the guard that was designed, named
// ReasonScopeInvented, and never wired to anything. Without it a reviewed
// five-item plan grew to forty-four, thirty-nine of them arriving after the
// last thing anybody had agreed to.
func TestWorkOutsideTheBriefIsRefused(t *testing.T) {
	p := good()
	p.FileScope = []string{"internal/dispatch/dispatch.go", "internal/lease/lease.go"}
	f := genFacts()
	f.SegmentScopes = []string{"site/cats/index.html", "site/cats/style.css"}

	d := AdmitItem(genCfg(t), p, f)
	if d.Admitted || d.Reason != ReasonScopeInvented {
		t.Fatalf("want scope_not_in_brief, got admitted=%v [%s] %s", d.Admitted, d.Reason, d.Detail)
	}
	// A refusal an agent cannot act on is one it works around. This one names
	// the roots it would have accepted.
	if !strings.Contains(d.Detail, "site/cats") {
		t.Errorf("the refusal should name where this deliverable's work lives: %q", d.Detail)
	}
}

// TestWorkInsideTheBriefIsAdmitted is the clean case, three ways: a path the
// brief itself names, a path a sibling item established, and an item that
// declares no scope at all — which this rule has nothing to say about, and
// says so by admitting it rather than by guessing.
func TestWorkInsideTheBriefIsAdmitted(t *testing.T) {
	cases := map[string]struct {
		scope  []string
		facts  func(GenerationFacts) GenerationFacts
		reason string
	}{
		"under a root the brief names": {
			scope: []string{"site/cats/sources.html"},
			facts: func(f GenerationFacts) GenerationFacts { return f },
		},
		"under a root a sibling established": {
			scope: []string{"site/cats/img/CREDITS.tsv"},
			facts: func(f GenerationFacts) GenerationFacts {
				f.SegmentBrief = "A one-page site about cats."
				f.SegmentScopes = []string{"site/cats/index.html"}
				return f
			},
		},
		"one path of several lands in the brief": {
			scope: []string{"docs/notes.md", "site/cats/index.html"},
			facts: func(f GenerationFacts) GenerationFacts { return f },
		},
		"no declared scope at all": {
			scope: nil,
			facts: func(f GenerationFacts) GenerationFacts { return f },
		},
		"the first item of a segment with nothing to compare against": {
			scope: []string{"anywhere/at/all.go"},
			facts: func(f GenerationFacts) GenerationFacts {
				f.SegmentBrief = "A one-page site about cats."
				return f
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := good()
			p.FileScope = c.scope
			if d := AdmitItem(genCfg(t), p, c.facts(genFacts())); !d.Admitted {
				t.Fatalf("want admitted, got [%s] %s", d.Reason, d.Detail)
			}
		})
	}
}

// --- two items must not be handed one file ----------------------------------

// TestTwoItemsCannotClaimOneFile is the depth defect. S1-004 and S1-011 both
// declared site/cats/index.html; the second could not start until the first
// landed, so the plan's depth set the wall clock and running more agents at
// once changed nothing.
func TestTwoItemsCannotClaimOneFile(t *testing.T) {
	f := genFacts()
	f.OpenScopes = map[string][]string{"S1-004": {"./site/cats/index.html"}}

	d := AdmitItem(genCfg(t), good(), f)
	if d.Admitted || d.Reason != ReasonScopeCollision {
		t.Fatalf("want file_scope_collision, got admitted=%v [%s] %s", d.Admitted, d.Reason, d.Detail)
	}
	if !strings.Contains(d.Detail, "S1-004") {
		t.Errorf("the refusal has to name the item already holding the file: %q", d.Detail)
	}

	// A directory claimed by one item covers the files under it, or the guard
	// is avoided by naming the parent.
	f.OpenScopes = map[string][]string{"S1-004": {"site/cats"}}
	if d := AdmitItem(genCfg(t), good(), f); d.Reason != ReasonScopeCollision {
		t.Errorf("a directory claim covers what is under it, got [%s] %s", d.Reason, d.Detail)
	}
}

// TestDisjointScopesAreAdmitted is the clean case. Two items that touch
// different files are the shape the planner is asked for, and refusing them
// would serialise the fleet in the name of stopping it being serialised.
func TestDisjointScopesAreAdmitted(t *testing.T) {
	f := genFacts()
	f.OpenScopes = map[string][]string{
		"S1-004": {"site/cats/style.css"},
		"S1-005": {"site/cats/img/CREDITS.tsv"},
	}
	if d := AdmitItem(genCfg(t), good(), f); !d.Admitted {
		t.Fatalf("disjoint scopes must be admitted, got [%s] %s", d.Reason, d.Detail)
	}

	// A finished item's files serialise nobody, so only open items are carried
	// in OpenScopes and a segment scope on its own is not a collision.
	f.OpenScopes = nil
	f.SegmentScopes = []string{"site/cats/index.html"}
	if d := AdmitItem(genCfg(t), good(), f); !d.Admitted {
		t.Fatalf("a closed item's files must not block new work, got [%s] %s", d.Reason, d.Detail)
	}
}

// TestScopePathsAreComparedCanonically pins the spelling problem underneath
// both scope rules. Two agents naming one file three ways is exactly the
// collision these exist to see, and a string compare would miss all of it.
func TestScopePathsAreComparedCanonically(t *testing.T) {
	for _, spelling := range []string{
		"site/cats/index.html", "./site/cats/index.html", `site\cats\index.html`, "/site/cats/index.html",
	} {
		f := genFacts()
		f.OpenScopes = map[string][]string{"S1-004": {spelling}}
		if d := AdmitItem(genCfg(t), good(), f); d.Reason != ReasonScopeCollision {
			t.Errorf("%q names the same file, got [%s]", spelling, d.Reason)
		}
	}
}

// TestGenerationRulesAreAPureFunctionOfTheirFacts is the property the package
// rests on. Nothing in AdmitItem may read the ledger, the clock or the
// filesystem: a rule that does cannot be tested without a database, and rules
// that are expensive to test are rules nobody writes a negative case for.
func TestGenerationRulesAreAPureFunctionOfTheirFacts(t *testing.T) {
	c, p, f := genCfg(t), good(), genFacts()
	f.SegmentScopes = []string{"site/cats/index.html"}
	f.OpenScopes = map[string][]string{"S1-009": {"site/cats/style.css"}, "S1-010": {"docs/x.md"}}
	first := AdmitItem(c, p, f)
	for i := 0; i < 20; i++ {
		if got := AdmitItem(c, p, f); got != first {
			t.Fatalf("the same inputs gave two answers: %+v then %+v", first, got)
		}
	}
}
