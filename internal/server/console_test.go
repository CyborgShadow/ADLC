package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/dispatch"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
)

// stubAgent stands in for the console agent. The console's job is to invoke
// something and be careful with what comes back, so what comes back is the
// thing worth controlling in a test.
type stubAgent struct {
	reply   string
	actions []map[string]any
	body    string // when set, written verbatim instead of a built envelope
	err     error
	calls   int
	lastTxt string
}

func (a *stubAgent) Invoke(_ context.Context, in dispatch.Invocation) (dispatch.Result, error) {
	a.calls++
	a.lastTxt = in.PromptText
	if a.err != nil {
		return dispatch.Result{ExitCode: 1}, a.err
	}
	body := a.body
	if body == "" {
		notes, _ := json.Marshal(map[string]any{"reply": a.reply, "actions": a.actions})
		env, _ := json.Marshal(map[string]any{
			"envelope_version": "1", "run_id": in.RunID, "worker_type": in.WorkerType,
			"verdict": "pass", "summary": a.reply, "commands_run": []any{},
			"outputs": map[string]any{"notes_md": string(notes)},
		})
		body = string(env)
	}
	if err := os.WriteFile(in.EnvelopePath, []byte(body), 0o600); err != nil {
		return dispatch.Result{}, err
	}
	return dispatch.Result{Envelope: []byte(body)}, nil
}

// consoleServer is newServer with the console switched on at a given authority.
func consoleServer(t *testing.T, auth config.ConsoleAuthority, agent *stubAgent) *Server {
	t.Helper()
	s := newServer(t)
	write(t, filepath.Join(s.Cfg.Prompts.Dir, "console.md"),
		"---\nid: console\n---\n# Console\nasked: {{question}}\nstate: {{state}}\nhistory: {{transcript}}\n")
	reloadLib(t, s)
	s.Cfg.Workers = append(s.Cfg.Workers, config.WorkerDecl{
		Type: "console", Layer: "platform", Prompt: "console",
		Capabilities: []string{config.CapConverse},
	})
	s.Cfg.Console = config.ConsolePolicy{
		Enabled: true, Authority: auth, Worker: "console",
		TimeoutSeconds: 30, HistoryTurns: 20,
	}
	s.Runner = agent
	return s
}

// askAndWait drives one whole turn. The handler returns before the agent does,
// which is the point of the design, so a test has to wait for the turn the same
// way the page does.
func askAndWait(t *testing.T, s *Server, text string) []ledger.ConsoleTurn {
	t.Helper()
	if _, loc := post(t, s, "/console/ask", url.Values{"text": {text}, "who": {"brandon"}}); strings.Contains(loc, "bad=1") {
		t.Fatalf("the ask was refused: %s", loc)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		turns, err := s.Led.ConsoleHistory(s.sessionID(), 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(turns) > 0 && turns[len(turns)-1].Replied {
			return turns
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the turn never produced a reply")
	return nil
}

func TestATurnIsRecordedAsAnExchangeEitherWay(t *testing.T) {
	agent := &stubAgent{reply: "Nothing needs you. Two lanes have never fired."}
	s := consoleServer(t, config.ConsoleAct, agent)

	turns := askAndWait(t, s, "what needs me?")
	last := turns[len(turns)-1]
	if last.Asked != "what needs me?" || last.AskedBy != "brandon" {
		t.Fatalf("the question must be recorded verbatim and attributed: %+v", last)
	}
	if last.Reply != agent.reply {
		t.Errorf("the reply should be recorded as written, got %q", last.Reply)
	}
	if agent.calls != 1 {
		t.Errorf("one ask is one turn, got %d invocations", agent.calls)
	}
	// The agent has no memory of its own, so everything it needs has to be in
	// the prompt it was handed.
	for _, want := range []string{"what needs me?", "The fleet right now", "Deliverables"} {
		if !strings.Contains(agent.lastTxt, want) {
			t.Errorf("the prompt did not carry %q, so the agent could not have used it", want)
		}
	}
}

// TestATurnThatFailedIsRecordedAsAFailedTurn is the honesty property. An ask
// with no reply and no explanation is indistinguishable from a console that
// quietly stopped working.
func TestATurnThatFailedIsRecordedAsAFailedTurn(t *testing.T) {
	agent := &stubAgent{err: context.DeadlineExceeded}
	s := consoleServer(t, config.ConsoleAct, agent)

	turns := askAndWait(t, s, "why is everything stopped?")
	last := turns[len(turns)-1]
	if !last.Replied {
		t.Fatal("a failed turn must still close the exchange")
	}
	if last.Failure == "" {
		t.Fatal("the failure must say what went wrong")
	}
	if last.Reply != "" {
		t.Error("a turn that failed must not also present a reply")
	}
}

// TestAuthorityDecidesWhatRunsAndWhatWaits is the whole point of the setting.
func TestAuthorityDecidesWhatRunsAndWhatWaits(t *testing.T) {
	ordinary := map[string]any{
		"kind": "create_deliverable", "summary": "Add the patching deliverable",
		"args": map[string]string{"id": "S9", "title": "Patch the fleet", "brief": "hosts patch themselves"},
	}
	gate := map[string]any{
		"kind": "sign_off", "summary": "Sign off S9",
		"args": map[string]string{"segment": "S9", "to": "roadmap"},
	}

	for _, tc := range []struct {
		auth                   config.ConsoleAuthority
		wantOrdinary, wantGate string
	}{
		{config.ConsolePropose, ledger.ActionPending, ledger.ActionPending},
		{config.ConsoleAct, ledger.ActionExecuted, ledger.ActionPending},
		{config.ConsoleFull, ledger.ActionExecuted, ledger.ActionExecuted},
	} {
		t.Run(string(tc.auth), func(t *testing.T) {
			agent := &stubAgent{reply: "done", actions: []map[string]any{ordinary, gate}}
			s := consoleServer(t, tc.auth, agent)
			turns := askAndWait(t, s, "set up the patching work")
			acts := turns[len(turns)-1].Actions
			if len(acts) != 2 {
				t.Fatalf("both actions must be carried, got %d", len(acts))
			}
			if acts[0].Outcome != tc.wantOrdinary {
				t.Errorf("an ordinary action under %s should be %s, got %s (%s)",
					tc.auth, tc.wantOrdinary, acts[0].Outcome, acts[0].Detail)
			}
			if acts[1].Outcome != tc.wantGate {
				t.Errorf("a gated action under %s should be %s, got %s (%s)",
					tc.auth, tc.wantGate, acts[1].Outcome, acts[1].Detail)
			}
			// Whatever the level, the effect and the record agree.
			seg, err := s.Led.Segment("S9")
			if tc.wantOrdinary == ledger.ActionExecuted {
				if err != nil {
					t.Fatalf("an executed action must have had its effect: %v", err)
				}
				want := "theory"
				if tc.wantGate == ledger.ActionExecuted {
					want = "roadmap"
				}
				if seg.State != want {
					t.Errorf("S9 is %s, want %s", seg.State, want)
				}
			} else if err == nil {
				t.Error("nothing should have been created under propose")
			}
		})
	}
}

// TestAPersonPressingAGateClearsItInTheirName covers the path that makes
// `act` usable: the console drafts, somebody presses, and the record carries
// their name rather than the console's.
func TestAPersonPressingAGateClearsItInTheirName(t *testing.T) {
	agent := &stubAgent{reply: "S9 is ready for you", actions: []map[string]any{
		{"kind": "create_deliverable", "summary": "Add S9",
			"args": map[string]string{"id": "S9", "title": "Patch", "brief": "hosts patch themselves"}},
		{"kind": "sign_off", "summary": "Put S9 on the roadmap",
			"args": map[string]string{"segment": "S9", "to": "roadmap"}},
	}}
	s := consoleServer(t, config.ConsoleAct, agent)
	turns := askAndWait(t, s, "set up the patching work")
	acts := turns[len(turns)-1].Actions

	pending := acts[1]
	if pending.Outcome != ledger.ActionPending || !pending.Gated {
		t.Fatalf("the sign-off should be waiting for a person: %+v", pending)
	}
	if _, loc := post(t, s, "/console/do", url.Values{
		"action": {pending.ID}, "press": {"yes"}, "who": {"brandon"},
	}); strings.Contains(loc, "bad=1") {
		t.Fatalf("a person pressing the control should be allowed: %s", loc)
	}
	seg, err := s.Led.Segment("S9")
	if err != nil || seg.State != "roadmap" {
		t.Fatalf("the gate should have been cleared, got %+v %v", seg, err)
	}
	// Recorded against the person, not the console. An approval an agent
	// granted itself is not an approval, and the record has to be able to tell
	// the two apart.
	evs, err := s.Led.EventsOfKind([]ledger.Kind{ledger.KindSegmentAdvanced}, "S9", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Actor != "brandon" {
		t.Fatalf("the sign-off must carry the person's name, got %+v", evs)
	}
}

// TestTheRecordTellsTheAgentApartFromThePerson is the property everything else
// in this system leans on. An action the console took on its own filed under
// the operator's name is the one thing the record must never say — it is what
// makes "did a person agree to this?" unanswerable afterwards.
func TestTheRecordTellsTheAgentApartFromThePerson(t *testing.T) {
	agent := &stubAgent{reply: "here you go", actions: []map[string]any{
		{"kind": "note", "summary": "Record what I found",
			"args": map[string]string{"subject": "S1", "text": "nothing is in flight"}},
		{"kind": "sign_off", "summary": "Put S1 on the roadmap",
			"args": map[string]string{"segment": "S1", "to": "roadmap"}},
	}}
	s := consoleServer(t, config.ConsoleAct, agent)
	turns := askAndWait(t, s, "have a look and tell me what you see")
	acts := turns[len(turns)-1].Actions

	// The console executed the note itself, so the note carries the console's
	// name — not the name of whoever happened to be typing.
	notes, err := s.Led.EventsOfKind([]ledger.Kind{ledger.KindNoteRecorded}, "S1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("the console should have recorded one note, got %d", len(notes))
	}
	if notes[0].Actor != consoleActor {
		t.Errorf("an action the console took on its own must be attributed to it, got %q", notes[0].Actor)
	}

	// The gate a person pressed carries theirs.
	if _, loc := post(t, s, "/console/do", url.Values{
		"action": {acts[1].ID}, "press": {"yes"}, "who": {"brandon"},
	}); strings.Contains(loc, "bad=1") {
		t.Fatalf("pressing the gate should be allowed: %s", loc)
	}
	adv, err := s.Led.EventsOfKind([]ledger.Kind{ledger.KindSegmentAdvanced}, "S1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(adv) == 0 || adv[len(adv)-1].Actor != "brandon" {
		t.Fatalf("a gate a person pressed must carry their name, got %+v", adv)
	}
	// And the question itself is attributed to whoever asked it.
	if turns[len(turns)-1].AskedBy != "brandon" {
		t.Error("the ask must carry the asker")
	}
}

func TestDecliningAnActionRecordsThatToo(t *testing.T) {
	agent := &stubAgent{reply: "here is one", actions: []map[string]any{
		{"kind": "sign_off", "summary": "Sign off S1",
			"args": map[string]string{"segment": "S1", "to": "roadmap"}},
	}}
	s := consoleServer(t, config.ConsoleAct, agent)
	turns := askAndWait(t, s, "should we start S1?")
	id := turns[len(turns)-1].Actions[0].ID

	post(t, s, "/console/do", url.Values{"action": {id}, "press": {"no"}, "who": {"brandon"}})

	after, err := s.Led.ConsoleHistory(s.sessionID(), 20)
	if err != nil {
		t.Fatal(err)
	}
	got := after[len(after)-1].Actions[0]
	if got.Outcome != ledger.ActionDeclined {
		t.Fatalf("a declined action must be recorded as declined, got %s", got.Outcome)
	}
	if seg, _ := s.Led.Segment("S1"); seg.State == "roadmap" {
		t.Error("declining must not have had the effect anyway")
	}
	// And it cannot be pressed afterwards by a stale page.
	if _, loc := post(t, s, "/console/do", url.Values{"action": {id}, "press": {"yes"}}); !strings.Contains(loc, "bad=1") {
		t.Error("an action that was already decided must not be actionable again")
	}
}

// TestTheConsoleCannotDoWhatTheRestOfTheSystemWouldRefuse is the containment
// property. The console is a way to say what you want, not a way past the rules.
func TestTheConsoleCannotDoWhatTheRestOfTheSystemWouldRefuse(t *testing.T) {
	agent := &stubAgent{reply: "raising it", actions: []map[string]any{
		{"kind": "raise_item", "summary": "Raise an item in an area nobody owns",
			"args": map[string]string{
				"segment": "S1", "id": "S1-900", "title": "Rewrite the kernel",
				"area": "kernel", "criteria": "the kernel is rewritten and boots",
			}},
	}}
	s := consoleServer(t, config.ConsoleAct, agent)
	turns := askAndWait(t, s, "rewrite the kernel please")
	got := turns[len(turns)-1].Actions[0]

	if got.Outcome != ledger.ActionRefused {
		t.Fatalf("an item filed under an unowned area must be refused, got %s", got.Outcome)
	}
	if !strings.Contains(got.Detail, "area") {
		t.Errorf("the refusal should name what was wrong: %q", got.Detail)
	}
	if _, err := s.Led.Item("S1-900"); err == nil {
		t.Error("the refused item must not exist")
	}
	// The refusal lands where every other refusal lands, so one report covers
	// them all.
	props, err := s.Led.Proposals("S1-900", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) == 0 {
		t.Error("a refused proposal must be recorded with the rest of them")
	}
}

func TestAnActionThisBuildDoesNotKnowIsRefusedByName(t *testing.T) {
	agent := &stubAgent{reply: "sure", actions: []map[string]any{
		{"kind": "delete_the_ledger", "summary": "Tidy up", "args": map[string]string{}},
		{"kind": "note", "summary": "", "args": map[string]string{"text": "hi"}},
		{"kind": "rework_item", "summary": "Retry it", "args": map[string]string{"item": "S1-001"}},
	}}
	s := consoleServer(t, config.ConsoleAct, agent)
	turns := askAndWait(t, s, "do some things")
	acts := turns[len(turns)-1].Actions
	if len(acts) != 3 {
		t.Fatalf("every proposed action must be carried, refused ones included, got %d", len(acts))
	}
	for _, a := range acts {
		if a.Outcome != ledger.ActionRefused {
			t.Errorf("%s should have been refused, got %s", a.Kind, a.Outcome)
		}
		if a.Detail == "" {
			t.Errorf("%s was refused with no reason", a.Kind)
		}
	}
}

// TestProseInsteadOfJsonIsStillAnAnswer keeps the failure mode proportionate:
// an agent that wrote a sentence where a JSON object was asked for has still
// answered the question, and refusing that spends a turn to punish shape.
func TestProseInsteadOfJsonIsStillAnAnswer(t *testing.T) {
	agent := &stubAgent{}
	agent.body = `{"envelope_version":"1","run_id":"c-1","worker_type":"console",
		"verdict":"pass","summary":"s","commands_run":[],
		"outputs":{"notes_md":"Two lanes have never fired."}}`
	s := consoleServer(t, config.ConsoleAct, agent)
	turns := askAndWait(t, s, "status?")
	last := turns[len(turns)-1]
	if last.Failure != "" {
		t.Fatalf("prose is an answer, not a failure: %s", last.Failure)
	}
	if !strings.Contains(last.Reply, "never fired") {
		t.Errorf("the prose should be the reply, got %q", last.Reply)
	}
}

func TestTheConsolePageRefusesToPretendItCanRun(t *testing.T) {
	s := newServer(t) // console off, no runner
	code, body := get(t, s, "/console")
	if code != 200 {
		t.Fatalf("the page must still render, got %d", code)
	}
	if !strings.Contains(body, "cannot run") {
		t.Error("a console that cannot run must say so rather than showing an inert box")
	}
	if _, loc := post(t, s, "/console/ask", url.Values{"text": {"hello"}}); !strings.Contains(loc, "bad=1") {
		t.Error("asking a console that is off must be refused")
	}
}

func TestTheTranscriptIsTheAgentsOnlyMemory(t *testing.T) {
	agent := &stubAgent{reply: "first answer"}
	s := consoleServer(t, config.ConsoleAct, agent)
	askAndWait(t, s, "first question")

	agent.reply = "second answer"
	askAndWait(t, s, "second question")

	for _, want := range []string{"first question", "first answer"} {
		if !strings.Contains(agent.lastTxt, want) {
			t.Errorf("the second turn's prompt must carry %q; without it the agent has no memory at all", want)
		}
	}
	if strings.Count(agent.lastTxt, "second question") == 0 {
		t.Error("the question being answered must reach the prompt")
	}
}

func reloadLib(t *testing.T, s *Server) {
	t.Helper()
	lib, err := prompt.Load(s.Cfg.Prompts)
	if err != nil {
		t.Fatal(err)
	}
	s.Lib = lib
}
