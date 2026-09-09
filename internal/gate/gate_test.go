package gate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
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

// TestAnHonestNotRunIsNeverADiscrepancy pins the honesty rule. A matcher that
// punishes "I could not run this" teaches workers to delete the line instead,
// which is strictly worse: the omission is invisible and the report was not.
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

// TestAClaimIsJudgedInTheChannelTheObservationWas pins the channel rule.
// Comparing exit codes for an output-judged check refuses a truthful envelope
// and admits a doctored one — the exact inversion the matcher exists to prevent.
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

// TestALineAnchoredCountStillRefusesOutputThatDoesNotCarryIt is the firing case
// for the anchoring fix in internal/config, and the half that matters more.
//
// Widening a match is only safe if the guard it feeds still fires. Output that
// genuinely does not carry the pattern must still count 0 and read RED, or the
// fix for a false refusal has bought a false pass, which is the worse defect.
func TestALineAnchoredCountStillRefusesOutputThatDoesNotCarryIt(t *testing.T) {
	ch := check(t, config.Check{
		ID: "images", Command: []string{"x"}, Verdict: config.VerdictCountMin,
		CountPattern: `^images checked: ([0-9]+)$`, MinCount: 6,
	})

	// Firing: nothing in this output is the line the pattern names — the count is
	// on a line with other text, so the anchors do their job and reject it.
	const noCount = "scanning site/cats\nwarning: images checked: 6 of them were skipped\ndone\n"
	v, n, why := judge(ch, 0, noCount)
	if v != StatusRed || n != 0 {
		t.Fatalf("output not carrying the pattern must be RED with 0, got %s n=%d (%s)", v, n, why)
	}
	if want := "found 0, needs at least 6 — a check that examined nothing has not passed"; why != want {
		t.Errorf("reason %q, want %q", why, want)
	}

	// Clean: the line the pattern names, which before the fix also counted 0.
	if v, n, why := judge(ch, 0, "scanning site/cats\nimages checked: 6\nall rules passed\n"); v != StatusGreen || n != 6 {
		t.Errorf("a scan that reported its count must pass, got %s n=%d (%s)", v, n, why)
	}
}

// TestAMetCountOverAFailedProcessIsStillRed pins the exit code that a wider
// match must not be allowed to carry past. A command that printed its count and
// then failed has not passed; counting is an extra condition on green, never a
// replacement for the exit code.
func TestAMetCountOverAFailedProcessIsStillRed(t *testing.T) {
	ch := check(t, config.Check{
		ID: "images", Command: []string{"x"}, Verdict: config.VerdictCountMin,
		CountPattern: `^images checked: ([0-9]+)$`, MinCount: 6,
	})
	v, n, why := judge(ch, 1, "images checked: 6\n")
	if v != StatusRed || n != 6 {
		t.Fatalf("a met count under exit 1 must be RED, got %s n=%d (%s)", v, n, why)
	}
	if want := "found 6 but the process exited 1"; why != want {
		t.Errorf("reason %q, want %q", why, want)
	}
}

// TestTheGateAndTheClaimMatcherReadOneCounter pins the half of the anchoring
// fix that nothing else guards: the count pattern is compiled once, in
// internal/config, and both the gate's own observation and the claim matcher
// reach it through judge.
//
// The failure this prevents is not a wrong count but an unexplainable one. Give
// the matcher a second way to read a count and the two answers drift apart on
// exactly the inputs the anchoring changed: an envelope quoting precisely what
// the gate saw is refused for a claim_discrepancy that reports two verdicts over
// one output, and nobody reading the refusal can tell which definition is wrong.
func TestTheGateAndTheClaimMatcherReadOneCounter(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{
		ID: "images", Command: []string{"x"}, Verdict: config.VerdictCountMin,
		CountPattern: `^images checked: ([0-9]+)$`, MinCount: 6,
	}})
	const ran = "scanning site/cats\nimages checked: 6\nall rules passed\n"
	res := &Result{Checks: []Observation{{
		CheckID: "images", Rule: config.VerdictCountMin, Ran: true,
		ExitCode: 0, Output: ran, Count: 6, Verdict: StatusGreen, Why: "examined 6",
	}}}

	// Clean: the envelope quotes the anchored line the gate also saw, and the two
	// agree. Agreement on its own would prove little — before the fix both sides
	// counted 0 over this output and agreed on RED — so what is pinned here is
	// agreement on GREEN over a scan that did run.
	truthful := res.CompareClaims(cfg, envWith(t, []envelope.Command{
		{CheckID: "images", Cmd: "sitecheck", ExitCode: 0, OutputTail: ran},
	}))
	if len(truthful) != 0 {
		t.Fatalf("an envelope quoting the output the gate judged GREEN must not be refused, got %+v", truthful)
	}

	// Firing: widening the match must not blind the matcher. The direction that
	// matters is an envelope claiming a scan the gate did not see — output
	// carrying a count over an observation that found none. That is a claim of
	// success nobody earned, and it is still refused.
	//
	// This case used to run the other way round, and asserted that an envelope
	// with NO count disagreeing with a GREEN gate was refused too. That is an
	// agent under-claiming — it ran the check, saw nothing, and said so, while
	// the gate running afterwards over the proposed tree found six. Refusing it
	// cost a run on every item and taught an agent to record only its cleanest
	// attempt, which is the behaviour this matcher exists to catch. The rule
	// pinned here is unchanged: both sides read the same counter.
	empty := &Result{Checks: []Observation{{
		CheckID: "images", Rule: config.VerdictCountMin, Ran: true,
		ExitCode: 0, Output: "scanning site/cats\ndone\n", Count: 0,
		Verdict: StatusRed, Why: "examined 0",
	}}}
	doctored := empty.CompareClaims(cfg, envWith(t, []envelope.Command{
		{CheckID: "images", Cmd: "sitecheck", ExitCode: 0, OutputTail: ran},
	}))
	if len(doctored) != 1 || doctored[0].Kind != "discrepancy" {
		t.Fatalf("an envelope claiming a count the gate never saw must be refused, got %+v", doctored)
	}
}

// A claim that is WORSE than what the gate observed is not a discrepancy.
//
// The matcher exists to catch an agent claiming a success the gate did not see.
// The reverse says the agent ran the check, found a problem, fixed it, and
// recorded what it found — and the gate, running afterwards over the tree being
// proposed, agrees the problem is gone. Refusing that cost a whole run on every
// item and taught an agent to record only its last, cleanest attempt, which is
// exactly the behaviour this matcher is for.
func TestAnAgentIsNotRefusedForClaimingWorseThanTheGateFound(t *testing.T) {
	cfg := &config.Config{Checks: []config.Check{
		{ID: "fmt", Command: []string{"gofmt", "-l", "."}, Verdict: config.VerdictOutputEmpty},
	}}
	green := &Result{Checks: []Observation{{
		CheckID: "fmt", Rule: config.VerdictOutputEmpty, Ran: true,
		Output: "", Verdict: StatusGreen, Why: "printed nothing",
	}}}

	// The agent recorded a run that printed a filename: it found a file it had
	// not formatted, and fixed it.
	worse := &envelope.Envelope{Commands: []envelope.Command{
		{CheckID: "fmt", Cmd: "gofmt -l .", OutputTail: "internal/a.go"},
	}}
	if issues := green.CompareClaims(cfg, worse); len(issues) != 0 {
		t.Fatalf("an agent was refused for reporting a problem it then fixed: %+v", issues)
	}

	// The firing case, and the whole point of the matcher: the agent claims
	// clean and the gate saw otherwise.
	red := &Result{Checks: []Observation{{
		CheckID: "fmt", Rule: config.VerdictOutputEmpty, Ran: true,
		Output: "internal/a.go", Verdict: StatusRed, Why: "printed 1 line(s)",
	}}}
	better := &envelope.Envelope{Commands: []envelope.Command{
		{CheckID: "fmt", Cmd: "gofmt -l .", OutputTail: ""},
	}}
	issues := red.CompareClaims(cfg, better)
	if len(issues) == 0 {
		t.Fatal("an agent claimed a check was clean where the gate saw it fail, and was admitted")
	}
	if issues[0].Kind != "discrepancy" {
		t.Errorf("the wrong kind of issue: %+v", issues[0])
	}
}

// --- timeouts -------------------------------------------------------------

// hangEnv gates the helper below. The timeout tests need a command that
// outlives its budget on every host this suite runs on, and neither `sleep`
// nor `timeout` is one of those; the test binary re-executing itself is.
const hangEnv = "ADLC_GATE_HANG=1"

// TestHangUntilKilled is not a test of anything. It is the long-running
// command the timeout tests execute, and it runs only when re-executed with
// hangEnv set.
func TestHangUntilKilled(t *testing.T) {
	if os.Getenv("ADLC_GATE_HANG") == "" {
		t.Skip("helper process for the timeout tests; not run directly")
	}
	time.Sleep(30 * time.Second)
}

// hangCmd is argv for the helper above.
func hangCmd(t *testing.T) []string {
	t.Helper()
	// cmd.Dir is a temp directory, so a relative os.Args[0] would be resolved
	// against the wrong tree.
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	return []string{self, "-test.run=^TestHangUntilKilled$"}
}

// TestARunOutOfBudgetSaysSoInsteadOfBlamingTheCheck is the firing case.
//
// The check's context is derived from the run's, so an expired run expires it
// too — and the observation then read "timed out after 15m0s" about a check
// that was never given a second of those fifteen minutes. That sends whoever
// reads it hunting a hang that did not happen, and hides the thing that did.
func TestARunOutOfBudgetSaysSoInsteadOfBlamingTheCheck(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{
		ID: "slow", Command: hangCmd(t), Env: []string{hangEnv},
		Verdict: config.VerdictExitZero, TimeoutSeconds: 900,
	}})
	r := &Runner{Cfg: cfg, Dir: t.TempDir()}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	obs := r.runOne(ctx, cfg.Check("slow"))

	if strings.Contains(obs.Why, "timed out after") {
		t.Errorf("a check never given its own budget must not be blamed for a timeout, got %q", obs.Why)
	}
	if !strings.Contains(obs.Why, "the run's budget was exhausted") {
		t.Errorf("the reason should name the run's budget as what ran out, got %q", obs.Why)
	}
	// It did not run, so it cannot have passed, and it did not hang either.
	if obs.Verdict != StatusUnknown {
		t.Errorf("a check that was never given its budget is UNKNOWN, got %s (%s)", obs.Verdict, obs.Why)
	}
	if obs.Ran {
		t.Errorf("the check never started; recording it as ran claims evidence nobody has")
	}
}

// TestACheckThatOutlivesItsOwnBudgetIsStillRed is the clean case for the guard
// above: with the run still inside its budget, a check that hangs past its own
// is reported as the hang it is. A guard that reclassified every timeout would
// pass the firing case and disarm the one rule this package has about hangs.
func TestACheckThatOutlivesItsOwnBudgetIsStillRed(t *testing.T) {
	cfg := mustConfig(t, []config.Check{{
		ID: "slow", Command: hangCmd(t), Env: []string{hangEnv},
		Verdict: config.VerdictExitZero, TimeoutSeconds: 1,
	}})
	r := &Runner{Cfg: cfg, Dir: t.TempDir()}

	obs := r.runOne(context.Background(), cfg.Check("slow"))

	if obs.Verdict != StatusRed {
		t.Errorf("a hang is RED — a check nobody can wait for is one that gets skipped; got %s (%s)", obs.Verdict, obs.Why)
	}
	if obs.Why != "timed out after 1s" {
		t.Errorf("the reason should name the check's own budget, got %q", obs.Why)
	}
	if !obs.TimedOut {
		t.Errorf("timed_out should record that this check ran out of its own time")
	}
}
