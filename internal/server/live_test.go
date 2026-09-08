package server

import (
	"net/http"
	"net/http/httptest"
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
		// Once deltas have been seen the whole message is the same text again.
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"Hi there"}]}}`, ""},
		{`{"type":"stream_event","event":{"type":"content_block_start","index":1,` +
			`"content_block":{"type":"tool_use","name":"Read"}}}`, "\n· Read\n"},
		// A tool's arguments arrive as split JSON and are noise, not progress.
		{`{"type":"stream_event","event":{"type":"content_block_delta","index":1,` +
			`"delta":{"type":"input_json_delta","partial_json":"{\"file\":"}}}`, ""},
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

// Without deltas the whole-message events ARE the progress, so they must not be
// suppressed. A decoder that only worked with partial messages on would show
// nothing at all for an agent that does not support them.
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

// A turn that is all tool calls and no prose produces no text deltas. The
// whole-message events that follow it must still be suppressed, or every tool
// call is announced twice — which is what a real turn did.
func TestStreamDecoderSuppressesEchoesWithoutAnyText(t *testing.T) {
	d := &streamDecoder{}
	if got := d.Line(`{"type":"stream_event","event":{"type":"content_block_start","index":0,` +
		`"content_block":{"type":"tool_use","name":"Bash"}}}`); got != "\n· Bash\n" {
		t.Fatalf("got %q", got)
	}
	echo := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`
	if got := d.Line(echo); got != "" {
		t.Fatalf("the same tool call was announced twice: %q", got)
	}
}
