package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CyborgShadow/ADLC/internal/console"
	"github.com/CyborgShadow/ADLC/internal/dispatch"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// The console page.
//
// It is a conversation without any JavaScript, which sounds like a compromise
// and mostly is not. Asking a question POSTs and redirects immediately; the
// turn runs in the background; the page's existing meta-refresh picks up the
// answer when it lands. The cost is that the reply appears on a refresh
// boundary rather than streaming in. The gain is that the one surface an
// operator opens when something has gone wrong still has no build step, no
// bundle, and nothing to fail.
//
// The dashboard is loopback-only and unauthenticated, which is exactly why this
// is safe to add: the console can do whatever the config says it can, and the
// only person who can reach it is the one sitting at the machine.

// turnState tracks a turn while its agent is running. It is in memory on
// purpose: an in-flight turn is not a fact about the project, and writing one
// to the ledger would leave a permanent record of every process that was
// killed halfway.
type turnState struct {
	mu       sync.Mutex
	inFlight map[string]time.Time
	// n makes a turn id unique even when two turns land in the same
	// millisecond. A timestamp alone is not an identity: two turns sharing an
	// id merge into one in the transcript, and the second question then looks
	// as though it was answered by the first reply.
	n int
}

func (t *turnState) nextID(at time.Time) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n++
	return fmt.Sprintf("t-%s-%d", at.UTC().Format("20060102T150405.000"), t.n)
}

func (t *turnState) start(id string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inFlight == nil {
		t.inFlight = map[string]time.Time{}
	}
	t.inFlight[id] = at
}

func (t *turnState) done(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inFlight, id)
}

func (t *turnState) running(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.inFlight[id]
	return ok
}

// since is how long a turn has been running, and any reports whether anything
// is. Both exist so the page can say how long it has been rather than telling
// somebody it will be along shortly for the fourth time.
func (t *turnState) since(id string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	started, ok := t.inFlight[id]
	if !ok {
		return 0
	}
	return now.Sub(started)
}

func (t *turnState) any() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.inFlight) > 0
}

// consoleActor is who the record names for anything the console did on its own.
const consoleActor = "console"

func (s *Server) sessionID() string { return console.SessionID(s.Cfg.Project) }

// runner resolves the thing that invokes an agent. The scheduler already holds
// one; the console borrows it rather than configuring a second, so a project
// with a working fleet has a working console by construction.
func (s *Server) runner() dispatch.Runner {
	// console.command is checked FIRST, ahead of the runner the caller supplied.
	// It is the more specific declaration — somebody wrote a conversational
	// command for the console specifically — and every entry point that starts a
	// dashboard also hands it the fleet's one-shot runner. Preferring that one
	// meant a configured session command was never used by anything, which is a
	// setting that reads as applied and is not.
	//
	// A conversational command gets a runner that holds one thread across turns.
	// Built once and kept: rebuilt per turn, every turn would be turn one.
	if len(s.Cfg.Console.Command) > 0 {
		s.sessOnce.Do(func() {
			s.sess = &sessionRunner{Command: s.Cfg.Console.Command,
				Log: func(m string) {
					if s.Log != nil {
						s.Log(m)
					}
				}}
		})
		return s.sess
	}
	if s.Runner != nil {
		return s.Runner
	}
	if s.Sched != nil && s.Sched.D != nil {
		return s.Sched.D.Runner
	}
	return nil
}

// consoleRow is one turn as the page renders it.
type consoleRow struct {
	ledger.ConsoleTurn
	Running bool
	// Waited is how long this turn has been running, so the page can say what
	// it knows instead of promising it will be along shortly.
	Waited  string
	Asked   string
	Reply   string
	Actions []consoleActionRow
}

type consoleActionRow struct {
	ledger.ConsoleAction
	// Pressable marks an action still waiting for somebody.
	Pressable bool
	// Human is what this action is, in a person's words.
	Human string
	// What is what pressing it actually does, and what it costs if it is wrong.
	What string
	// Fields are its arguments — the thing it will be done TO — linked where
	// they name something with a page, so it can be read before it is approved.
	Fields []actField
	// Verb and Decline label the two buttons with what they do.
	Verb, Decline string
	// Back is where pressing it returns to.
	Back string
}

func (s *Server) consolePage(*http.Request) (string, any, error) {
	sid := s.sessionID()
	turns, err := s.Led.ConsoleHistory(sid, s.Cfg.Console.HistoryTurns)
	if err != nil {
		return "", nil, err
	}
	rows := make([]consoleRow, 0, len(turns))
	pending := 0
	for _, t := range turns {
		r := consoleRow{ConsoleTurn: t, Asked: t.Asked, Reply: t.Reply}
		r.Running = t.Pending() && s.turns.running(t.TurnID)
		if r.Running {
			r.Waited = s.turns.since(t.TurnID, s.now()).Round(time.Second).String()
		}
		for _, a := range t.Actions {
			if a.Outcome == ledger.ActionPending {
				pending++
			}
			r.Actions = append(r.Actions, actionRow(a, "/console"))
		}
		rows = append(rows, r)
	}
	// Reasons the console cannot run, stated up front rather than discovered on
	// the first ask.
	var blocked string
	switch {
	case !s.Cfg.Console.Enabled:
		blocked = "The console is off. Set `console.enabled` to true in the config, and declare a worker holding the `converse` capability."
	case s.runner() == nil:
		blocked = "No agent runner is available, so a turn has nothing to invoke. Start the dashboard with `adlc schedule run`, or configure `dispatch.command`."
	case s.Lib == nil:
		blocked = "No prompt library is loaded, so there is no console prompt to assemble."
	default:
		if _, err := s.Lib.Get(s.consolePrompt()); err != nil {
			blocked = fmt.Sprintf("The console prompt %q is missing from the library: %v", s.consolePrompt(), err)
		}
	}
	return "Console", struct {
		Turns     []consoleRow
		Blocked   string
		Authority string
		AuthWhat  string
		Worker    string
		Pending   int
		Vocab     []console.HumanAction
	}{rows, blocked, string(s.Cfg.Console.Authority), s.Cfg.Console.Authority.Describe(),
		s.Cfg.Console.Worker, pending, console.HumanVocabulary(s.Cfg.Console.Authority)}, nil
}

// consolePrompt is the prompt id the console worker uses.
func (s *Server) consolePrompt() string {
	if w := s.Cfg.Worker(s.Cfg.Console.Worker); w != nil && w.Prompt != "" {
		return w.Prompt
	}
	return "console"
}

// ask records the question and starts the turn.
//
// The ask is appended BEFORE the agent is invoked, and the handler returns
// immediately. Both matter: a question recorded only on success loses every
// question that crashed the turn, and a handler that waits for the agent gives
// the operator a browser hanging on a POST for a minute with no way to tell
// whether anything is happening.
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/console", http.StatusSeeOther)
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	who := strings.TrimSpace(r.FormValue("who"))
	// Back to the page the question was about. An answer that arrives somewhere
	// else is an answer you have to carry back to the thing you were looking at.
	back := safeBack(orDefault(r.FormValue("back"), "/console"))
	if text == "" {
		redirect(w, r, back, "nothing to ask", true)
		return
	}
	// A name is required rather than defaulted. Under `act` the console executes
	// on its own, so the record has to be able to say who asked for it — and a
	// conversation attributed to whatever the process was started as answers
	// that question with the name of a service.
	if who == "" {
		redirect(w, r, back, "say who you are — the record names whoever asked, and a question from nobody cannot be answered for later", true)
		return
	}
	if !s.Cfg.Console.Enabled {
		redirect(w, r, back, "the console is off in this project's config", true)
		return
	}
	run := s.runner()
	if run == nil {
		redirect(w, r, back, "no agent runner is available, so a turn has nothing to invoke", true)
		return
	}
	sid := s.sessionID()
	turnID := s.turns.nextID(s.now())
	if _, err := s.Led.Append(who, ledger.KindConsoleAsked, sid, ledger.ConsoleAsked{
		SessionID: sid, TurnID: turnID, Text: text, AskedBy: who,
	}); err != nil {
		redirect(w, r, back, err.Error(), true)
		return
	}
	s.turns.start(turnID, s.now())
	// Opened HERE, before the redirect, and not inside the goroutine. The page
	// that is about to load connects to this buffer immediately; a buffer that
	// did not exist yet reads as a turn that has already finished, and the page
	// says so — about a turn that has not started.
	s.live.open(turnID)
	go s.runTurn(sid, turnID, run)
	redirect(w, r, back, "asked — the answer appears in the console when the turn finishes", false)
}

// runTurn invokes the agent and records what came back, whatever came back.
//
// Every exit from this function appends a reply. A turn that timed out, crashed
// or produced nothing readable is recorded as a failed turn with the reason,
// because an ask with no reply and no explanation looks exactly like a console
// that quietly stopped working.
func (s *Server) runTurn(sid, turnID string, run dispatch.Runner) {
	// Registered first so it runs LAST. The page reloads when the stream ends,
	// and a reload that arrives before the reply is appended shows an empty
	// answer to a question that was, in fact, answered. The buffer itself was
	// opened by the handler, before this goroutine was scheduled.
	defer s.live.finish(turnID)
	defer s.turns.done(turnID)

	reply := ledger.ConsoleReplied{SessionID: sid, TurnID: turnID}
	finish := func() {
		if _, err := s.Led.Append(consoleActor, ledger.KindConsoleReplied, sid, reply); err != nil && s.Log != nil {
			s.Log(fmt.Sprintf("CONSOLE could not record the reply to %s: %v", turnID, err))
		}
	}
	defer finish()

	text, runID, err := s.consoleInvoke(sid, turnID, run)
	if err != nil {
		reply.Failure = err.Error()
		return
	}
	reply.RunID = runID

	env, err := envelope.Parse(text)
	if err != nil {
		reply.Failure = "the turn wrote an envelope this build cannot read: " + err.Error()
		return
	}
	if u := env.Usage; u != (envelope.Usage{}) {
		reply.Usage = ledger.Usage{
			InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
			CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens,
		}
	}
	t, err := console.Parse(env, turnID, s.Cfg.Console.Authority)
	if err != nil {
		reply.Failure = err.Error()
		return
	}
	reply.Reply = t.Reply
	reply.Actions = append(t.Actions, t.Rejected...)

	// Execute what the configured authority allows. Everything else stays
	// pending and renders as a control.
	for i := range reply.Actions {
		a := &reply.Actions[i]
		if a.Outcome != ledger.ActionPending {
			continue
		}
		if !s.Cfg.Console.Authority.MayExecute(a.Gated) {
			a.Detail = "waiting for you"
			continue
		}
		// Recorded as the console, not as the person who asked. Everything in
		// this system turns on being able to tell what an agent did from what a
		// person did, and the actor column is where that is answered — an action
		// the console took on its own filed under the operator's name is the one
		// thing the record must never say.
		a.Outcome, a.Detail = s.execute(*a, consoleActor, byConsole)
	}
}

// consoleInvoke assembles the prompt, runs the agent, and returns the envelope
// bytes.
func (s *Server) consoleInvoke(sid, turnID string, run dispatch.Runner) ([]byte, string, error) {
	if s.Lib == nil {
		return nil, "", fmt.Errorf("no prompt library is loaded")
	}
	segs, err := s.Led.Segments()
	if err != nil {
		return nil, "", err
	}
	items, err := s.Led.Items("")
	if err != nil {
		return nil, "", err
	}
	questions, err := s.Led.Questions("", true)
	if err != nil {
		return nil, "", err
	}
	approvals, err := s.Led.Approvals("")
	if err != nil {
		return nil, "", err
	}
	var lanes []ledger.LoopHealth
	if s.Sched != nil {
		lanes, _ = s.Sched.Health(s.now())
	}
	history, err := s.Led.ConsoleHistory(sid, s.Cfg.Console.HistoryTurns)
	if err != nil {
		return nil, "", err
	}
	// The turn being answered is the last one, and it is in the history as an
	// unanswered ask. Split it off so the prompt can put the question in front
	// of the agent rather than leaving it as the tail of a transcript.
	question := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].TurnID == turnID {
			question = history[i].Asked
			history = history[:i]
			break
		}
	}

	// With a session, the agent is already holding the conversation; replaying it
	// as text pays for the whole history again and hands the model a second,
	// worse copy of what it already has. Without one, the transcript IS the
	// memory and has to go.
	transcript := console.Transcript(history)
	if len(s.Cfg.Console.Command) > 0 {
		transcript = "You are holding this conversation, so it is not repeated here. " +
			"What follows is only what has changed since your last turn."
	}
	asm, err := s.Lib.Assemble(s.consolePrompt(), map[string]string{
		"project":         s.Cfg.Project,
		"question":        question,
		"state":           console.Brief(s.Cfg, segs, items, questions, approvals, lanes, s.now()),
		"transcript":      transcript,
		"actions":         console.Vocabulary(s.Cfg.Console.Authority),
		"authority":       string(s.Cfg.Console.Authority),
		"authority_means": s.Cfg.Console.Authority.Describe(),
	})
	if err != nil {
		return nil, "", err
	}
	// Retained like any other prompt, so a past turn can be read back with the
	// text it was actually given rather than the text the file holds today.
	if _, err := s.Led.PutBlob(ledger.BlobPrompt, asm.Bytes()); err != nil {
		return nil, "", err
	}

	dir, err := os.MkdirTemp("", "adlc-console-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	promptPath := filepath.Join(dir, "prompt.md")
	envPath := filepath.Join(dir, "envelope.json")
	if err := os.WriteFile(promptPath, []byte(asm.Text), 0o600); err != nil {
		return nil, "", err
	}

	runID := "c-" + strings.TrimPrefix(turnID, "t-")
	if exists, _ := s.Led.RunExists(runID); exists {
		// The clock can be coarse and a session can be restarted. A run id
		// nothing else has taken is what makes the turn attributable at all.
		runID = fmt.Sprintf("%s-%d", runID, s.now().UnixNano()%1000)
	}
	if _, err := s.Led.Append(consoleActor, ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: s.Cfg.Console.Worker, PromptID: asm.PromptID,
		PromptSHA: asm.SHA, WorkDir: s.Repo, Model: s.Cfg.Budget.DefaultModel,
	}); err != nil {
		return nil, "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(s.Cfg.Console.TimeoutSeconds)*time.Second)
	defer cancel()

	dec := &streamDecoder{}

	res, rerr := run.Invoke(ctx, dispatch.Invocation{
		RunID: runID, WorkerType: s.Cfg.Console.Worker,
		PromptPath: promptPath, PromptText: asm.Text,
		WorkDir: s.Repo, EnvelopePath: envPath,
		Timeout: time.Duration(s.Cfg.Console.TimeoutSeconds) * time.Second,
		// OnOutput hands over one line at a time. Decoding happens here rather
		// than inside a runner so that every runner streams the same way, and so
		// that a runner stays a thing that starts a process and reads a file.
		OnOutput: func(line string) { s.live.write(turnID, dec.Line(line)) },
	})
	verdict := "pass"
	if rerr != nil || len(res.Envelope) == 0 {
		verdict = "unknown"
	}
	if _, err := s.Led.Append(consoleActor, ledger.KindRunFinished, runID, ledger.RunFinished{
		RunID: runID, Verdict: verdict,
	}); err != nil {
		return nil, runID, err
	}
	if rerr != nil {
		return nil, runID, fmt.Errorf("the agent did not finish: %w", rerr)
	}
	if len(res.Envelope) == 0 {
		return nil, runID, fmt.Errorf(
			"the agent exited %d and wrote no envelope, so what it did is UNKNOWN rather than failed", res.ExitCode)
	}
	return res.Envelope, runID, nil
}

// consoleDo presses, or declines, one action the console left pending.
func (s *Server) consoleDo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/console", http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("action"))
	press := r.FormValue("press")
	who := orDefault(strings.TrimSpace(r.FormValue("who")), s.Actor)
	back := safeBack(orDefault(r.FormValue("back"), "/console"))
	sid := s.sessionID()

	turn, a, err := s.Led.ConsoleAction(sid, id)
	if err != nil {
		redirect(w, r, back, err.Error(), true)
		return
	}
	if a.Outcome != ledger.ActionPending {
		redirect(w, r, back, fmt.Sprintf(
			"%s was already %s; reload and look at where it stands now", id, a.Outcome), true)
		return
	}
	outcome, detail := ledger.ActionDeclined, "declined by "+who
	if press == "yes" {
		outcome, detail = s.execute(a, who, byPerson)
	}
	if _, err := s.Led.Append(who, ledger.KindConsoleActed, sid, ledger.ConsoleActed{
		SessionID: sid, TurnID: turn.TurnID, ActionID: id, Kind: a.Kind,
		Outcome: outcome, Detail: detail, By: who,
	}); err != nil {
		redirect(w, r, back, err.Error(), true)
		return
	}
	redirect(w, r, back, detail, outcome == ledger.ActionRefused)
}
