package envelope

import "testing"

// The severity vocabulary has two readers and they ask different questions.
//
// Blockers asks "is this run contradicting itself by reporting a pass". Grounds
// ForRejection asks "did this rejection cite a substantive defect". Reading one
// answer for both questions is what discarded six rejections that each cited a
// real defect weighted "major".

func withFindings(t *testing.T, verdict string, severities ...string) *Envelope {
	t.Helper()
	e := &Envelope{Verdict: verdict}
	for _, s := range severities {
		e.Outputs.Findings = append(e.Outputs.Findings, Finding{
			Severity: s, Location: "x.go:1", Evidence: "e", Required: "r",
		})
	}
	return e
}

func TestTheTwoHalvesOfTheSeverityVocabularyDrawDifferentLines(t *testing.T) {
	cases := []struct {
		severity    string
		blocks      bool // contradicts a pass
		substantive bool // substantiates a rejection
	}{
		{"blocker", true, true},
		{"major", false, true},
		{"minor", false, false},
		{"note", false, false},
	}
	for _, c := range cases {
		e := withFindings(t, "reject", c.severity)
		if got := len(e.Blockers()) > 0; got != c.blocks {
			t.Errorf("%q blocks a pass = %v, want %v", c.severity, got, c.blocks)
		}
		if got := len(e.GroundsForRejection()) > 0; got != c.substantive {
			t.Errorf("%q substantiates a rejection = %v, want %v", c.severity, got, c.substantive)
		}
	}
}

// TestTheSpellingsAWorkerActuallyUsesReachTheRightHalf is the half that was
// invisible. shapes.go maps medium, moderate and warning onto "major", so those
// are how most reviewers spell a real defect — and while only "blocker" counted
// as grounds, none of them could substantiate a rejection at all.
func TestTheSpellingsAWorkerActuallyUsesReachTheRightHalf(t *testing.T) {
	for _, spelling := range []string{"blocker", "blocking", "critical", "severe", "high", "error",
		"major", "medium", "moderate", "warning"} {
		e := withFindings(t, "reject", spelling)
		e.normalise()
		if len(e.GroundsForRejection()) != 1 {
			t.Errorf("%q is a substantive defect and must ground a rejection, read as %q",
				spelling, e.Outputs.Findings[0].Severity)
		}
	}
	// And the clean case: the weights that really are taste stay taste, however
	// they are spelled. A rule that grounded a rejection on anything at all
	// would pass the loop above and fail here.
	for _, spelling := range []string{"minor", "low", "note", "info", "nit", "suggestion"} {
		e := withFindings(t, "reject", spelling)
		e.normalise()
		if len(e.GroundsForRejection()) != 0 {
			t.Errorf("%q is taste and must not ground a rejection, read as %q",
				spelling, e.Outputs.Findings[0].Severity)
		}
	}
}
