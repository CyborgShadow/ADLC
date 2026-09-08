package server

import (
	"encoding/json"
	"strings"
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
	// whole-message events that follow describe the SAME work a second time and
	// are dropped — otherwise every reply, and every tool call, appears twice.
	//
	// The flag is set by ANY incremental event, not only by a text delta. A turn
	// that is all tool calls and no prose produces no text deltas at all, and
	// keying off those alone double-printed exactly that turn.
	live bool
	// tool is the block index of a tool call being assembled, so its arguments
	// are not streamed out as raw JSON.
	inTool map[int]bool
}

type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Status  string `json:"status"`
	Event   struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
	} `json:"event"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
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
	if d.inTool == nil {
		d.inTool = map[int]bool{}
	}

	switch e.Type {
	case "stream_event":
		return d.event(e)
	case "assistant":
		if d.live {
			// Already shown as it happened.
			return ""
		}
		var b strings.Builder
		for _, c := range e.Message.Content {
			switch c.Type {
			case "text":
				b.WriteString(c.Text)
			case "tool_use":
				b.WriteString("\n· " + orDefault(c.Name, "tool") + "\n")
			}
		}
		return b.String()
	case "system":
		if e.Subtype == "status" && e.Status == "requesting" {
			return "\n· thinking\n"
		}
		return ""
	}
	// result, rate_limit_event, usage summaries and anything a future version
	// adds: the envelope carries the answer, so silence is correct.
	return ""
}

func (d *streamDecoder) event(e streamLine) string {
	switch e.Event.Type {
	case "content_block_start", "content_block_delta", "content_block_stop":
		d.live = true
	}
	switch e.Event.Type {
	case "content_block_start":
		if e.Event.ContentBlock.Type == "tool_use" {
			d.inTool[e.Event.Index] = true
			return "\n· " + orDefault(e.Event.ContentBlock.Name, "tool") + "\n"
		}
	case "content_block_delta":
		if d.inTool[e.Event.Index] {
			// A tool call's arguments arrive as a JSON string split across
			// deltas. Showing them raw is noise, and showing them half-parsed
			// is worse.
			return ""
		}
		switch e.Event.Delta.Type {
		case "text_delta":
			return e.Event.Delta.Text
		case "thinking_delta":
			return ""
		}
	case "content_block_stop":
		delete(d.inTool, e.Event.Index)
	}
	return ""
}
