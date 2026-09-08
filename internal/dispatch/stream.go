package dispatch

import (
	"bytes"
	"io"
	"sync"
)

// LineWriter hands each completed line of an agent's output to emit as it
// arrives, so a caller that is showing progress does not wait for the process
// to exit.
//
// It is a plain io.Writer rather than a scanner over a pipe because that is
// what os/exec wants, and because the same writer can be composed with the
// bounded buffer the Result still needs.
type LineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(string)
}

// NewLineWriter returns a writer that calls emit once per line, without the
// newline. A nil emit makes it a sink, so callers need no branch.
func NewLineWriter(emit func(string)) *LineWriter { return &LineWriter{emit: emit} }

// lineCap bounds a single line. An agent that writes a megabyte without a
// newline is not going to be helped by holding all of it in memory first.
const lineCap = 1 << 20

func (w *LineWriter) Write(p []byte) (int, error) {
	if w.emit == nil {
		return len(p), nil
	}
	// Emitted under the lock, on purpose. The consumer is a decoder carrying
	// state across lines, and lines delivered out of order would corrupt it.
	// emit is expected to be a hand-off rather than a write to a socket, which
	// is why the buffer it feeds appends and wakes instead of sending.
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		w.emit(line)
	}
	if len(w.buf) > lineCap {
		w.emit(string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}

// Close flushes a final line that never got its newline.
func (w *LineWriter) Close() error {
	if w.emit == nil {
		return nil
	}
	w.mu.Lock()
	rest := string(w.buf)
	w.buf = nil
	w.mu.Unlock()
	if rest != "" {
		w.emit(rest)
	}
	return nil
}

// TailBuffer keeps the last max bytes written to it and discards the rest.
//
// The Result carries a tail of the agent's output so a failure can say what the
// agent actually said. Under a streaming output format that output is large and
// mostly protocol, and a run that accumulates all of it to throw away the front
// is a memory leak with a good excuse.
type TailBuffer struct {
	b   []byte
	max int
}

func NewTailBuffer(max int) *TailBuffer { return &TailBuffer{max: max} }

func (t *TailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2*t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *TailBuffer) String() string { return tail(string(t.b), t.max) }

var _ io.WriteCloser = (*LineWriter)(nil)
