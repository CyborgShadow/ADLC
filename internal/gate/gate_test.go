package gate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CyborgShadow/adlc/internal/config"
	"github.com/CyborgShadow/adlc/internal/envelope"
)

func check(t *testing.T, c config.Check) *config.Check {
	t.Helper()
	cfg := config.Check(c)
	// Route through the real config validator so the test cannot pass against a
	// check the config layer would have rejected.
	full := mustConfig(t, []config.Check{cfg})
	return full.Check(c.ID)
}

func mustConfig(t *testing.T, checks []config.Check) *config.Config {
	t.Helper()
	cfg, err := config.FromChecks("test", []string{"."}, checks)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// TestTheOutputIsTheVerdictWhenTheRuleSaysSo pins the first of the two judging
// rules that are not "exit code zero".
//
// A formatter that lists the files it would rewrite exits 0 whether or not it
// printed any. Reading its exit code is reading a channel that carries no
// information, and a gate built that way is one no tree can fail.
func TestTheOutputIsTheVerdictWhenTheRuleSaysSo(t *testing.T) {
	ch := check(t, config.Check{ID: "fmt", Command: []string{"x"}, Verdict: config.VerdictOutputEmpty})

	if v, _, why := judge(ch, 0, "internal/thing.go\n"); v != StatusRed {
		t.Errorf("exit 0 with output should be RED for an output-judged check, got %s (%s)", v, why)
	}
	// Clean case: the same exit code, no output, must be green — otherwise the
	// rule is not a rule, it is a permanent failure.
	if v, _, why := judge(ch, 0, "  \n"); v != StatusGreen {
		t.Errorf("exit 0 with no output should be GREEN, got %s (%s)", v, why)
	}
}

// TestARunThatDiscoveredNothingIsAFailure pins the second rule. A filter that
// silently matches nothing converts "I ran nothing" into "everything passed",
// which is the one failure mode a selective run must never have.
func TestARunThatDiscoveredNothingIsAFailure(t *testing.T) {
	ch := check(t, config.Check{ID: "test", Command: []string{"x"}, Verdict: config.VerdictGoTestJSON})

	if v, n, why := judge(ch, 0, `{"Action":"start"}`+"\n"); v != StatusRed || n != 0 {
		t.Errorf("zero tests with exit 0 should be RED, got %s n=%d (%s)", v, n, why)
	}
	green := `{"Action":"pass","Test":"TestOne"}` + "\n" + `{"Action":"pass","Test":"TestTwo"}` + "\n"
	if v, n, why := judge(ch, 0, green); v != StatusGreen || n != 2 {
		t.Errorf("two passing tests should be GREEN with n=2, got %s n=%d (%s)", v, n, why)
	}
	red := green + `{"Action":"fail","Test":"TestThree"}` + "\n"
	if v, _, _ := judge(ch, 1, red); v != StatusRed {
		t.Error("a failing test should be RED")
	}
	// A green log with a non-zero process exit is still red: never trust a single
	// channel for a verdict.
	if v, _, _ := judge(ch, 2, green); v != StatusRed {
		t.Error("passing tests under a non-zero exit should still be RED")
	}
}

func TestACountingCheckRefusesAVacuousScan(t *testing.T) {
	ch := check(t, config.Check{
		ID: "cis", Command: []string{"x"}, Verdict: config.VerdictCountMin,
		CountPattern: `(\d+) rules evaluated`, MinCount: 10,
	})
	if v, n, _ := judge(ch, 0, "0 rules evaluated\n"); v != StatusRed || n != 0 {
		t.Errorf("a scan that evaluated nothing must not pass, got %s n=%d", v, n)
	}
	if v, n, _ := judge(ch, 0, "412 rules evaluated\n"); v != StatusGreen || n != 412 {
		t.Errorf("a real scan should pass, got %s n=%d", v, n)
	}
}

func TestAToolThatIsAbsentIsUnknownNotGreen(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{
		ID: "ghost", Command: []string{"adlc-no-such-binary-anywhere"}, Verdict: config.VerdictExitZero,
	}})
	r := &Runner{Cfg: cfg, Dir: t.TempDir()}
	obs := r.runOne(context.Background(), cfg.Check("ghost"))
	if obs.Verdict != StatusUnknown {
		t.Fatalf("an absent tool should be UNKNOWN, got %s", obs.Verdict)
	}
	if !strings.Contains(obs.Why, "NOT passed") {
		t.Errorf("the reason should say plainly that this is not a pass, got %q", obs.Why)
	}
}

func TestABehaviouralCheckWithNoArtifactIsUnknown(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{
		ID: "scan", Kind: config.KindBehavioural, BindsArtifact: true,
		Command: []string{"go", "version"}, Verdict: config.VerdictExitZero,
	}})
	r := &Runner{Cfg: cfg, Dir: t.TempDir()} // no Artifact
	obs := r.runOne(context.Background(), cfg.Check("scan"))
	if obs.Verdict != StatusUnknown {
		t.Fatalf("a behavioural check with nothing to bind to should be UNKNOWN, got %s", obs.Verdict)
	}
	// Clean case: give it an artifact and it runs.
	r2 := &Runner{Cfg: cfg, Dir: t.TempDir(), Artifact: "sha256:abc"}
	if obs2 := r2.runOne(context.Background(), cfg.Check("scan")); obs2.Verdict == StatusUnknown {
		t.Errorf("with an artifact the check should actually run, got UNKNOWN (%s)", obs2.Why)
	}
}

// --- claim matching -------------------------------------------------------

func envWith(t *testing.T, cmds []envelope.Command) *envelope.Envelope {
	t.Helper()
	e, err := envelope.Parse(buildEnvelope(t, cmds))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return e
}

func TestAFabricatedExitCodeIsADiscrepancy(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{ID: "test", Command: []string{"x"}, Verdict: config.VerdictExitZero}})
	res := &Result{Checks: []Observation{{CheckID: "test", Rule: config.VerdictExitZero, Ran: true, ExitCode: 1, Verdict: StatusRed, Why: "exit 1"}}}

	got := res.CompareClaims(cfg, envWith(t, []envelope.Command{{CheckID: "test", Cmd: "go test ./...", ExitCode: 0}}))
	if len(got) != 1 || got[0].Kind != "discrepancy" {
		t.Fatalf("claiming exit 0 over an observed exit 1 must be a discrepancy, got %+v", got)
	}
	// Clean case: an honest matching claim raises nothing.
	honest := res.CompareClaims(cfg, envWith(t, []envelope.Command{{CheckID: "test", Cmd: "go test ./...", ExitCode: 1}}))
	if len(honest) != 0 {
		t.Fatalf("an accurate claim must not be flagged, got %+v", honest)
	}
}

// TestAnHonestNotRunIsNeverADiscrepancy pins a correction that cost real runs.
//
// A matcher that punishes a worker for saying "I could not run this" teaches
// workers to delete the line instead — which is strictly worse, because the
// omission is invisible and the honest report was not.
func TestAnHonestNotRunIsNeverADiscrepancy(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{ID: "race", Command: []string{"x"}, Verdict: config.VerdictExitZero}})
	res := &Result{Checks: []Observation{{CheckID: "race", Rule: config.VerdictExitZero, Ran: true, ExitCode: 0, Verdict: StatusGreen}}}

	got := res.CompareClaims(cfg, envWith(t, []envelope.Command{
		{CheckID: "race", Cmd: "go test -race ./...", NotRun: true, Why: "no C toolchain on this host"},
	}))
	if len(got) != 0 {
		t.Fatalf("an honest not-run declaration must never be flagged, got %+v", got)
	}
}

// TestAClaimIsJudgedInTheChannelTheObservationWas pins the inversion this
// matcher was rewritten to fix: comparing exit codes for an output-judged
// check refused a truthful envelope and admitted a doctored one.
func TestAClaimIsJudgedInTheChannelTheObservationWas(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{ID: "fmt", Command: []string{"x"}, Verdict: config.VerdictOutputEmpty}})
	// The gate saw an unformatted tree: exit 0, but files printed.
	res := &Result{Checks: []Observation{{
		CheckID: "fmt", Rule: config.VerdictOutputEmpty, Ran: true,
		ExitCode: 0, Output: "a.go\n", Verdict: StatusRed, Why: "printed 1 line(s)",
	}}}

	// The truthful envelope reports the same thing: exit 0 AND the output. Under
	// an exit-code comparison this would look like a claim of success and be
	// refused. It must not be.
	truthful := res.CompareClaims(cfg, envWith(t, []envelope.Command{
		{CheckID: "fmt", Cmd: "gofmt -l ./cmd", ExitCode: 0, OutputTail: "a.go\n"},
	}))
	if len(truthful) != 0 {
		t.Fatalf("an envelope that honestly reported the failing output must not be refused, got %+v", truthful)
	}

	// The doctored envelope keeps the exit code and drops the output, which is a
	// claim that the tree was clean. That is the discrepancy.
	doctored := res.CompareClaims(cfg, envWith(t, []envelope.Command{
		{CheckID: "fmt", Cmd: "gofmt -l ./cmd", ExitCode: 0, OutputTail: ""},
	}))
	if len(doctored) != 1 || doctored[0].Kind != "discrepancy" {
		t.Fatalf("trimming the failing output is a discrepancy, got %+v", doctored)
	}
}

func TestSilenceAboutAFailingCheckIsAnOmissionButSilenceAboutAPassingOneIsNot(t *testing.T) {
	cfg := mustConfig(t, []config.Check{
		{ID: "red", Command: []string{"x"}, Verdict: config.VerdictExitZero},
		{ID: "green", Command: []string{"x"}, Verdict: config.VerdictExitZero},
	})
	res := &Result{Checks: []Observation{
		{CheckID: "red", Rule: config.VerdictExitZero, Ran: true, ExitCode: 1, Verdict: StatusRed, Why: "exit 1"},
		{CheckID: "green", Rule: config.VerdictExitZero, Ran: true, ExitCode: 0, Verdict: StatusGreen},
	}}
	got := res.CompareClaims(cfg, envWith(t, nil))
	if len(got) != 1 || got[0].CheckID != "red" || got[0].Kind != "omission" {
		t.Fatalf("only the unmentioned FAILING check is an omission, got %+v", got)
	}
}

func TestAGateWithNoDeclaredChecksIsNotGreen(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{ID: "x", Command: []string{"go", "version"}, Verdict: config.VerdictExitZero}})
	r := &Runner{Cfg: cfg, Dir: t.TempDir()}
	res, err := r.RunEdge(context.Background(), "in_progress", "verifying") // nothing declared for this edge
	if err != nil {
		t.Fatal(err)
	}
	if res.Green() {
		t.Fatal("a gate that ran nothing must not report green")
	}
	if res.Status != StatusUnknown || !res.NoChecksDeclared {
		t.Fatalf("want UNKNOWN with NoChecksDeclared, got %s %v", res.Status, res.NoChecksDeclared)
	}
}

// buildEnvelope renders a minimal valid envelope carrying the given claims.
func buildEnvelope(t *testing.T, cmds []envelope.Command) []byte {
	t.Helper()
	type outputs struct{}
	payload := map[string]any{
		"envelope_version": envelope.Version,
		"run_id":           "p-1",
		"worker_type":      "performer",
		"work_item_id":     "S1-001",
		"verdict":          "pass",
		"commands_run":     cmds,
		"outputs":          outputs{},
	}
	if cmds == nil {
		payload["commands_run"] = []envelope.Command{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
