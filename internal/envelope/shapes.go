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
	// Decoded a value at a time so that a value which is not a criterion can be
	// refused by naming the shape it should have had. Returning json.Unmarshal's
	// own error instead named a Go type the worker cannot see — "cannot unmarshal
	// string into Go struct field .AC-1 of type envelope.Criterion" is the text
	// S1-029 and S1-038 were refused with — and it says neither which entry was
	// wrong nor which fields it wanted, so the next run guesses at the shape and
	// spends another attempt discovering the same thing.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("outputs.criteria is neither a list nor an object keyed by criterion id")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var one Criterion
		if err := json.Unmarshal(m[k], &one); err != nil {
			return fmt.Errorf("outputs.criteria[%q] is not a criterion: each value needs id, "+
				"status (pass, fail or untested), command_index and evidence — a criterion "+
				"written as a bare string cannot cite the command that proved it, and a verdict "+
				"nothing was run for is the one this system refuses", k)
		}
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
	// A finding reads like a sentence, so a worker writes the whole thing as one
	// and puts a bare string where the list belongs. There is no list to
	// re-shape, so this is refused -- and the refusal has to name the subfield
	// itself, because the decoder's own message ("cannot unmarshal string into
	// Go value of type []jsontext.Value") names an internal type the worker
	// cannot map back to anything it wrote. criteriaList above already names
	// its field; a worker deserves the same answer on either side.
	if b[0] != '[' {
		return fmt.Errorf(`outputs.findings is a list of finding objects, not %s; one finding is still a list of one`,
			describeJSON(b))
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	for i, el := range arr {
		el = bytes.TrimSpace(el)
		if len(el) > 0 && el[0] == '"' {
			var s string
			if err := json.Unmarshal(el, &s); err != nil {
				return fmt.Errorf("outputs.findings[%d]: %w", i, err)
			}
			*f = append(*f, Finding{Evidence: s})
			continue
		}
		var one Finding
		if err := json.Unmarshal(el, &one); err != nil {
			return fmt.Errorf("outputs.findings[%d]: %w", i, err)
		}
		*f = append(*f, one)
	}
	return nil
}

// describeJSON names what a worker actually wrote, in the words it would use
// about it. A refusal that quotes the value back is unreadable when the value
// is a page of prose, and one that quotes a Go type is unactionable.
func describeJSON(b []byte) string {
	if len(b) == 0 {
		return "nothing"
	}
	switch b[0] {
	case '"':
		return "a single string"
	case '{':
		return "an object"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	default:
		return "a number"
	}
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
