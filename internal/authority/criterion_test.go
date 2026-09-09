package authority

import (
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// A criterion that names a command and how to read it is settled by the control
// plane, not by dispatching an agent to run it and report back.
//
// This was the one verification in the whole system taken on an agent's word.
// Everywhere else the gate executes the declared commands itself, because an
// envelope is a declaration and never evidence.
func TestACriterionCarryingACheckIsExecutable(t *testing.T) {
	cs := ParseCriteria([]string{
		"AC-1 [exit_zero] go run ./cmd/adlc config check",
		"AC-2 [output_matches: DIRTY] go run ./cmd/adlc gate run -workdir .",
		"AC-3 the page is readable by a five-year-old",
	})
	if len(cs) != 3 {
		t.Fatalf("parsed %d criteria, want 3", len(cs))
	}
	if !cs[0].Executable() || cs[0].Rule != config.VerdictExitZero {
		t.Errorf("AC-1 not executable: %+v", cs[0])
	}
	if got := cs[0].Command; len(got) != 5 || got[0] != "go" {
		t.Errorf("AC-1 command %v", got)
	}
	if cs[1].Want != "DIRTY" {
		t.Errorf("AC-2 lost its expected match: %q", cs[1].Want)
	}

	// The clean case, and the important one: prose is not a defect. Some things
	// are not mechanically checkable, and pretending otherwise is worse than
	// admitting it — so AC-3 stays a criterion and needs judgement.
	if cs[2].Executable() {
		t.Error("prose was treated as a command")
	}
	if cs[2].Text != "AC-3 the page is readable by a five-year-old" {
		t.Error("a criterion nobody can execute must still be carried verbatim")
	}
	need := NeedsJudgement(cs)
	if len(need) != 1 || need[0].Text != "AC-3 the page is readable by a five-year-old" {
		t.Errorf("wrong criteria sent for judgement: %+v", need)
	}
}

// An item whose criteria are all executable needs no judge at all. That is the
// whole saving: a ten-to-fifteen minute agent run replaced by running the
// commands that were already written down.
func TestAnItemWithOnlyExecutableCriteriaNeedsNoJudge(t *testing.T) {
	cs := ParseCriteria([]string{
		"AC-1 [exit_zero] go build ./...",
		"AC-2 [output_empty] gofmt -l ./cmd",
	})
	if n := NeedsJudgement(cs); len(n) != 0 {
		t.Fatalf("%d criteria still need a judge: %+v", len(n), n)
	}
}

// An unrecognised rule leaves the criterion as prose rather than dropping it.
// A criterion nobody checks and a criterion nobody knows about look identical
// afterwards, and only one of them is safe.
func TestAnUnknownRuleFallsBackToProse(t *testing.T) {
	c := ParseCriterion("AC-1 [vibes_good] go test ./...")
	if c.Executable() {
		t.Fatal("a rule this build does not understand was executed anyway")
	}
	if c.Text != "AC-1 [vibes_good] go test ./..." {
		t.Errorf("the criterion was altered: %q", c.Text)
	}
}

// Prose that merely mentions a bracket is not a check.
func TestProseMentioningABracketIsNotACommand(t *testing.T) {
	c := ParseCriterion("the output should look like [DIRTY 3] when files are uncommitted")
	if c.Executable() {
		t.Fatalf("prose was parsed as a command: %+v", c)
	}
}
