package server

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Turning an agent's stdout into progress a person can read.
//
// The control plane has no vendor in it and this file does not put one there.
// A line that is not JSON, or is JSON this decoder does not recognise, is
// passed through unchanged — so an agent that simply prints what it is doing
// streams perfectly well, and the only thing understanding the event format
// buys is that the common case reads like prose instead of like a log.
//
// Nothing here is authoritative. The envelope is still read from a file, for
// the reason it always was: an agent that narrates its reasoning would
// otherwise bury its own result.

// streamDecoder holds the little state the format needs across lines.
type streamDecoder struct {
	// live records that this agent emits incremental events. Once it does, the
	// whole message that follows repeats prose already shown token by token, so
	// only its tool calls are taken from it.
	live bool
}

type streamLine struct {
	Type  string `json:"type"`
	Event struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	} `json:"event"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// Line converts one line of agent output into text to show, which may be empty.
func (d *streamDecoder) Line(line string) string {
	s := strings.TrimRight(line, "\r")
	if strings.TrimSpace(s) == "" {
		return ""
	}
	if !strings.HasPrefix(strings.TrimSpace(s), "{") {
		// Not the event format. An agent that prints plain text, or a runtime
		// printing an error, is exactly what an operator needs to see.
		return s + "\n"
	}
	var e streamLine
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return s + "\n"
	}

	switch e.Type {
	case "stream_event":
		return d.event(e)
	case "assistant":
		// Tool calls are announced from HERE rather than from the incremental
		// content_block_start, because only the completed message carries what
		// the tool was actually called with. "Bash" eleven times in a row tells
		// a person nothing they did not already know; "Bash(go test ./...)" is
		// the difference between watching a run and watching a spinner. The
		// cost is that the line appears when the call is made rather than a
		// moment earlier, which nobody can perceive.
		var b strings.Builder
		for _, c := range e.Message.Content {
			switch c.Type {
			case "text":
				if !d.live {
					b.WriteString(c.Text)
				}
			case "tool_use":
				b.WriteString("\n" + toolLine(c.Name, c.Input) + "\n")
			}
		}
		return b.String()
	}
	// system status, result, rate limits, usage summaries and anything a future
	// version adds. The envelope carries the answer, so silence is correct —
	// and a per-request status event announced "thinking" between every pair of
	// tool calls, which is noise wearing the costume of progress.
	return ""
}

func (d *streamDecoder) event(e streamLine) string {
	switch e.Event.Type {
	case "content_block_start", "content_block_delta", "content_block_stop":
		d.live = true
	}
	if e.Event.Type == "content_block_delta" && e.Event.Delta.Type == "text_delta" {
		// Prose, token by token. This is the part worth streaming.
		return e.Event.Delta.Text
	}
	return ""
}

// toolArgs are the fields worth showing, in the order a person would want them.
// A tool this build has never heard of still gets its first short string, which
// is nearly always the thing it was pointed at.
var toolArgs = []string{
	"command", "file_path", "path", "pattern", "query", "url",
	"description", "prompt", "id", "name",
}

// toolLine renders one tool call as a person would read it.
func toolLine(name string, input json.RawMessage) string {
	if name == "" {
		name = "tool"
	}
	arg := firstArg(input)
	if arg == "" {
		return "· " + name
	}
	return "· " + name + "(" + arg + ")"
}

func firstArg(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	for _, k := range toolArgs {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return oneLine(s, 96)
		}
	}
	// Nothing recognised: take any short string rather than showing a bare name.
	for _, v := range m {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" && len(s) < 120 {
			return oneLine(s, 96)
		}
	}
	return ""
}

// oneLine flattens and shortens an argument. A multi-line heredoc in a shell
// command would otherwise turn one tool call into thirty lines of progress.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > max {
		out = strings.TrimSpace(out[:max]) + "…"
	}
	return out
}
