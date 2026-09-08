package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/dispatch"
)

// A subscriber that arrives late still gets everything written before it
// connected. The alternative is a page that shows the last third of an answer
// because it took a moment to open the connection.
func TestLiveReplaysWhatItMissed(t *testing.T) {
	lt := newLiveTurn()
	lt.write("hello ")
	lt.write("world")
	chunk, next, done, _ := lt.read(0)
	if chunk != "hello world" || next != 11 || done {
		t.Fatalf("got %q next=%d done=%v", chunk, next, done)
	}
	chunk, _, _, _ = lt.read(next)
	if chunk != "" {
		t.Fatalf("a caught-up reader should see nothing, got %q", chunk)
	}
}

// The buffer is bounded, and a reader whose offset fell off the front is moved
// forward rather than handed a slice of the wrong bytes.
func TestLiveDropsTheOldestNotTheNewest(t *testing.T) {
	lt := newLiveTurn()
	lt.write(strings.Repeat("a", liveMax))
	lt.write("TAIL")
	chunk, next, _, _ := lt.read(0)
	if !strings.HasSuffix(chunk, "TAIL") {
		t.Fatal("the end of a run in progress is the interesting end; it was dropped")
	}
	if len(chunk) > liveMax {
		t.Fatalf("buffer grew past its cap: %d", len(chunk))
	}
	if next != liveMax+4 {
		t.Fatalf("offset must count everything ever written, got %d", next)
	}
}

// A write wakes a waiting reader. Without this the endpoint is a poll with
// extra steps.
func TestLiveWakesAReader(t *testing.T) {
	lt := newLiveTurn()
	_, _, _, wait := lt.read(0)
	go lt.write("x")
	select {
	case <-wait:
	case <-time.After(2 * time.Second):
		t.Fatal("a write did not wake the reader")
	}
}

// An unknown turn is answered with done, not left hanging. It is the ordinary
// case: the turn finished while the page was loading, or this process was
// restarted. Either way the page must reload into the ledger rather than wait
// forever for a stream that will never speak.
func TestLiveUnknownTurnEndsImmediately(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.consoleLive(w, httptest.NewRequest(http.MethodGet, "/console/live?turn=nope", nil))
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	if !strings.Contains(w.Body.String(), "event: done") {
		t.Fatalf("body %q", w.Body.String())
	}
}

// Everything the stream carries is agent output, which is not trusted input.
// Encoding it as JSON is what stops a reply containing a newline from forging
// an event boundary.
func TestLiveCannotForgeAnEvent(t *testing.T) {
	s := &Server{}
	lt := s.live.open("t1")
	lt.write("line one\n\nevent: done\ndata: \"\"\n\n")
	s.live.finish("t1")

	w := httptest.NewRecorder()
	s.consoleLive(w, httptest.NewRequest(http.MethodGet, "/console/live?turn=t1", nil))
	body := w.Body.String()
	// The escaped copy inside the payload is fine; what must not exist is a
	// second line the protocol would read AS an event.
	ends := 0
	for _, line := range strings.Split(body, "\n") {
		if line == "event: done" {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("agent output forged an event boundary (%d of them):%s", ends, body)
	}
	if !strings.Contains(body, `"line one\n\nevent: done`) {
		t.Fatalf("payload was not encoded:\n%s", body)
	}
}

// The decoder turns the streaming event format into prose, and passes anything
// else through untouched — a control plane with no vendor in it cannot require
// one particular agent's output format.
func TestStreamDecoder(t *testing.T) {
	d := &streamDecoder{}
	cases := []struct{ in, want string }{
		{`{"type":"stream_event","event":{"type":"content_block_delta","index":0,` +
			`"delta":{"type":"text_delta","text":"Hi"}}}`, "Hi"},
		{`{"type":"stream_event","event":{"type":"content_block_delta","index":0,` +
			`"delta":{"type":"text_delta","text":" there"}}}`, " there"},
		// Once deltas have been seen the whole message repeats the same prose.
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"Hi there"}]}}`, ""},
		// A tool call is announced from the completed message, which is the only
		// place its arguments are whole. The incremental events that carry it in
		// pieces say nothing, because a name on its own is not progress.
		{`{"type":"stream_event","event":{"type":"content_block_start","index":1,` +
			`"content_block":{"type":"tool_use","name":"Read"}}}`, ""},
		{`{"type":"stream_event","event":{"type":"content_block_delta","index":1,` +
			`"delta":{"type":"input_json_delta","partial_json":"{\"file\":"}}}`, ""},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read",` +
			`"input":{"file_path":"internal/server/live.go"}}]}}`,
			"\n· Read(internal/server/live.go)\n"},
		// A per-request status event fires between every pair of tool calls. It
		// is noise wearing the costume of progress.
		{`{"type":"system","subtype":"status","status":"requesting"}`, ""},
		{`{"type":"result","subtype":"success","result":"Hi there"}`, ""},
		{`plain progress from some other agent`, "plain progress from some other agent\n"},
		{`{"not":"an event this build knows"}`, ""},
	}
	for i, c := range cases {
		if got := d.Line(c.in); got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
}

// The argument is what makes a tool call worth showing. Eleven lines reading
// "Bash" tell somebody nothing they did not already know.
func TestStreamDecoderShowsWhatAToolWasCalledWith(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash",` +
			`"input":{"command":"go test ./..."}}]}}`, "\n· Bash(go test ./...)\n"},
		// A heredoc would otherwise turn one call into thirty lines of progress.
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash",` +
			`"input":{"command":"cat <<EOF\nline one\nline two\nEOF"}}]}}`,
			"\n· Bash(cat <<EOF line one line two EOF)\n"},
		// A tool this build has never heard of still gets pointed at something.
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Whatsit",` +
			`"input":{"target":"the thing"}}]}}`, "\n· Whatsit(the thing)\n"},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Nullary",` +
			`"input":{}}]}}`, "\n· Nullary\n"},
	}
	for i, c := range cases {
		if got := (&streamDecoder{}).Line(c.in); got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
}

// Without deltas the whole-message events ARE the progress, so their prose must
// not be suppressed. A decoder that only worked with partial messages on would
// show nothing at all for an agent that does not support them.
func TestStreamDecoderWithoutPartials(t *testing.T) {
	d := &streamDecoder{}
	got := d.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`)
	if got != "done" {
		t.Fatalf("got %q", got)
	}
}

// A configured console.command must be the thing that runs.
//
// This is a regression test for a setting that read as applied and was not:
// every entry point that starts a dashboard also hands it the fleet's one-shot
// runner, and the console preferred that one — so a project that declared a
// conversational command got a fresh, full-price, memoryless turn every time
// and no error anywhere said so.
func TestConsoleCommandBeatsTheFleetRunner(t *testing.T) {
	s := &Server{Cfg: &config.Config{}, Runner: &dispatch.ExecRunner{Command: []string{"echo"}}}
	s.Cfg.Console.Command = []string{"claude", "-p", "{{session_args}}"}
	got, ok := s.runner().(*sessionRunner)
	if !ok {
		t.Fatalf("console.command is declared but %T ran instead — the session is dead config", s.runner())
	}
	if got.SessionID() != "" {
		t.Fatal("a runner that has not been asked for session flags should hold no thread yet")
	}
	// And the fallback still works when nothing conversational is declared.
	s2 := &Server{Cfg: &config.Config{}, Runner: &dispatch.ExecRunner{Command: []string{"echo"}}}
	if _, ok := s2.runner().(*dispatch.ExecRunner); !ok {
		t.Fatalf("with no console.command the supplied runner must be used, got %T", s2.runner())
	}
}

// A tool call is announced exactly once, whichever way it arrives.
//
// There are two events for the same call — the incremental start and the
// completed message — and an earlier version read both, so a turn that was all
// tool work printed every step twice. Only the completed message is read now,
// because only it carries what the tool was called with.
func TestStreamDecoderAnnouncesAToolCallOnce(t *testing.T) {
	d := &streamDecoder{}
	if got := d.Line(`{"type":"stream_event","event":{"type":"content_block_start","index":0,` +
		`"content_block":{"type":"tool_use","name":"Bash"}}}`); got != "" {
		t.Fatalf("the incremental start knows no arguments and should say nothing, got %q", got)
	}
	whole := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash",` +
		`"input":{"command":"go build ./..."}}]}}`
	if got := d.Line(whole); got != "\n· Bash(go build ./...)\n" {
		t.Fatalf("got %q", got)
	}
}

// The two pages that ARE a text box do not reload themselves while idle.
//
// A regression test for a page that threw away a half-written question every
// fifteen seconds. Nothing on Home or the console changes on its own while no
// turn is running, so the refresh bought nothing and cost the thing somebody
// was in the middle of typing.
func TestTypingPagesDoNotReloadThemselvesWhileIdle(t *testing.T) {
	s := newServer(t)
	s.Cfg.Server.RefreshSeconds = 15

	for _, page := range []string{"home", "console"} {
		d, err := s.shell(httptest.NewRequest(http.MethodGet, "/", nil), page, "T", nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Refresh != 0 {
			t.Errorf("%s auto-refreshes every %ds with nothing running; a question being typed is lost", page, d.Refresh)
		}
	}
	// Every other page still refreshes: those are read, not written into.
	d, err := s.shell(httptest.NewRequest(http.MethodGet, "/overview", nil), "overview", "T", nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Refresh != 15 {
		t.Errorf("a page an operator only reads should still refresh, got %d", d.Refresh)
	}
	// And a running turn brings the fast fallback back, on Home too — that is
	// what shows the answer to a browser that cannot stream.
	s.turns.start("t-1", s.now())
	d, err = s.shell(httptest.NewRequest(http.MethodGet, "/", nil), "home", "T", nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Refresh != 2 {
		t.Errorf("a running turn must keep the no-JavaScript fallback alive, got %d", d.Refresh)
	}
}

// The keepalive has to be an event, not an SSE comment.
//
// A comment keeps the connection open but is never delivered to the page, so a
// page watching for a dead stream could not tell one from an agent thinking
// quietly — and reloaded itself, on a healthy turn, roughly every minute.
func TestLiveKeepaliveIsDeliverable(t *testing.T) {
	src, err := os.ReadFile("live.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `": still here`) {
		t.Fatal("the keepalive is an SSE comment again; the page cannot see it")
	}
	if !strings.Contains(string(src), "event: ping") {
		t.Fatal("no ping event: a quiet stream is indistinguishable from a dead one")
	}
	// And the page has to listen for it, or the watchdog fires anyway.
	if !strings.Contains(liveScript, `es.addEventListener("ping"`) {
		t.Fatal("the page does not listen for the ping it is being sent")
	}
}

// A reload makes the browser restore what was in a form's fields, so "the box
// has text in it" is not the same question as "somebody is typing". Getting
// that wrong told a reader the page would not refresh because they were
// part-way through typing something they had never typed.
func TestTypingIsMeasuredAgainstWhatLoaded(t *testing.T) {
	if !strings.Contains(liveScript, "before.push") ||
		!strings.Contains(liveScript, "!== before[i]") {
		t.Fatal("typing() no longer compares against the value the field loaded with")
	}
	// And the boxes say not to restore them in the first place.
	for _, tmpl := range []struct{ name, body string }{
		{"home", homePageHTML}, {"console", consoleHTML},
	} {
		if strings.Contains(tmpl.body, `<textarea name="text"`) &&
			!strings.Contains(tmpl.body, `autocomplete="off"`) {
			t.Errorf("the %s compose box will be refilled by the browser on reload", tmpl.name)
		}
	}
}
