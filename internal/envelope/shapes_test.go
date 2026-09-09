package envelope

import (
	"strings"
	"testing"
)

func outputs(body string) string {
	return strings.Replace(minimal, `"outputs": {}`, `"outputs": `+body, 1)
}

// Each of these shapes cost a real agent run and a refusal that taught the next
// run nothing. Counted from the ledger of the first fleet test: three envelopes
// wrote criteria as an object keyed by id, two wrote "met" where the schema
// says "pass", and two wrote findings as prose. None of them was wrong about
// the work -- they were wrong about the shape, and the whole run was discarded.
func TestTheShapesAgentsActuallyWriteAreStillEnvelopes(t *testing.T) {
	t.Run("criteria keyed by id", func(t *testing.T) {
		e, err := Parse([]byte(outputs(`{"criteria":{
			"AC-2":{"status":"pass","evidence":"b","command_index":1},
			"AC-1":{"status":"fail","evidence":"a","command_index":0}}}`)))
		if err != nil {
			t.Fatal(err)
		}
		cs := e.Criteria()
		if len(cs) != 2 {
			t.Fatalf("both criteria should survive, got %d", len(cs))
		}
		// Sorted, so two reads of one envelope report the same order.
		if cs[0].ID != "AC-1" || cs[1].ID != "AC-2" {
			t.Errorf("the object key is the id when the value carries none: %+v", cs)
		}
		if len(e.FailedCriteria()) != 1 {
			t.Error("a failing criterion must still reach FailedCriteria")
		}
	})

	t.Run("met means pass", func(t *testing.T) {
		e, err := Parse([]byte(outputs(`{"criteria":[
			{"id":"AC-1","status":"met","evidence":"a","command_index":0},
			{"id":"AC-2","status":"NOT MET","evidence":"b","command_index":0},
			{"id":"AC-3","status":"skipped","evidence":"c","command_index":0}]}`)))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"pass", "fail", "untested"}
		for i, c := range e.Criteria() {
			if c.Status != want[i] {
				t.Errorf("criteria[%d] read as %q, want %q", i, c.Status, want[i])
			}
		}
		if len(e.Normalised()) != 3 {
			t.Errorf("every re-shaping must be sayable, got %v", e.Normalised())
		}
	})

	t.Run("a finding written as prose", func(t *testing.T) {
		e, err := Parse([]byte(outputs(`{"findings":["the cache key omits the locale"]}`)))
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Outputs.Findings) != 1 || e.Outputs.Findings[0].Evidence != "the cache key omits the locale" {
			t.Fatalf("the prose is the evidence: %+v", e.Outputs.Findings)
		}
		// The verdict on `minimal` is pass, so a defect the worker did not
		// weigh is a note. It is not invented: the worker weighed the run.
		if e.Outputs.Findings[0].Severity != "note" {
			t.Errorf("an unstated severity on a passing run is a note, got %q", e.Outputs.Findings[0].Severity)
		}
	})

	t.Run("a finding written as prose on a rejecting run", func(t *testing.T) {
		body := strings.Replace(outputs(`{"findings":["the cache key omits the locale"]}`),
			`"verdict": "pass"`, `"verdict": "reject"`, 1)
		e, err := Parse([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Blockers()) != 1 {
			t.Fatalf("a run that rejected listed its reasons; they block: %+v", e.Outputs.Findings)
		}
	})
}

// The clean case: an envelope in the declared shape is untouched, and says so.
// Without this, the guard above would still pass on the day normalise started
// rewriting everything it was handed.
func TestADeclaredEnvelopeIsNotReshaped(t *testing.T) {
	e, err := Parse([]byte(outputs(`{"criteria":[{"id":"AC-1","status":"pass","evidence":"a","command_index":0}],
		"findings":[{"severity":"major","location":"x","evidence":"y","required_change":"z"}]}`)))
	if err != nil {
		t.Fatal(err)
	}
	if n := e.Normalised(); len(n) != 0 {
		t.Errorf("nothing needed re-shaping, yet: %v", n)
	}
	if e.Criteria()[0].Status != "pass" || e.Outputs.Findings[0].Severity != "major" {
		t.Error("a declared envelope must survive parse unchanged")
	}
}

// Meaning is still unforgiving. A word from no vocabulary is refused rather
// than read as some severity, because reading it would be this package
// inventing the weight of a defect.
func TestReshapingStopsAtMeaning(t *testing.T) {
	for name, body := range map[string]string{
		"a severity from nowhere": outputs(`{"findings":[{"severity":"pretty bad","location":"x","evidence":"y","required_change":"z"}]}`),
		"a status from nowhere":   outputs(`{"criteria":[{"id":"AC-1","status":"mostly","evidence":"a","command_index":0}]}`),
		"criteria as a number":    outputs(`{"criteria": 3}`),
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s should be refused", name)
		} else if _, ok := err.(ErrMalformed); !ok {
			t.Errorf("%s should be refusable rather than fatal, got %T", name, err)
		}
	}
}
