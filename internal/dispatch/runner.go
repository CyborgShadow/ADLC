package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecRunner invokes an agent by running a configured command.
//
// The control plane deliberately has no vendor in it. An agent is a process
// that is handed a prompt and a path to write an envelope to; whether that
// process is a coding CLI, a script wrapping an API, or a person running the
// prompt by hand is not this module's business, and keeping it that way is
// what makes the whole thing testable and portable.
//
// The envelope is read from a FILE whose path the runner supplies, not parsed
// out of standard output. An agent that narrates its reasoning would otherwise
// bury its own result, and the parser would end up guessing which JSON object
// in the transcript was the real one.
type ExecRunner struct {
	// Command is argv with {{prompt}}, {{envelope}}, {{workdir}}, {{run_id}},
	// {{worker}} and {{item}} substituted.
	Command []string
	// Env adds variables to the agent's environment.
	Env []string
	// Stream, when set, receives the agent's output as it arrives.
	Stream func(string)
}

// Invoke runs the agent.
func (r *ExecRunner) Invoke(ctx context.Context, in Invocation) (Result, error) {
	if len(r.Command) == 0 {
		return Result{}, fmt.Errorf(
			"no dispatch.command is configured, so there is nothing to invoke — declare the argv that starts an agent, with {{prompt}} and {{envelope}}")
	}
	if err := os.MkdirAll(filepath.Dir(in.EnvelopePath), 0o755); err != nil {
		return Result{}, err
	}
	vars := map[string]string{
		"prompt":   in.PromptPath,
		"envelope": in.EnvelopePath,
		"workdir":  in.WorkDir,
		"run_id":   in.RunID,
		"worker":   in.WorkerType,
		"item":     in.ItemID,
	}
	argv := make([]string, len(r.Command))
	for i, a := range r.Command {
		s := a
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
		}
		argv[i] = s
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = in.WorkDir
	cmd.Env = append(os.Environ(), r.Env...)
	cmd.Env = append(cmd.Env,
		"ADLC_RUN_ID="+in.RunID,
		"ADLC_WORKER="+in.WorkerType,
		"ADLC_ITEM="+in.ItemID,
		"ADLC_ENVELOPE="+in.EnvelopePath,
		"ADLC_PROMPT="+in.PromptPath,
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// The prompt reaches the agent by path AND on stdin, because the two common
	// shapes of coding CLI disagree about which one they read.
	cmd.Stdin = strings.NewReader(in.PromptText)

	err := cmd.Run()
	res := Result{Stderr: tail(out.String(), 4000)}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if r.Stream != nil {
		r.Stream(res.Stderr)
	}
	if b, rerr := os.ReadFile(in.EnvelopePath); rerr == nil {
		res.Envelope = b
	}
	if err != nil && len(res.Envelope) == 0 {
		return res, fmt.Errorf("agent exited %d and wrote no envelope to %s: %w", res.ExitCode, in.EnvelopePath, err)
	}
	return res, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
