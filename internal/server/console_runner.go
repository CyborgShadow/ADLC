package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/CyborgShadow/ADLC/internal/dispatch"
)

// The console runs a conversation, not a job, and those want different things
// from the process underneath them.
//
// A lane's run is deliberately a one-shot: a fresh process, everything it needs
// passed in, nothing carried over. That is what makes a past decision
// re-derivable — a run that depended on state living somewhere else could not
// be replayed at all, and replay is the property the whole record rests on.
//
// A conversation is the opposite shape. Re-sending the transcript as text on
// every turn pays for the whole history again each time, gets slower as it
// grows, and hands the model a *rendering* of the conversation rather than the
// conversation. So the console keeps a session: the agent holds the thread, and
// each turn sends only the new question and the state, which is the part that
// actually changed.
//
// The trade is named rather than hidden. A console turn is NOT re-derivable the
// way a lane's run is, because part of its input lives in the agent's session
// rather than in the ledger. That is acceptable here and would not be for work
// that advances an item: nobody replays a conversation to check whether a
// decision still holds, and the actions a conversation produces are recorded,
// admitted and replayable on their own.

// sessionRunner invokes a conversational agent, keeping one thread across turns.
type sessionRunner struct {
	// Command is argv. Two placeholders are expanded to whole arguments:
	// {{session_args}} becomes the flags that start or resume the thread, and
	// {{prompt}} / {{envelope}} behave as they do for a lane.
	Command []string
	Log     func(string)

	mu      sync.Mutex
	session string
	started bool
}

// newSessionID mints a UUID, which is what the session flags expect.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// sessionArgs are the flags for this turn: start a named thread the first time,
// resume it afterwards.
//
// Resuming is attempted rather than assumed. A session can be gone — the agent
// was upgraded, its store was cleared, the machine changed — and a console that
// died permanently because a thread expired would be worse than one that starts
// a new thread and says so.
func (r *sessionRunner) sessionArgs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == "" {
		id, err := newSessionID()
		if err != nil {
			return nil
		}
		r.session = id
	}
	if !r.started {
		return []string{"--session-id", r.session}
	}
	return []string{"--resume", r.session}
}

func (r *sessionRunner) markStarted() {
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
}

// reset forgets the thread, so the next turn starts a fresh one.
func (r *sessionRunner) reset() {
	r.mu.Lock()
	r.session, r.started = "", false
	r.mu.Unlock()
}

// SessionID is the thread this console is holding, for the record and the page.
func (r *sessionRunner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

func (r *sessionRunner) log(f string, a ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(f, a...))
	}
}

// Invoke runs one turn, retrying once without the session if resuming failed.
func (r *sessionRunner) Invoke(ctx context.Context, in dispatch.Invocation) (dispatch.Result, error) {
	res, err := r.run(ctx, in, r.sessionArgs())
	if err == nil && len(res.Envelope) > 0 {
		r.markStarted()
		return res, nil
	}
	// A resume that failed is worth one clean attempt on a new thread before
	// the turn is reported as failed. Anything else — an authentication
	// problem, a missing binary — will fail the same way twice and is reported
	// with what the agent actually said rather than retried into a loop.
	if r.wasResume() && looksLikeSessionProblem(res.Stderr) {
		r.log("CONSOLE could not resume the previous thread; starting a new one")
		r.reset()
		res, err = r.run(ctx, in, r.sessionArgs())
		if err == nil && len(res.Envelope) > 0 {
			r.markStarted()
		}
	}
	return res, err
}

func (r *sessionRunner) wasResume() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// looksLikeSessionProblem keeps the retry narrow. Retrying an authentication
// failure just spends the same failure twice and hides the real message.
func looksLikeSessionProblem(out string) bool {
	s := strings.ToLower(out)
	for _, probe := range []string{"session", "resume", "conversation not found", "no such"} {
		if strings.Contains(s, probe) {
			return true
		}
	}
	return false
}

func (r *sessionRunner) run(ctx context.Context, in dispatch.Invocation, sess []string) (dispatch.Result, error) {
	if len(r.Command) == 0 {
		return dispatch.Result{}, fmt.Errorf(
			"no console command is configured, so there is nothing to invoke")
	}
	argv := make([]string, 0, len(r.Command)+len(sess))
	for _, a := range r.Command {
		switch a {
		case "{{session_args}}":
			argv = append(argv, sess...)
		default:
			s := a
			s = strings.ReplaceAll(s, "{{prompt}}", in.PromptPath)
			s = strings.ReplaceAll(s, "{{envelope}}", in.EnvelopePath)
			s = strings.ReplaceAll(s, "{{workdir}}", in.WorkDir)
			s = strings.ReplaceAll(s, "{{run_id}}", in.RunID)
			argv = append(argv, s)
		}
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = in.WorkDir
	cmd.Env = append(os.Environ(),
		"ADLC_RUN_ID="+in.RunID,
		"ADLC_WORKER="+in.WorkerType,
		"ADLC_ENVELOPE="+in.EnvelopePath,
		"ADLC_PROMPT="+in.PromptPath,
	)
	// Two consumers of the same output. The tail explains a failure after the
	// fact; the line writer is what somebody is watching while it happens.
	out := dispatch.NewTailBuffer(4000)
	lines := dispatch.NewLineWriter(in.OnOutput)
	var sink io.Writer = out
	if in.OnOutput != nil {
		sink = io.MultiWriter(out, lines)
	}
	cmd.Stdout, cmd.Stderr = sink, sink
	cmd.Stdin = strings.NewReader(in.PromptText)

	err := cmd.Run()
	_ = lines.Close()
	res := dispatch.Result{Stderr: out.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if b, rerr := os.ReadFile(in.EnvelopePath); rerr == nil {
		res.Envelope = b
	}
	if err != nil && len(res.Envelope) == 0 {
		return res, fmt.Errorf("the agent exited %d and wrote no envelope. It said: %s",
			res.ExitCode, firstLines(res.Stderr, 3))
	}
	return res, nil
}

// firstLines surfaces what the agent actually said, because "exit status 1" on
// its own sends somebody to read logs the dashboard already has.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	out := strings.TrimSpace(strings.Join(lines, " · "))
	if out == "" {
		return "nothing at all"
	}
	return out
}

var _ = time.Second
