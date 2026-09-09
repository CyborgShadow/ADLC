package envelope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The shapes in this file are the ones agents actually wrote, counted from the
// ledger rather than imagined: an object keyed by criterion id where the schema
// declares an array, a bare string where it declares a finding, and a verdict
// word ("met") that means exactly what the declared word means. Each of those
// cost a whole agent run and produced a refusal that told the next run nothing,
// because the next run could not see it.
//
// The line this file will not cross: it re-shapes, it never invents. A word
// with a meaning gets its declared spelling; a fact the worker did not state
// stays unstated and is resolved against the worker's own verdict, failing
// closed. Parse's raw bytes are untouched, so the blob on the chain is still
// exactly what the agent wrote and the normalisation is auditable against it.

// criteriaList accepts the declared array and the object-keyed-by-id an agent
// writes when it is thinking of the criteria as a map. The key becomes the id
// when the value does not carry one, and the keys are sorted so that two runs
// over the same envelope report the criteria in the same order.
type criteriaList []Criterion

func (c *criteriaList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var arr []Criterion
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*c = arr
		return nil
	}
	if b[0] != '{' {
		return fmt.Errorf("outputs.criteria is neither a list nor an object keyed by criterion id")
	}
	var m map[string]Criterion
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		one := m[k]
		if strings.TrimSpace(one.ID) == "" {
			one.ID = k
		}
		*c = append(*c, one)
	}
	return nil
}

// findingsList accepts a finding written as prose as well as one written as an
// object. A prose finding carries no severity, which is left unstated here and
// resolved in normalise -- guessing it in the decoder, where the verdict is not
// visible, is how a blocker becomes a note.
type findingsList []Finding

func (f *findingsList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	for _, el := range arr {
		el = bytes.TrimSpace(el)
		if len(el) > 0 && el[0] == '"' {
			var s string
			if err := json.Unmarshal(el, &s); err != nil {
				return err
			}
			*f = append(*f, Finding{Evidence: s})
			continue
		}
		var one Finding
		if err := json.Unmarshal(el, &one); err != nil {
			return err
		}
		*f = append(*f, one)
	}
	return nil
}

// statusWords are the spellings of a criterion verdict that mean one of the
// declared three. Nothing ambiguous is in here: a word this map does not know
// stays as written and is refused, because a verdict guessed wrong is the one
// error this system exists to prevent.
var statusWords = map[string]string{
	"pass": "pass", "passed": "pass", "passing": "pass", "met": "pass",
	"satisfied": "pass", "ok": "pass", "yes": "pass", "true": "pass",

	"fail": "fail", "failed": "fail", "failing": "fail", "unmet": "fail",
	"not met": "fail", "not_met": "fail", "violated": "fail", "no": "fail",
	"false": "fail",

	"untested": "untested", "not tested": "untested", "not_tested": "untested",
	"skipped": "untested", "unknown": "untested", "n/a": "untested",
	"na": "untested", "": "untested",
}

// severityWords map a finding's severity onto the declared four. Where two
// vocabularies do not line up the mapping goes to the heavier value, because a
// finding recorded lighter than the worker meant is one an item passes over.
var severityWords = map[string]string{
	"blocker": "blocker", "blocking": "blocker", "critical": "blocker",
	"severe": "blocker", "high": "blocker", "error": "blocker",

	"major": "major", "medium": "major", "moderate": "major", "warning": "major",

	"minor": "minor", "low": "minor",

	"note": "note", "info": "note", "informational": "note", "nit": "note",
	"suggestion": "note",
}

// normalise gives the shapes above their declared spelling, and returns a line
// per change so the run's log can say what was re-shaped. It runs before
// validate, so anything it cannot resolve is still refused.
func (e *Envelope) normalise() {
	for i := range e.Outputs.Criteria {
		raw := e.Outputs.Criteria[i].Status
		if want, ok := statusWords[strings.ToLower(strings.TrimSpace(raw))]; ok && want != raw {
			e.Outputs.Criteria[i].Status = want
			e.normalised = append(e.normalised,
				fmt.Sprintf("outputs.criteria[%d].status %q read as %q", i, raw, want))
		}
	}

	// A finding with no severity is a worker that wrote the defect and not its
	// weight. The weight it did state is its own verdict, so take it from
	// there: a run that rejected means the things it listed were reasons.
	unstated := "note"
	switch e.Verdict {
	case "fail", "reject", "blocked":
		unstated = "blocker"
	}
	for i := range e.Outputs.Findings {
		raw := strings.TrimSpace(e.Outputs.Findings[i].Severity)
		if raw == "" {
			e.Outputs.Findings[i].Severity = unstated
			e.normalised = append(e.normalised,
				fmt.Sprintf("outputs.findings[%d] stated no severity; the run's own verdict %q makes it %q",
					i, e.Verdict, unstated))
			continue
		}
		if want, ok := severityWords[strings.ToLower(raw)]; ok {
			if want != raw {
				e.Outputs.Findings[i].Severity = want
				e.normalised = append(e.normalised,
					fmt.Sprintf("outputs.findings[%d].severity %q read as %q", i, raw, want))
			}
			continue
		}
		// A word from no vocabulary anybody declared is left exactly as written,
		// so validate refuses it. Reading it as SOME severity would be this file
		// inventing the weight of a defect, which is the one thing it must not do
		// -- and the refusal now teaches, because dispatch records it as a lesson.
	}
}

// Normalised says what Parse re-shaped, for the run's log. An empty result is
// the ordinary case: the worker wrote the declared shape.
func (e *Envelope) Normalised() []string { return e.normalised }
