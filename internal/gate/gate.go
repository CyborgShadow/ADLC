// Package gate executes the declared checks and reports what it observed.
//
// It exists because of one defect, and the defect is worth restating every
// time someone is tempted to simplify this package away. A transition
// authority that reads its gate result out of the worker's own envelope is
// checking that the worker CLAIMED a command ran, never that one did. An agent
// that writes a test command with a filter matching nothing, and an exit code
// of zero beside it, satisfies a gate whose entire purpose is "the tests ran
// and were green".
//
// So the control plane runs the commands itself, in the tree the work is in,
// and the exit codes and output it observes are the evidence. The worker's
// account survives only as a declaration to be compared against what was
// observed, and a disagreement is its own recorded refusal.
//
// Three judging rules here are not "exit code zero", and each is a defect this
// package was written to close:
//
//   - Some commands report their verdict in their OUTPUT, and exit 0 either
//     way. Reading the exit code of one of those is reading a channel that
//     carries no information.
//   - A run that discovered zero units of work — zero tests, zero hosts, zero
//     rules evaluated — is a FAILURE, not a pass. A filter that silently
//     matches nothing must never report green, because that converts "I ran
//     nothing" into "everything passed".
//   - "I could not tell" is not a pass. A missing tool is UNKNOWN, a hang is
//     RED, and neither one satisfies anything.
package gate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
)

// Status is a verdict. There are three, and the third is load-bearing.
type Status string

const (
	// StatusGreen means the check ran and passed.
	StatusGreen Status = "GREEN"
	// StatusRed means the check ran and failed, or hung, or could not be judged.
	// A hang is a failure and not a wait.
	StatusRed Status = "RED"
	// StatusUnknown means the check did not run — the tool is absent, the
	// directory is missing, the host did not answer. It is not a pass, it is not
	// a failure of the thing being checked, and it never satisfies a gate.
	StatusUnknown Status = "UNKNOWN"
)

// Observation is what the control plane saw for one declared check.
type Observation struct {
	CheckID    string             `json:"check_id"`
	Kind       config.Kind        `json:"kind"`
	Cmd        string             `json:"cmd"`
	Rule       config.VerdictRule `json:"rule"`
	Ran        bool               `json:"ran"`
	ExitCode   int                `json:"exit_code"`
	TimedOut   bool               `json:"timed_out"`
	Output     string             `json:"output_tail"`
	Count      int                `json:"count,omitempty"`
	Verdict    Status             `json:"verdict"`
	Why        string             `json:"why"`
	DurationMS int64              `json:"duration_ms"`
}

// Result is a whole gate run.
type Result struct {
	Edge       string        `json:"edge"`
	Dir        string        `json:"dir"`
	TreeSHA    string        `json:"tree_sha"`
	Dirty      bool          `json:"dirty"`
	DirtyPaths []string      `json:"dirty_paths,omitempty"`
	Artifact   string        `json:"artifact,omitempty"`
	Status     Status        `json:"status"`
	Checks     []Observation `json:"checks"`
	// NoChecksDeclared distinguishes a green gate from a gate that had nothing to
	// run. They are not the same fact and must not render the same.
	NoChecksDeclared bool `json:"no_checks_declared"`
}

// Green reports whether the gate is satisfied. UNKNOWN is not satisfied.
func (r *Result) Green() bool { return r.Status == StatusGreen && !r.NoChecksDeclared }

// Failed names the checks that came back RED.
func (r *Result) Failed() []Observation { return r.byVerdict(StatusRed) }

// Unknown names the checks that could not be run.
func (r *Result) Unknown() []Observation { return r.byVerdict(StatusUnknown) }

func (r *Result) byVerdict(v Status) []Observation {
	var out []Observation
	for _, c := range r.Checks {
		if c.Verdict == v {
			out = append(out, c)
		}
	}
	return out
}

// Summary is a one-line rendering for a refusal message.
func (r *Result) Summary() string {
	if r.NoChecksDeclared {
		return "no checks are declared for this edge, so the gate observed nothing — which is not a pass"
	}
	var g, red, unk int
	for _, c := range r.Checks {
		switch c.Verdict {
		case StatusGreen:
			g++
		case StatusRed:
			red++
		case StatusUnknown:
			unk++
		}
	}
	parts := []string{fmt.Sprintf("%d green", g)}
	if red > 0 {
		parts = append(parts, fmt.Sprintf("%d red", red))
	}
	if unk > 0 {
		parts = append(parts, fmt.Sprintf("%d could not run", unk))
	}
	return strings.Join(parts, ", ")
}

// JSON renders the observations for the ledger.
func (r *Result) JSON() string {
	b, err := json.Marshal(r.Checks)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// Runner executes declared checks.
type Runner struct {
	Cfg *config.Config
	// Dir is the tree the checks run in. It is the run's own isolated workspace,
	// not the shared trunk.
	Dir string
	// Artifact is the digest of what a behavioural check runs against.
	Artifact string
	// Vars are substituted into check commands as {{name}}.
	Vars map[string]string
}

// RunEdge executes every check the config declares for a lifecycle edge.
func (r *Runner) RunEdge(ctx context.Context, from, to string) (*Result, error) {
	checks := r.Cfg.ChecksForEdge(from, to)
	res := &Result{Edge: from + "->" + to, Dir: r.Dir, Artifact: r.Artifact}

	tree, dirty, paths, err := TreeState(r.Dir, r.Cfg.SourceRoots)
	if err != nil {
		// Not being in a repository is a fact about the workspace, not a failure of
		// the work, so it is recorded rather than fatal.
		res.TreeSHA = ""
	} else {
		res.TreeSHA, res.Dirty, res.DirtyPaths = tree, dirty, paths
	}

	if len(checks) == 0 {
		res.NoChecksDeclared = true
		res.Status = StatusUnknown
		return res, nil
	}
	for _, ch := range checks {
		res.Checks = append(res.Checks, r.runOne(ctx, ch))
	}
	res.Status = rollUp(res.Checks)
	return res, nil
}

// RunAll executes every declared check, regardless of edge. Used by the
// standalone `adlc gate run`, which exists so that a person can ask the same
// question the authority asks without proposing anything.
func (r *Runner) RunAll(ctx context.Context) (*Result, error) {
	res := &Result{Edge: "*", Dir: r.Dir, Artifact: r.Artifact}
	tree, dirty, paths, err := TreeState(r.Dir, r.Cfg.SourceRoots)
	if err == nil {
		res.TreeSHA, res.Dirty, res.DirtyPaths = tree, dirty, paths
	}
	if len(r.Cfg.Checks) == 0 {
		res.NoChecksDeclared = true
		res.Status = StatusUnknown
		return res, nil
	}
	for i := range r.Cfg.Checks {
		res.Checks = append(res.Checks, r.runOne(ctx, &r.Cfg.Checks[i]))
	}
	res.Status = rollUp(res.Checks)
	return res, nil
}

func rollUp(obs []Observation) Status {
	status := StatusGreen
	for _, c := range obs {
		switch c.Verdict {
		case StatusRed:
			return StatusRed
		case StatusUnknown:
			status = StatusUnknown
		}
	}
	return status
}

func (r *Runner) runOne(ctx context.Context, ch *config.Check) Observation {
	argv := make([]string, len(ch.Command))
	for i, a := range ch.Command {
		argv[i] = substitute(a, r.Vars)
	}
	obs := Observation{
		CheckID: ch.ID, Kind: ch.Kind, Rule: ch.Verdict,
		Cmd: strings.Join(argv, " "),
	}
	if ch.Kind == config.KindBehavioural && r.Artifact == "" {
		obs.Verdict = StatusUnknown
		obs.Why = "behavioural check has no artifact digest to bind its result to; its output would describe something nobody can identify later"
		return obs
	}

	dir := r.Dir
	if ch.Dir != "" {
		dir = substitute(ch.Dir, r.Vars)
	}
	cctx, cancel := context.WithTimeout(ctx, ch.Timeout())
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if len(ch.Env) > 0 {
		cmd.Env = append(cmd.Environ(), ch.Env...)
	}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	obs.DurationMS = time.Since(start).Milliseconds()
	obs.Output = tail(buf.String(), 8000)

	switch {
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		// A hang is a failure, not a wait. A check nobody can wait for is a check
		// that will be skipped, and a skipped check is an absent one.
		obs.Ran, obs.TimedOut = true, true
		obs.Verdict, obs.Why = StatusRed, fmt.Sprintf("timed out after %s", ch.Timeout())
		return obs
	case isNotFound(err):
		// A tool that is absent must fail loudly and never exit 0. A bare name on
		// PATH can resolve to a stub that exits 0 without doing anything, silently
		// disarming a gate while every log line reads green.
		obs.Verdict = StatusUnknown
		obs.Why = fmt.Sprintf("%s could not be executed here — check not run, NOT passed", argv[0])
		return obs
	}
	obs.Ran = true
	obs.ExitCode = exitCodeOf(err, cmd)
	obs.Verdict, obs.Count, obs.Why = judge(ch, obs.ExitCode, buf.String())
	return obs
}

// judge applies a check's declared verdict rule. It is the single definition
// of what a check's result means, and the claim matcher calls it too: two
// definitions of one verdict is exactly how an envelope that told the truth
// came to be refused while one that deleted the line was admitted.
func judge(ch *config.Check, exitCode int, output string) (Status, int, string) {
	trimmed := strings.TrimSpace(output)
	switch ch.Verdict {
	case config.VerdictExitZero:
		if exitCode == 0 {
			return StatusGreen, 0, "exit 0"
		}
		return StatusRed, 0, fmt.Sprintf("exit %d", exitCode)

	case config.VerdictExitIn:
		for _, ok := range ch.AllowedExits {
			if exitCode == ok {
				return StatusGreen, 0, fmt.Sprintf("exit %d is allowed", exitCode)
			}
		}
		return StatusRed, 0, fmt.Sprintf("exit %d is not in %v", exitCode, ch.AllowedExits)

	case config.VerdictOutputEmpty:
		if trimmed == "" {
			return StatusGreen, 0, "printed nothing"
		}
		return StatusRed, 0, fmt.Sprintf("printed %d line(s); for this check the output is the verdict and the exit code says nothing", lines(trimmed))

	case config.VerdictOutputNonEmpty:
		if trimmed != "" {
			return StatusGreen, 0, "printed output"
		}
		return StatusRed, 0, "printed nothing"

	case config.VerdictOutputMatches:
		if ch.Pattern().MatchString(output) {
			return StatusGreen, 0, "output matched " + ch.ExpectPattern
		}
		return StatusRed, 0, "output did not match " + ch.ExpectPattern

	case config.VerdictOutputNotMatch:
		if !ch.Pattern().MatchString(output) {
			return StatusGreen, 0, "output did not match " + ch.ExpectPattern
		}
		return StatusRed, 0, "output matched the forbidden pattern " + ch.ExpectPattern

	case config.VerdictGoTestJSON:
		n, failed := countGoTests(output)
		switch {
		case n == 0:
			return StatusRed, 0, "zero tests reported a result — a run that discovered nothing is a failure, not a pass"
		case failed > 0:
			return StatusRed, n, fmt.Sprintf("%d of %d tests failed", failed, n)
		case exitCode != 0:
			return StatusRed, n, fmt.Sprintf("%d tests passed but the process exited %d", n, exitCode)
		}
		return StatusGreen, n, fmt.Sprintf("%d tests ran and passed", n)

	case config.VerdictCountMin:
		n := countMatches(ch, output)
		if n < ch.MinCount {
			return StatusRed, n, fmt.Sprintf("found %d, needs at least %d — a check that examined nothing has not passed", n, ch.MinCount)
		}
		if exitCode != 0 {
			return StatusRed, n, fmt.Sprintf("found %d but the process exited %d", n, exitCode)
		}
		return StatusGreen, n, fmt.Sprintf("examined %d", n)
	}
	return StatusUnknown, 0, "no verdict rule"
}

func countGoTests(output string) (total, failed int) {
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "pass", "skip":
			total++
		case "fail":
			total++
			failed++
		}
	}
	return total, failed
}

func countMatches(ch *config.Check, output string) int {
	best := 0
	for _, m := range ch.Counter().FindAllStringSubmatch(output, -1) {
		if len(m) < 2 {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil && n > best {
			best = n
		}
	}
	return best
}

// ---------------------------------------------------------- claim matching

// ClaimIssue is a disagreement between what a worker said and what the control
// plane saw.
type ClaimIssue struct {
	CheckID string
	Kind    string // "discrepancy" | "omission"
	Detail  string
}

// CompareClaims judges the worker's account of the checks against the
// observations.
//
// A claim is compared in the SAME CHANNEL the observation was judged in. For a
// check whose verdict is its output, comparing exit codes reads a channel that
// carries no information, and produces the exact inversion the matcher exists
// to prevent — a truthful envelope refused, a doctored one admitted.
//
// A command a worker honestly reports it could NOT run is never a discrepancy.
// A matcher that punishes that honesty teaches workers to delete the line
// instead, which is strictly worse: the omission is invisible and the honest
// report was not.
func (r *Result) CompareClaims(cfg *config.Config, env *envelope.Envelope) []ClaimIssue {
	var issues []ClaimIssue
	for _, obs := range r.Checks {
		claim, declared := env.Claim(obs.CheckID)
		if !declared {
			// Silence about a check that passed is not dishonesty. Silence about one
			// that failed is the omission the matcher is for.
			if obs.Verdict == StatusRed {
				issues = append(issues, ClaimIssue{
					CheckID: obs.CheckID, Kind: "omission",
					Detail: fmt.Sprintf("the gate ran %q here and it came back RED (%s), and the envelope does not mention it",
						obs.Cmd, obs.Why),
				})
			}
			continue
		}
		if claim.NotRun {
			continue
		}
		ch := cfg.Check(obs.CheckID)
		if ch == nil {
			continue
		}
		if ch.Verdict.TurnsOnExitCode() {
			// Only a claim that is BETTER than the observation is a
			// discrepancy. See below: an agent reporting worse than the gate
			// found is an agent that fixed something, not one that lied.
			if claim.ExitCode != obs.ExitCode && claim.ExitCode == 0 {
				issues = append(issues, ClaimIssue{
					CheckID: obs.CheckID, Kind: "discrepancy",
					Detail: fmt.Sprintf("the envelope reports %q exit_code %d, but the gate ran it here and it exited %d",
						claim.Cmd, claim.ExitCode, obs.ExitCode),
				})
			}
			continue
		}
		claimed, _, why := judge(ch, claim.ExitCode, claim.OutputTail)
		// A claim that is worse than what the gate observed is not dishonesty.
		//
		// The matcher exists to catch an agent claiming success the gate did not
		// see. The reverse says the agent ran the check, found a problem, fixed
		// it, and recorded what it found — and the gate, running afterwards over
		// the tree being proposed, agrees the problem is gone. Refusing that
		// costs a whole run per item and teaches an agent to record only its
		// last, cleanest attempt, which is the behaviour this matcher is for.
		//
		// The gate is the authority either way: it ran the command itself, here,
		// over this tree. Nothing is admitted on the strength of the claim.
		if claimed != obs.Verdict && !worseThan(claimed, obs.Verdict) {
			issues = append(issues, ClaimIssue{
				CheckID: obs.CheckID, Kind: "discrepancy",
				Detail: fmt.Sprintf("for %s the verdict is the output, not the exit code: the envelope's own output reads %s (%s), the gate observed %s (%s)",
					obs.CheckID, claimed, why, obs.Verdict, obs.Why),
			})
		}
	}
	return issues
}

// worseThan reports whether the first verdict is a less favourable claim than
// the second. RED is worse than UNKNOWN, and UNKNOWN is worse than GREEN.
func worseThan(claimed, observed Status) bool {
	rank := func(s Status) int {
		switch s {
		case StatusRed:
			return 0
		case StatusUnknown:
			return 1
		}
		return 2
	}
	return rank(claimed) < rank(observed)
}

// ---------------------------------------------------------------- helpers

func substitute(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func exitCodeOf(err error, cmd *exec.Cmd) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	return -1
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found")
}

func lines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// RunCriteria executes an item's acceptance criteria and reports what it saw.
//
// The same executor, the same verdict rules and the same observation shape the
// declared checks get, because "did this criterion hold" and "did this check
// pass" are the same question asked about different things — and answering them
// through two code paths is how the two answers drift.
//
// This is what makes an acceptance criterion evidence rather than a report. It
// was the only verification in the system settled by asking an agent to run a
// command and believing its account; every other one the control plane runs
// itself, in the run's own tree, and compares the claim afterwards.
func (r *Runner) RunCriteria(ctx context.Context, cs []config.Check) *Result {
	res := &Result{Dir: r.Dir, Edge: "acceptance", Status: StatusGreen}
	if len(cs) == 0 {
		// No executable criterion is not a pass. An item whose specification
		// nobody could run has not been verified, and saying GREEN about it
		// would be the exact failure the gate exists to prevent.
		res.NoChecksDeclared = true
		res.Status = StatusUnknown
		return res
	}
	for i := range cs {
		obs := r.runOne(ctx, &cs[i])
		res.Checks = append(res.Checks, obs)
		switch obs.Verdict {
		case StatusRed:
			res.Status = StatusRed
		case StatusUnknown:
			if res.Status != StatusRed {
				res.Status = StatusUnknown
			}
		}
	}
	return res
}
