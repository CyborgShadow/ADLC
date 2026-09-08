package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The console's live transport.
//
// Everything else on this dashboard is a form and a meta refresh, and that is a
// constraint rather than a taste: the one surface an operator opens when
// something has gone wrong must not depend on a bundler, a network fetch or a
// browser feature. This file is the single deliberate exception, and it is
// shaped so the constraint still holds.
//
// A console turn is a real agent run — it reads files, thinks, and takes a
// minute. Telling somebody "the answer will appear shortly" for sixty seconds
// is not a progress report; it is indistinguishable from a console that has
// stopped working, which is the exact failure this dashboard exists to make
// visible. So the turn's output is streamed.
//
// The rules that keep it honest:
//
//   - The stream is a VIEW of a run in progress. Nothing here is authoritative.
//     The reply, the actions and the cost are appended to the ledger by
//     runTurn exactly as before, and the page reloads into that record when the
//     turn ends. If this whole file were deleted the console would still work.
//   - The buffer is in memory and bounded, and it is dropped shortly after the
//     turn finishes. An in-flight turn is not a fact about the project.
//   - The meta refresh is still emitted. The script cancels it only after a
//     stream is actually open, so a browser without JavaScript, without
//     EventSource, or unable to reach this endpoint keeps the old behaviour.

// liveMax bounds one turn's buffer. An agent that narrates for ten minutes is
// a possibility, and a dashboard that dies of it is not acceptable; the oldest
// bytes are dropped rather than the newest, because the interesting end of a
// run in progress is the end.
const liveMax = 96 << 10

// liveGrace is how long a finished turn's buffer is kept. Long enough for a
// page that reconnects during the reload to be told "done" rather than
// "unknown"; short enough that nothing accumulates.
const liveGrace = 2 * time.Minute

// liveTurn is one turn's output as it arrives.
type liveTurn struct {
	mu sync.Mutex
	// text is the tail of the output; total is how much has ever been written.
	// Subscribers hold an absolute offset into total, so a subscriber that has
	// fallen behind a truncation is moved forward rather than reading garbage.
	text  []byte
	total int
	done  bool
	// ch is closed on every change and replaced. A subscriber selects on it
	// instead of polling, which is what makes this a stream rather than a
	// faster refresh.
	ch chan struct{}
}

func newLiveTurn() *liveTurn { return &liveTurn{ch: make(chan struct{})} }

func (l *liveTurn) write(s string) {
	if s == "" {
		return
	}
	l.mu.Lock()
	l.text = append(l.text, s...)
	l.total += len(s)
	if len(l.text) > liveMax {
		l.text = l.text[len(l.text)-liveMax:]
	}
	l.wake()
	l.mu.Unlock()
}

func (l *liveTurn) finish() {
	l.mu.Lock()
	l.done = true
	l.wake()
	l.mu.Unlock()
}

// wake is called with the lock held.
func (l *liveTurn) wake() {
	close(l.ch)
	l.ch = make(chan struct{})
}

// read returns everything written since from, the new offset, whether the turn
// has ended, and a channel that closes when there is more.
func (l *liveTurn) read(from int) (chunk string, next int, done bool, wait <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	base := l.total - len(l.text)
	if from < base {
		from = base
	}
	if from < l.total {
		chunk = string(l.text[from-base:])
	}
	return chunk, l.total, l.done, l.ch
}

// liveStore holds the buffers of the turns currently running.
type liveStore struct {
	mu sync.Mutex
	m  map[string]*liveTurn
}

func (s *liveStore) open(id string) *liveTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]*liveTurn{}
	}
	lt := newLiveTurn()
	s.m[id] = lt
	return lt
}

func (s *liveStore) get(id string) *liveTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

func (s *liveStore) write(id, text string) {
	if lt := s.get(id); lt != nil {
		lt.write(text)
	}
}

// finish ends the stream and schedules the buffer's removal.
func (s *liveStore) finish(id string) {
	lt := s.get(id)
	if lt == nil {
		return
	}
	lt.finish()
	time.AfterFunc(liveGrace, func() {
		s.mu.Lock()
		delete(s.m, id)
		s.mu.Unlock()
	})
}

// consoleLive streams one turn's output as server-sent events.
func (s *Server) consoleLive(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "this server cannot stream", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// Proxies are not expected on loopback, but a buffered stream is a stream
	// that arrives all at once at the end, which is the thing being fixed.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	id := strings.TrimSpace(r.URL.Query().Get("turn"))
	lt := s.live.get(id)
	if lt == nil {
		// Not running here: it finished before the page connected, this process
		// was restarted, or the id is nonsense. Say done and let the page reload
		// into the ledger, which was the only authoritative source all along.
		liveDone(w, fl)
		return
	}

	off, ctx := 0, r.Context()
	for {
		chunk, next, done, wait := lt.read(off)
		off = next
		if chunk != "" {
			// JSON-encoded, so the payload is one line by construction and no
			// agent output can forge an event boundary.
			b, err := json.Marshal(chunk)
			if err == nil {
				fmt.Fprintf(w, "event: text\ndata: %s\n\n", b)
			}
		}
		if done {
			liveDone(w, fl)
			return
		}
		fl.Flush()
		select {
		case <-wait:
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
			// A real event, not an SSE comment. A comment keeps the connection
			// open but is never delivered to the page, so a page watching for a
			// dead stream cannot tell one from an agent thinking quietly — and
			// reloaded itself, on a perfectly healthy turn, every minute.
			fmt.Fprint(w, "event: ping\ndata: \"\"\n\n")
		}
	}
}

func liveDone(w http.ResponseWriter, fl http.Flusher) {
	fmt.Fprint(w, "event: done\ndata: \"\"\n\n")
	fl.Flush()
}
