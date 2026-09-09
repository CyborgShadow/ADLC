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
