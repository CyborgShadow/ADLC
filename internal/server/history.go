package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/spend"
	"github.com/CyborgShadow/ADLC/internal/story"
)

// The ledger view.
//
// Two things were wrong with showing the payload as `string(e.Payload)`.
//
// The first is readability: a row of minified JSON is not a record anybody
// reads, and several payloads carry JSON *inside* a JSON string — a console
// turn's reply, an envelope's outputs — so what reached the page was
// double-encoded and unreadable even by the standards of raw JSON.
//
// The second is worth being precise about, because it is easy to get wrong in
// both directions. Payload text is written by agents, which makes it untrusted
// input. It is NOT an injection risk here — html/template escapes by
// contextual type, and every value below reaches the page through it as a
// plain string, never as HTML, an attribute or a URL. What untrusted text can
// still do is mislead: a reply that contains something looking like a system
// message reads as one if it is rendered indistinguishably from the tool's own
// words. So agent-authored free text is marked as such where it is shown.

// fieldRow is one decoded field of a payload.
type fieldRow struct {
	Key   string
	Value string
	// Nested is set when the value was itself JSON — the console's replies and
	// an agent's outputs both arrive that way, and rendering the escaped string
	// shows backslashes where the structure should be.
	Nested []fieldRow
	// Link is the page that explains this value, when one exists.
	Link string
	// Long marks a value folded behind a disclosure rather than shown inline.
	Long bool
	// Prose marks free text an agent wrote, as opposed to a value the control
	// plane computed.
	Prose bool
}

// proseFields are the payload keys whose contents an agent wrote in its own
// words. Everything else in a payload is either computed here or copied from a
// declaration, and reads as the tool's own voice.
var proseFields = map[string]bool{
	"text": true, "reply": true, "answer": true, "summary": true, "detail": true,
	"why": true, "lean": true, "evidence": true, "note": true, "rationale": true,
	"brief": true, "notes_md": true, "failure": true, "output_tail": true,
	"required_change": true, "title": true,
}

const inlineLimit = 160

// decodePayload turns a payload into readable fields, recursively.
func decodePayload(raw []byte, depth int) []fieldRow {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// Not an object. Shown as it is rather than hidden — a payload this
		// build cannot decode is a fact about the record worth seeing.
		return []fieldRow{{Key: "payload", Value: strings.TrimSpace(string(raw)), Long: len(raw) > inlineLimit}}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]fieldRow, 0, len(keys))
	for _, k := range keys {
		row := decodeField(k, m[k], depth)
		row.Link = linkForField(row.Key, row.Value)
		out = append(out, row)
	}
	return out
}

func decodeField(key string, v any, depth int) fieldRow {
	row := fieldRow{Key: key, Prose: proseFields[key]}
	switch t := v.(type) {
	case nil:
		row.Value = "—"
	case string:
		s := strings.TrimSpace(t)
		// A string that is itself JSON gets decoded rather than shown with its
		// escapes. Bounded by depth so a pathological payload cannot spin.
		if depth < 3 && looksLikeJSON(s) {
			if nested := decodePayload([]byte(s), depth+1); len(nested) > 0 {
				row.Nested = nested
				return row
			}
		}
		row.Value = s
		row.Long = len(s) > inlineLimit || strings.Contains(s, "\n")
	case bool:
		row.Value = strconv.FormatBool(t)
	case float64:
		// JSON numbers are float64. Integers are the overwhelming majority here
		// — sequence numbers, token counts, timestamps — and rendering those as
		// 1.6788e+12 helps nobody.
		if t == float64(int64(t)) {
			row.Value = strconv.FormatInt(int64(t), 10)
		} else {
			row.Value = strconv.FormatFloat(t, 'f', -1, 64)
		}
	case []any:
		if len(t) == 0 {
			row.Value = "none"
			return row
		}
		var parts []string
		simple := true
		for _, e := range t {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
				continue
			}
			simple = false
			break
		}
		if simple {
			row.Value = strings.Join(parts, ", ")
			row.Long = len(row.Value) > inlineLimit
			return row
		}
		for i, e := range t {
			row.Nested = append(row.Nested, decodeField(fmt.Sprintf("[%d]", i), e, depth+1))
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			row.Nested = append(row.Nested, decodeField(k, t[k], depth+1))
		}
		if len(row.Nested) == 0 {
			row.Value = "none"
		}
	default:
		row.Value = fmt.Sprint(t)
	}
	return row
}

func looksLikeJSON(s string) bool {
	if len(s) < 2 {
		return false
	}
	return (s[0] == '{' && s[len(s)-1] == '}') || (s[0] == '[' && s[len(s)-1] == ']')
}

// ledgerRow is one event, ready to render.
type ledgerRow struct {
	ledger.Event
	When  string
	Ago   string
	Known bool
	// Link is where the subject is explained. A record is a graph, and a name
	// you cannot follow is one nobody looks up.
	Link   string
	Fields []fieldRow
	// Says is the one-line human reading of this event, so the table can be
	// skimmed without opening anything.
	Says string
}

// historyView is what both tabs render from.
type historyView struct {
	Tab     string
	Rows    []ledgerRow
	Runs    []historyRun
	Kinds   []kindCount
	Kind    string
	Total   int
	Showing int
}

type kindCount struct {
	Kind string
	N    int
	On   bool
}

func (s *Server) historyPage(r *http.Request) (string, any, error) {
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "runs"
	}
	if tab != "ledger" {
		runs, err := s.Led.Runs("", 120)
		if err != nil {
			return "", nil, err
		}
		out := make([]historyRun, 0, len(runs))
		for _, run := range runs {
			out = append(out, s.historyRunRow(run))
		}
		return "History", historyView{Tab: "runs", Runs: out}, nil
	}

	evs, err := s.Led.Events(1, 0)
	if err != nil {
		return "", nil, err
	}
	view := historyView{Tab: "ledger", Total: len(evs), Kind: r.URL.Query().Get("kind")}

	counts := map[string]int{}
	for _, e := range evs {
		counts[string(e.Kind)]++
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		view.Kinds = append(view.Kinds, kindCount{Kind: k, N: counts[k], On: k == view.Kind})
	}

	// Newest first: the tail is what anybody is looking for.
	for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
		evs[i], evs[j] = evs[j], evs[i]
	}
	const cap = 200
	for _, e := range evs {
		if view.Kind != "" && string(e.Kind) != view.Kind {
			continue
		}
		if len(view.Rows) >= cap {
			break
		}
		view.Rows = append(view.Rows, ledgerRow{
			Event: e,
			When:  time.UnixMilli(e.TsMS).Format("Jan 2 15:04:05"),
			Ago:   time.Since(time.UnixMilli(e.TsMS)).Round(time.Second).String() + " ago",
			Known: ledger.KnownKinds[e.Kind],
			Link:  linkFor(e.Kind, e.Subject),
			Says:  saysWhat(e),
			// Decoded lazily-ish: only the rows actually rendered are decoded,
			// which is why the cap is applied first.
			Fields: decodePayload(e.Payload, 0),
		})
	}
	view.Showing = len(view.Rows)
	return "History", view, nil
}

// saysWhat is the one-line reading of an event.
//
// The kind and subject are already columns; this is the sentence a person would
// write about the row. A ledger that can only be read by decoding JSON is a
// ledger nobody reads, and a record nobody reads stops being a record.
func saysWhat(e ledger.Event) string {
	var m map[string]any
	_ = json.Unmarshal(e.Payload, &m)
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	switch e.Kind {
	case ledger.KindWorkerRegistered:
		return "the role " + e.Subject + " was declared, so a run that never happens can still be counted"
	case ledger.KindSegmentCreated:
		return "the deliverable " + e.Subject + " was written down"
	case ledger.KindSegmentAdvanced:
		return e.Subject + " moved from " + str("from") + " to " + str("to")
	case ledger.KindItemCreated:
		return "work item " + e.Subject + " was created in area " + str("area")
	case ledger.KindItemProposed:
		if ok, _ := m["admitted"].(bool); ok {
			return e.Subject + " was proposed and admitted"
		}
		return e.Subject + " was proposed and refused: " + str("reason")
	case ledger.KindItemTransitioned:
		return e.Subject + " moved from " + str("from") + " to " + str("to")
	case ledger.KindTransitionAdmitted:
		return "the authority admitted " + str("from") + " → " + str("to") + " for " + e.Subject
	case ledger.KindTransitionRefused:
		return "the authority refused " + str("from") + " → " + str("to") + " for " + e.Subject + ": " + str("reason")
	case ledger.KindRunStarted:
		return "run " + e.Subject + " started as " + str("worker_type")
	case ledger.KindRunFinished:
		return "run " + e.Subject + " finished with verdict " + str("verdict")
	case ledger.KindGateObserved:
		return "the control plane ran the checks for " + str("edge") + " and observed " + str("status")
	case ledger.KindQuestionRaised:
		return "a run stopped and asked: " + firstLine(str("text"))
	case ledger.KindQuestionAnswered:
		return e.Subject + " was answered by " + str("answered_by")
	case ledger.KindApprovalRequested:
		return "approval " + e.Subject + " was requested for a " + str("radius") + " change"
	case ledger.KindApprovalDecided:
		return e.Subject + " was " + str("verdict") + "d by " + str("approver")
	case ledger.KindPromptPinned:
		return "the exact bytes of prompt " + e.Subject + " were pinned to this run"
	case ledger.KindLoopTicked:
		return "lane " + e.Subject + " fired"
	case ledger.KindNoteRecorded:
		return "a note was recorded against " + e.Subject
	case ledger.KindConsoleAsked:
		return "somebody asked the console: " + firstLine(str("text"))
	case ledger.KindConsoleReplied:
		if f := str("failure"); f != "" {
			return "the console turn failed: " + firstLine(f)
		}
		return "the console answered"
	case ledger.KindConsoleActed:
		return "an action the console proposed was " + str("outcome") + " by " + str("by")
	case ledger.KindItemAmended:
		return e.Subject + " had its " + str("field") + " corrected"
	case ledger.KindRunActorCorrected:
		return "the actor on run " + e.Subject + " was corrected"
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// historyRunRow builds one row of the runs tab.
//
// A run that started and recorded no end is UNKNOWN rather than failed. Nobody
// knows how it went — the process may have been killed, the host may have gone
// away — and rendering that as a failure invents a fact.
func (s *Server) historyRunRow(r ledger.Run) historyRun {
	h := historyRun{Run: r, Cost: spend.Micros(r.CostMicros).String(),
		When: time.UnixMilli(r.StartedMS).Format("Jan 2 15:04")}
	if st, err := story.OfRun(s.Led, s.Cfg, r.RunID); err == nil {
		h.Headline = st.Headline
	}
	switch {
	case !r.Finished():
		h.Class = "warn"
		if h.Headline == "" {
			h.Headline = "started and never recorded an end"
		}
	case r.Verdict == "pass":
		h.Class = "ok"
	default:
		h.Class = "bad"
	}
	return h
}
