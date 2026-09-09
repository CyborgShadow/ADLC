package envelope

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The JSON blocks in agents/_preamble.md are the only description of the
// envelope 22 role prompts ever see, and nothing joined them to the structs
// that parse them: the outputs.criteria illustration landed without the `text`
// field Criterion declares, and needed a second commit to correct. A block that
// does not parse teaches every worker in the fleet to write something the
// control plane will refuse.
const preamblePath = "../../agents/_preamble.md"

// criteriaBlocks returns the fenced JSON blocks in the preamble that illustrate
// outputs.criteria. Zero blocks is a failure and never a skip: a locator that
// finds nothing and reports green turns "I checked nothing" into "everything
// matched", which is the exact shape of the hole this test exists to close.
func criteriaBlocks(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(preamblePath)
	if err != nil {
		t.Fatalf("the preamble every role is served must be readable from here: %v", err)
	}
	var blocks []string
	var cur []string
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n") {
		switch {
		case !in && strings.TrimSpace(line) == "```json":
			in, cur = true, nil
		case in && strings.HasPrefix(strings.TrimSpace(line), "```"):
			in = false
			body := strings.Join(cur, "\n")
			if strings.Contains(body, `"criteria"`) {
				blocks = append(blocks, body)
			}
		case in:
			cur = append(cur, line)
		}
	}
	if in {
		t.Fatalf("%s has an unclosed ```json fence", preamblePath)
	}
	if len(blocks) == 0 {
		t.Fatalf("no fenced json block in %s illustrates outputs.criteria; "+
			"either the illustration was removed or this locator no longer finds it, "+
			"and both must fail rather than pass on nothing", preamblePath)
	}
	return blocks
}

// envelopeAround wraps an outputs fragment in the smallest envelope that is
// otherwise valid, so what Parse refuses is the fragment and nothing else.
func envelopeAround(fragment string) []byte {
	return []byte(`{"envelope_version":"1","run_id":"r-preamble",` +
		`"worker_type":"implementer","verdict":"pass",` + strings.TrimSpace(fragment) + `}`)
}

// criteriaOf pulls the criteria out of a block as raw JSON, so a mutation can
// be made structurally rather than by editing the markdown text.
func criteriaOf(t *testing.T, block string) []json.RawMessage {
	t.Helper()
	var doc struct {
		Outputs struct {
			Criteria []json.RawMessage `json:"criteria"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(envelopeAround(block), &doc); err != nil {
		t.Fatalf("the block is not JSON at all: %v", err)
	}
	if len(doc.Outputs.Criteria) == 0 {
		t.Fatal("the block illustrates outputs.criteria with no criteria in it")
	}
	return doc.Outputs.Criteria
}

func envelopeWithCriteria(t *testing.T, crits []json.RawMessage) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"criteria": crits})
	if err != nil {
		t.Fatal(err)
	}
	return envelopeAround(`"outputs":` + string(body))
}

// The clean case: the block exactly as the preamble carries it goes through the
// same decoder a run's envelope goes through, and carries every field the
// struct declares without omitempty.
func TestThePreambleCriteriaBlockParsesAsAnEnvelope(t *testing.T) {
	required := requiredCriterionFields(t)
	for i, block := range criteriaBlocks(t) {
		e, err := Parse(envelopeAround(block))
		if err != nil {
			t.Fatalf("block %d in %s does not parse: %v", i, preamblePath, err)
		}
		if len(e.Criteria()) == 0 {
			t.Fatalf("block %d parsed to no criteria at all", i)
		}
		// Read the statuses off the raw block rather than off the parsed
		// envelope: normalise reads "met" as "pass", so the parsed value would
		// hide an illustration that told 22 prompts to write a synonym.
		for j, el := range criteriaOf(t, block) {
			var c map[string]json.RawMessage
			if err := json.Unmarshal(el, &c); err != nil {
				t.Errorf("block %d criteria[%d] is not an object: %v", i, j, err)
				continue
			}
			for _, field := range required {
				if _, ok := c[field]; !ok {
					t.Errorf("block %d criteria[%d] omits %q, which envelope.Criterion declares without omitempty",
						i, j, field)
				}
			}
			var status string
			if err := json.Unmarshal(c["status"], &status); err != nil {
				t.Errorf("block %d criteria[%d] status is not a string: %v", i, j, err)
				continue
			}
			switch status {
			case "pass", "fail", "untested":
			default:
				t.Errorf("block %d criteria[%d] illustrates status %q, which is not one of the three words",
					i, j, status)
			}
		}
	}
}

// requiredCriterionFields is the json names Criterion declares without
// omitempty. Taken from the struct rather than listed here, so a field added to
// the struct is a field the illustration has to gain.
func requiredCriterionFields(t *testing.T) []string {
	t.Helper()
	var out []string
	ty := reflect.TypeOf(Criterion{})
	for i := 0; i < ty.NumField(); i++ {
		tag := ty.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" || strings.Contains(opts, "omitempty") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("envelope.Criterion declares no mandatory field; the check above would then assert nothing")
	}
	return out
}

// The firing case, so the guard cannot pass vacuously the day Parse starts
// accepting anything. Both mutations are made to the block the preamble
// actually carries, so if it moves they move with it.
func TestAMutatedPreambleCriteriaBlockIsRefused(t *testing.T) {
	for i, block := range criteriaBlocks(t) {
		t.Run("a criterion written as prose", func(t *testing.T) {
			crits := criteriaOf(t, block)
			mutated := append([]json.RawMessage(nil), crits...)
			mutated[0] = json.RawMessage(`"AC-1 passed, see the output above"`)
			assertRefusedNamingCriteria(t, envelopeWithCriteria(t, mutated), i)
		})

		t.Run("a status from no vocabulary", func(t *testing.T) {
			crits := criteriaOf(t, block)
			var one map[string]json.RawMessage
			if err := json.Unmarshal(crits[0], &one); err != nil {
				t.Fatal(err)
			}
			one["status"] = json.RawMessage(`"looks about right"`)
			raw, err := json.Marshal(one)
			if err != nil {
				t.Fatal(err)
			}
			mutated := append([]json.RawMessage(nil), crits...)
			mutated[0] = raw
			assertRefusedNamingCriteria(t, envelopeWithCriteria(t, mutated), i)
		})
	}
}

func assertRefusedNamingCriteria(t *testing.T, raw []byte, block int) {
	t.Helper()
	_, err := Parse(raw)
	if err == nil {
		t.Fatalf("block %d: the mutation parsed, so the clean case above proves nothing", block)
	}
	if _, ok := err.(ErrMalformed); !ok {
		t.Fatalf("block %d: a bad envelope is refusable, not fatal; got %T", block, err)
	}
	// A refusal an agent cannot classify is one it works around rather than
	// fixes, so the message has to name the field it is about.
	if !strings.Contains(err.Error(), "outputs.criteria") {
		t.Errorf("block %d: refusal does not name outputs.criteria: %v", block, err)
	}
}
