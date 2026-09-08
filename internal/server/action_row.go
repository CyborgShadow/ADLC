package server

import (
	"strings"

	"github.com/CyborgShadow/ADLC/internal/console"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// One console action, as a person has to be able to read it.
//
// The control the console leaves behind is a decision somebody makes on the
// strength of what is written next to it. An earlier version showed a one-line
// summary and two unlabelled buttons — press one and something irreversible
// happens to a thing that was never named on screen. That is not a decision, it
// is a guess, and the record would have said the person made it.
//
// So a pressable action states four things: what kind of thing it is, what
// pressing it actually does, exactly what it will do it to (linked, so it can
// be read first), and what each button means.

// actField is one argument of an action, linked where the value names something
// with a page.
type actField struct {
	Label string
	Value string
	Href  string
}

// actLabels give an argument a name somebody recognises. The key is what the
// agent writes; this is what a person reads.
var actLabels = map[string]string{
	"id":           "which",
	"item_id":      "work item",
	"segment_id":   "deliverable",
	"work_item_id": "work item",
	"title":        "title",
	"brief":        "brief",
	"area":         "area",
	"reason":       "reason",
	"why":          "why",
	"answer":       "answer",
	"verdict":      "verdict",
	"note":         "note",
	"loop":         "lane",
	"every":        "every (s)",
	"enabled":      "enabled",
	"criteria":     "acceptance criteria",
	"radius":       "blast radius",
}

// actFieldOrder puts the subject first. An action whose arguments render in map
// order shows the reason before the thing it is about.
var actFieldOrder = []string{
	"id", "segment_id", "item_id", "work_item_id", "title", "area", "brief",
	"criteria", "verdict", "answer", "reason", "why", "note", "loop", "every",
	"enabled", "radius",
}

// actionRow assembles what the page needs to render one action.
func actionRow(a ledger.ConsoleAction, back string) consoleActionRow {
	k := console.ActionKind(a.Kind)
	r := consoleActionRow{
		ConsoleAction: a,
		Pressable:     a.Outcome == ledger.ActionPending,
		Human:         k.Human(),
		What:          k.Detail(),
		Back:          back,
		Fields:        actFields(a),
	}
	r.Verb, r.Decline = actVerbs(k, subjectOf(a))
	return r
}

// actFields renders the arguments, subject first, linked where they name
// something with a page.
func actFields(a ledger.ConsoleAction) []actField {
	seen := map[string]bool{}
	out := make([]actField, 0, len(a.Args))
	add := func(k, v string) {
		if seen[k] || strings.TrimSpace(v) == "" {
			return
		}
		seen[k] = true
		label := actLabels[k]
		if label == "" {
			label = strings.ReplaceAll(k, "_", " ")
		}
		out = append(out, actField{Label: label, Value: v, Href: linkForField(k, v)})
	}
	for _, k := range actFieldOrder {
		if v, ok := a.Args[k]; ok {
			add(k, v)
		}
	}
	// Anything the agent sent that this build has no opinion about is still
	// shown. An argument hidden because it was unrecognised is an argument
	// somebody approved without seeing.
	for k, v := range a.Args {
		add(k, v)
	}
	return out
}

// subjectOf is the identifier an action is about, for the button label.
func subjectOf(a ledger.ConsoleAction) string {
	for _, k := range []string{"id", "item_id", "work_item_id", "segment_id", "loop"} {
		if v := strings.TrimSpace(a.Args[k]); v != "" && !strings.ContainsAny(v, " \t\n") {
			return v
		}
	}
	return ""
}

// actVerbs label the two buttons with what they do. "Do it" and "No thanks" are
// the same two words whether the action writes a note or signs off a plan, and
// the button is the last thing somebody reads before it happens.
func actVerbs(k console.ActionKind, subject string) (yes, no string) {
	on := ""
	if subject != "" {
		on = " " + subject
	}
	switch k {
	case console.ActSignOff:
		return "Sign off" + on, "Not yet"
	case console.ActDecideApproval:
		return "Approve" + on, "Refuse"
	case console.ActAnswerQuestion:
		return "Send this answer", "Not this answer"
	case console.ActCreateDeliverable:
		return "Add it to the roadmap", "Don't add it"
	case console.ActRaiseItem:
		return "Raise" + on, "Don't raise it"
	case console.ActReworkItem:
		return "Send" + on + " back", "Leave it"
	case console.ActCancelItem:
		return "Withdraw" + on, "Keep it"
	case console.ActSetLane:
		return "Change" + on, "Leave it as it is"
	case console.ActNote:
		return "Record the note", "Don't record it"
	}
	return "Do it", "No thanks"
}

// actionCSS and actionHTML are the one rendering of a console action.
//
// It was three: the Home page, the console page and the panel each drew their
// own, and they had already drifted — one of them showed the arguments, none of
// them showed what pressing the button would do, and all three labelled it
// "Do it".
const actionCSS = `
.act2{border-left:2px solid var(--warn);background:#131a1e;border-radius:0 4px 4px 0;
padding:10px 13px;margin-top:10px}
.act2.done2{border-left-color:var(--ok)}
.act2.no2{border-left-color:var(--line);opacity:.72}
.act2 .sum{font-weight:600;display:block;margin-bottom:3px}
.act2 .kind{color:var(--dim);font-size:12px;text-transform:uppercase;letter-spacing:.05em}
.act2 .does{color:var(--dim);font-size:12.5px;margin:5px 0 0;line-height:1.5}
.act2 dl{display:grid;grid-template-columns:auto 1fr;gap:3px 12px;margin:9px 0 0;font-size:12.5px}
.act2 dt{color:var(--dim);white-space:nowrap}
.act2 dd{margin:0;word-break:break-word}
.act2 form{display:flex;gap:7px;margin-top:10px;flex-wrap:wrap;align-items:center}
.act2 form input[type=text]{min-width:120px}
.act2 .owned{color:var(--warn);font-size:12px}
`

const actionHTML = `
{{define "action"}}<div class="act2 {{if .Pressable}}{{else if eq .Outcome "executed"}}done2{{else}}no2{{end}}">
  <span class="kind">{{.Human}}</span>
  <b class="sum">{{.Summary}}</b>
  {{if .Fields}}<dl>
    {{range .Fields}}<dt>{{.Label}}</dt>
      <dd>{{if .Href}}<a href="{{.Href}}">{{.Value}}</a>{{else}}{{.Value}}{{end}}</dd>{{end}}
  </dl>{{end}}
  {{if .Pressable}}
    <p class="does">{{.What}}{{if .Gated}} <span class="owned">This one is yours to decide — the
      console will not press it however it is configured.</span>{{end}}</p>
    <form method="post" action="/console/do">
      <input type="hidden" name="action" value="{{.ID}}">
      <input type="hidden" name="back" value="{{.Back}}">
      <input type="text" name="who" placeholder="your name" required>
      <button type="submit" name="press" value="yes">{{.Verb}}</button>
      <button type="submit" name="press" value="no" class="sec">{{.Decline}}</button>
    </form>
  {{else}}<p class="does"><span class="pill {{verdictClass .Outcome}}">{{.Outcome}}</span>
    {{if .Detail}}{{.Detail}}{{else}}{{.What}}{{end}}</p>{{end}}
</div>{{end}}
`
