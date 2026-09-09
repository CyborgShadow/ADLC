package envelope

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const minimal = `{
  "envelope_version": "1",
  "run_id": "p-1",
  "worker_type": "performer",
  "work_item_id": "S1-001",
  "verdict": "pass",
  "commands_run": [],
  "outputs": {}
}`

func TestAWellFormedEnvelopeParses(t *testing.T) {
	e, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.RunID != "p-1" || e.Verdict != "pass" {
		t.Fatalf("unexpected: %+v", e)
	}
	if e.SHA() == "" {
		t.Error("an envelope must be identifiable by digest")
	}
}

func TestAByteOrderMarkDoesNotDestroyARun(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(minimal)...)
	if _, err := Parse(withBOM); err != nil {
		t.Fatalf("a BOM must not make an envelope unreadable: %v", err)
	}
	crlf := strings.ReplaceAll(minimal, "\n", "\r\n")
	if _, err := Parse([]byte("   \n" + crlf + "\n\n")); err != nil {
		t.Fatalf("CRLF and surrounding whitespace must not matter: %v", err)
	}
}

func TestAFencedEnvelopeIsStillAnEnvelope(t *testing.T) {
	fenced := "```json\n" + minimal + "\n```"
	if _, err := Parse([]byte(fenced)); err != nil {
		t.Fatalf("refusing a fenced envelope costs a whole run to punish formatting: %v", err)
	}
}

func TestMeaningIsCheckedStrictly(t *testing.T) {
	cases := map[string]string{
		"a verdict outside the vocabulary": strings.Replace(minimal, `"pass"`, `"probably fine"`, 1),
		"no run id":                        strings.Replace(minimal, `"run_id": "p-1",`, ``, 1),
		"an unknown schema version":        strings.Replace(minimal, `"1"`, `"7"`, 1),
	}
	for name, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s should be refused", name)
		} else if _, ok := err.(ErrMalformed); !ok {
			t.Errorf("%s should be ErrMalformed so it is refusable rather than fatal, got %T", name, err)
		}
	}
	if _, err := Parse(nil); err == nil {
		t.Error("an empty envelope should be refused")
	}
}

func TestFindingSeverityIsAClosedVocabulary(t *testing.T) {
	bad := strings.Replace(minimal, `"outputs": {}`,
		`"outputs": {"findings":[{"severity":"pretty bad","location":"x","evidence":"y","required_change":"z"}]}`, 1)
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("a free-text severity is not a severity")
	}
	good := strings.Replace(minimal, `"outputs": {}`,
		`"outputs": {"findings":[{"severity":"blocker","location":"x","evidence":"y","required_change":"z"}]}`, 1)
	e, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Blockers()) != 1 {
		t.Error("a blocker finding should be reachable through Blockers()")
	}
}

func TestAClaimIsFoundByCheckIdNotBySubstring(t *testing.T) {
	body := strings.Replace(minimal, `"commands_run": []`,
		`"commands_run": [{"check_id":"test","cmd":"go test ./...","exit_code":0}]`, 1)
	e, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Claim("test"); !ok {
		t.Error("the declared check should be found by id")
	}
	// Matching on the command string is what let a command sharing a prefix with
	// the real one satisfy a gate while running nothing.
	if _, ok := e.Claim("go test ./..."); ok {
		t.Error("a claim must not be findable by its command text")
	}
}

// The claim about a check is its LAST run, not its first.
//
// An agent that runs a check, sees it fail, fixes the cause and runs it again
// records both — and it should: the working is the evidence. But the claim it
// is making is about the tree it is proposing, which is the final run. Taking
// the first compared the gate's observation of the tree as it IS against the
// agent's note of the tree as it WAS, and refused the work for a discrepancy
// that was really an agent showing its working.
func TestAClaimIsTheLastRunOfACheck(t *testing.T) {
	env := &Envelope{Commands: []Command{
		{CheckID: "fmt", Cmd: "gofmt -l .", ExitCode: 0, OutputTail: "internal/a.go"},
		{CheckID: "test", Cmd: "go test ./...", ExitCode: 1},
		{CheckID: "fmt", Cmd: "gofmt -l .", ExitCode: 0, OutputTail: ""},
	}}
	got, ok := env.Claim("fmt")
	if !ok {
		t.Fatal("the envelope reported a check and the claim was not found")
	}
	if got.OutputTail != "" {
		t.Errorf("took the earlier run (%q); the claim is about the tree being proposed", got.OutputTail)
	}

	// Unaffected when a check was run once, which is the ordinary case.
	if c, ok := env.Claim("test"); !ok || c.ExitCode != 1 {
		t.Errorf("a single run was not returned intact: %+v", c)
	}
	// And a check nobody ran is still absent rather than empty-but-present.
	if _, ok := env.Claim("vet"); ok {
		t.Error("a check the envelope never mentions was reported as claimed")
	}
}

// Criterion status is a closed vocabulary of three words, and the refusal for a
// word outside it names the field.
//
// statusWords lets a known synonym through under its declared spelling, so the
// dangerous case is no longer a synonym but a word from nowhere: it stops at
// validate, and unless the refusal says WHICH field it is, the next run gets a
// rejection it cannot classify and works around rather than fixes. Naming the
// field is the half of the guard that makes the message actionable, and it is
// the half nothing else here holds.
func TestCriterionStatusIsAClosedVocabulary(t *testing.T) {
	withCriteria := func(entries string) string {
		return strings.Replace(minimal, `"outputs": {}`, `"outputs": {"criteria":[`+entries+`]}`, 1)
	}

	// Firing case: a word no vocabulary knows. Asserting it is absent from
	// statusWords is what stops this going quiet — the day somebody makes this
	// word a synonym, the guard would start passing for the wrong reason, which
	// is how S1-025's first attempt was invalidated by a change it never saw.
	const fromNowhere = "roughly there"
	if _, ok := statusWords[fromNowhere]; ok {
		t.Fatalf("%q is now a known synonym, so it no longer fires this guard; pick another", fromNowhere)
	}
	bad := withCriteria(`{"id":"AC-1","text":"t","status":"` + fromNowhere + `","command_index":0,"evidence":"e"}`)
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("%q is not a status and normalise cannot read it; it must be refused", fromNowhere)
	}
	if _, ok := err.(ErrMalformed); !ok {
		t.Errorf("should be ErrMalformed so it is refusable rather than fatal, got %T", err)
	}
	if !strings.Contains(err.Error(), "outputs.criteria") {
		t.Errorf("the refusal must name the field to fix, got %q", err)
	}

	// Clean case: each of the three parses, and survives a round-trip through
	// JSON — the statuses have to still be there when the ledger reads them back.
	want := []string{"pass", "fail", "untested"}
	good := withCriteria(`{"id":"AC-1","text":"t","status":"pass","command_index":0,"evidence":"e"},` +
		`{"id":"AC-2","text":"t","status":"fail","command_index":0,"evidence":"e"},` +
		`{"id":"AC-3","text":"t","status":"untested","command_index":0,"evidence":"e"}`)
	e, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("all three of pass, fail and untested must parse: %v", err)
	}
	statuses := func(e *Envelope) []string {
		var out []string
		for _, c := range e.Criteria() {
			out = append(out, c.Status)
		}
		return out
	}
	if got := statuses(e); !slices.Equal(got, want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}

	round, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(round)
	if err != nil {
		t.Fatalf("a parsed envelope must re-parse: %v", err)
	}
	if got := statuses(again); !slices.Equal(got, want) {
		t.Errorf("after a round-trip statuses %v, want %v", got, want)
	}
	if len(again.FailedCriteria()) != 1 {
		t.Errorf("the failing criterion should be reachable through FailedCriteria(), got %d", len(again.FailedCriteria()))
	}
}

// AC-3's firing half, and the reason it is worth having on top of the
// re-shaping tests next door: shapes.go decides which wrong shapes are read
// rather than refused, and everything it does NOT tolerate still discards the
// whole envelope — the verdict, the commands and the account of the work go
// with it. That is what agents/_preamble.md now tells every role, and this is
// what holds the file to it. Each refusal must name its subfield, because a
// worker cannot reshape a field the refusal did not identify.
func TestAWrongShapeInOutputsDiscardsTheEnvelopeAndNamesTheSubfield(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		// The one this item exists for: a finding reads like a sentence, so it
		// gets written as one, in place of the list rather than inside it.
		{"a finding written as one bare string",
			`{"findings":"the gate is reading the wrong tree"}`, "outputs.findings"},
		{"findings as a number", `{"findings":42}`, "outputs.findings"},
		// Inside the list, prose is tolerated (shapes_test.go) but a value that
		// is neither prose nor an object is not, and the index says which one.
		{"a finding that is neither prose nor an object",
			`{"findings":[{"severity":"note","evidence":"a"},42]}`, "outputs.findings[1]"},
		{"files_changed as objects",
			`{"files_changed":[{"path":"internal/gate/run.go"}]}`, "outputs.files_changed"},
		{"deferred as objects", `{"deferred":[{"why":"out of scope"}]}`, "outputs.deferred"},
		{"notes_md as a list", `{"notes_md":["one","two"]}`, "outputs.notes_md"},
		{"work_items as strings", `{"work_items":["build the thing"]}`, "outputs.work_items"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(outputs(tc.body)))
			if err == nil {
				t.Fatalf("%s must be refused, not read as an empty subfield", tc.name)
			}
			if _, ok := err.(ErrMalformed); !ok {
				t.Fatalf("the refusal must be retryable rather than fatal, got %T", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal must name %s so the next attempt knows what to reshape, got %q", tc.want, err)
			}
			// Naming the subfield is not enough on its own. The decoder's own
			// message for these reaches for the type it was decoding into, and
			// "[]jsontext.Value" is not something a worker can map back to
			// anything it wrote — it reads as an internal fault rather than as
			// its own envelope being the wrong shape.
			if strings.Contains(err.Error(), "jsontext") {
				t.Errorf("the refusal leaks the decoder's internal type instead of describing the shape: %q", err)
			}
		})
	}

	// The clean case, without which every row above still passes on the day
	// Parse starts refusing everything it is handed: the same five subfields,
	// each in its declared shape, in one envelope that parses intact.
	t.Run("the declared shapes all parse", func(t *testing.T) {
		e, err := Parse([]byte(outputs(`{
			"findings":[{"severity":"blocker","location":"internal/gate/run.go:88",
				"criterion":"AC-2","evidence":"observed exit 1","required_change":"run the check in the run's own tree"}],
			"files_changed":["internal/gate/run.go"],
			"deferred":["the second scanner"],
			"notes_md":"one paragraph",
			"work_items":[{"title":"build the thing"}]}`)))
		if err != nil {
			t.Fatalf("the declared shapes must parse: %v", err)
		}
		if len(e.Outputs.Findings) != 1 || len(e.Outputs.FilesChanged) != 1 ||
			len(e.Outputs.Deferred) != 1 || e.Outputs.Notes != "one paragraph" ||
			len(e.Outputs.WorkItems) != 1 {
			t.Errorf("a declared envelope lost a subfield in parsing: %+v", e.Outputs)
		}
		if n := e.Normalised(); len(n) != 0 {
			t.Errorf("nothing here needed re-shaping, yet: %v", n)
		}
	})
}

// Outputs decodes through custom unmarshallers, and an envelope is read back
// after it is written — by the ledger that stored it and by the story that
// replays it. A severity that survives Parse but not the round trip would
// downgrade a blocker to nothing at the point nobody is looking.
func TestAFindingSurvivesBeingWrittenOutAndReadBack(t *testing.T) {
	e, err := Parse([]byte(outputs(`{"findings":[{"severity":"major",
		"location":"internal/gate/run.go:88","criterion":"AC-2",
		"evidence":"observed exit 1","required_change":"run the check in the run's own tree"}]}`)))
	if err != nil {
		t.Fatalf("a well-formed findings array must parse: %v", err)
	}
	f := e.Outputs.Findings[0]
	if f.Severity != "major" || f.Location != "internal/gate/run.go:88" || f.Required == "" {
		t.Fatalf("the declared fields did not survive parsing: %+v", f)
	}

	round, err := json.Marshal(e.Outputs)
	if err != nil {
		t.Fatalf("marshal outputs: %v", err)
	}
	var back Outputs
	if err := json.Unmarshal(round, &back); err != nil {
		t.Fatalf("unmarshal outputs: %v", err)
	}
	if len(back.Findings) != 1 || back.Findings[0] != f {
		t.Errorf("the finding did not survive the round trip: %+v", back.Findings)
	}
}

// Prose in place of a criterion is refused by shape, not by Go type.
//
// The decoder was decoding straight into map[string]Criterion and returning its
// own error, so a worker that wrote a sentence where an object goes was told
// "cannot unmarshal string into Go struct field .AC-1 of type
// envelope.Criterion" — a Go type it cannot see, naming neither the entry that
// was wrong nor the fields it wanted. Two runs were refused with exactly that
// text and the next one had to guess.
func TestACriterionWrittenAsProseIsRefusedByShapeNotByGoType(t *testing.T) {
	withOutputs := func(body string) string {
		return strings.Replace(minimal, `"outputs": {}`, `"outputs": `+body, 1)
	}

	// Firing case: the tolerated object shape, with prose where a criterion goes.
	_, err := Parse([]byte(withOutputs(`{"criteria": {"AC-1": "I ran the suite"}}`)))
	if err == nil {
		t.Fatal("prose is not a criterion — it cites no command, so it must be refused")
	}
	if _, ok := err.(ErrMalformed); !ok {
		t.Errorf("should be ErrMalformed so it is refusable rather than fatal, got %T", err)
	}
	msg := err.Error()
	for _, want := range []string{"outputs.criteria", "AC-1", "id", "status", "command_index"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must name %q so the next run can fix the shape, got %q", want, msg)
		}
	}
	for _, leak := range []string{"cannot unmarshal", "envelope.Criterion"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the refusal leaks the decoder's own %q, which names a Go type the worker cannot see: %q", leak, msg)
		}
	}

	// Clean case: the message must not become a new refusal. The declared array
	// shape, the tolerated object shape, the key standing in as the id when the
	// value carries none, and all three status words still parse.
	arr, err := Parse([]byte(withOutputs(`{"criteria": [
		{"id":"AC-1","status":"pass","command_index":0,"evidence":"e"},
		{"id":"AC-2","status":"fail","command_index":0,"evidence":"e"},
		{"id":"AC-3","status":"untested","command_index":0,"evidence":"e"}]}`)))
	if err != nil {
		t.Fatalf("the declared array shape must still parse: %v", err)
	}
	obj, err := Parse([]byte(withOutputs(`{"criteria": {
		"AC-1": {"status":"pass","command_index":0,"evidence":"e"},
		"AC-2": {"id":"AC-2","status":"fail","command_index":0,"evidence":"e"},
		"AC-3": {"status":"untested","command_index":0,"evidence":"e"}}}`)))
	if err != nil {
		t.Fatalf("the tolerated object-keyed-by-id shape must still parse: %v", err)
	}
	ids := func(e *Envelope) []string {
		var out []string
		for _, c := range e.Criteria() {
			out = append(out, c.ID+"="+c.Status)
		}
		return out
	}
	want := []string{"AC-1=pass", "AC-2=fail", "AC-3=untested"}
	if got := ids(arr); !slices.Equal(got, want) {
		t.Errorf("array shape read as %v, want %v", got, want)
	}
	// AC-1 and AC-3 carry no id of their own: the key is what supplies it.
	if got := ids(obj); !slices.Equal(got, want) {
		t.Errorf("object shape read as %v, want %v — the key is the id when the value carries none", got, want)
	}
}
