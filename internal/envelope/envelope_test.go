package envelope

import (
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
