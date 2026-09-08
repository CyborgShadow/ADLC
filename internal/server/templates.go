package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

var funcs = template.FuncMap{
	"ago": func(ms int64) string {
		if ms == 0 {
			return "—"
		}
		return time.Since(time.UnixMilli(ms)).Round(time.Second).String() + " ago"
	},
	"pct": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return a * 100 / b
	},
	"short": func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		if s == "" {
			return "—"
		}
		return s
	},
	"join":  func(v []string) string { return strings.Join(v, ", ") },
	"clock": func(t time.Time) string { return t.Format("15:04:05") },
	"stateClass": func(s string) string {
		switch s {
		case "done", "delivered":
			return "ok"
		case "blocked", "rejected":
			return "bad"
		case "awaiting_approval", "planned":
			return "warn"
		case "queued", "cancelled", "superseded", "theory", "paused":
			return "mute"
		}
		return "live"
	},
	"verdictClass": func(s string) string {
		switch s {
		case "INTACT", "live", "GREEN", "pass", "ok":
			return "ok"
		case "TAMPERED", "NEVER RUN", "STALE", "RED", "bad":
			return "bad"
		case "UNKNOWN", "paused", "warn":
			return "warn"
		}
		return "mute"
	},
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	},
}

// The dashboard is one page of HTML with no JavaScript and no external assets.
//
// That is a constraint rather than a taste. This surface has to work when
// something has gone wrong — which is the only time anybody opens it — so
// it must not depend on a network fetch, a bundler or a browser feature. Every
// control is a plain form and the refresh is a meta tag.
var tmpl = template.Must(template.New("page").Funcs(funcs).Parse(strings.Join([]string{
	pageHTML, overviewHTML, roadmapHTML, progressHTML, questionsHTML, approvalsHTML,
	rolesHTML, roleHTML, coordinationHTML, configHTML, historyHTML, runHTML, itemHTML,
	segmentHTML, aboutHTML, endHTML,
}, "")))

const pageHTML = `
{{define "page"}}<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
{{if .Refresh}}<meta http-equiv="refresh" content="{{.Refresh}}">{{end}}
<title>{{.Title}} · {{.Project}}</title>
<style>
:root{--bg:#0f1417;--card:#161d21;--line:#253136;--ink:#dde6e6;--dim:#8fa3a5;
--ok:#5fbf8b;--warn:#d8a750;--bad:#e0796d;--live:#59a9c4;--accent:#4bb3a8}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);
font:14px/1.55 ui-sans-serif,system-ui,"Segoe UI",Roboto,sans-serif}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
header{border-bottom:1px solid var(--line);padding:12px 24px;display:flex;
align-items:center;gap:18px;flex-wrap:wrap;background:var(--card);position:sticky;top:0;z-index:5}
header h1{font-size:15px;margin:0;font-weight:600;letter-spacing:-.01em;white-space:nowrap}
header .meta{margin-left:auto;color:var(--dim);font-size:12px;font-family:ui-monospace,Consolas,monospace}
nav{display:flex;gap:2px;flex-wrap:wrap}
nav a{padding:5px 10px;border-radius:3px;color:var(--dim);font-size:13px}
nav a:hover{background:var(--line);text-decoration:none}
nav a.on{background:var(--line);color:var(--ink)}
nav a .n{display:inline-block;margin-left:6px;padding:0 6px;border-radius:9px;font-size:11px;
background:var(--line);color:var(--ink)}
nav a.alarm{color:var(--warn)}nav a.alarm .n{background:var(--warn);color:#1b1305}
main{padding:22px 24px 60px;max-width:1220px;margin:0 auto}
h2{font-size:14px;margin:26px 0 10px;font-weight:600;letter-spacing:.01em}
h2:first-child{margin-top:0}
h2 .sub{font-weight:400;color:var(--dim);margin-left:8px;font-size:13px}
.banner{padding:11px 15px;border-radius:4px;margin-bottom:16px;border-left:3px solid var(--accent);background:var(--card)}
.banner.bad{border-left-color:var(--bad)}
.banner.calm{color:var(--dim)}
.grid{display:grid;gap:10px;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));margin-bottom:6px}
.card{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:13px 15px}
.card .n{font-size:24px;font-weight:600;font-variant-numeric:tabular-nums;line-height:1.1}
.card .l{color:var(--dim);font-size:12px;margin-top:3px}
table{width:100%;border-collapse:collapse;background:var(--card);border:1px solid var(--line);border-radius:4px}
th{text-align:left;font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--dim);
padding:7px 11px;border-bottom:1px solid var(--line);font-weight:600;white-space:nowrap}
td{padding:7px 11px;border-bottom:1px solid var(--line);vertical-align:top}
tr:last-child td{border-bottom:none}
.wrap{overflow-x:auto;margin-bottom:6px}
.mono{font-family:ui-monospace,Consolas,monospace;font-size:12px}
.num{text-align:right;font-variant-numeric:tabular-nums;font-family:ui-monospace,Consolas,monospace}
.pill{display:inline-block;padding:1px 8px;border-radius:10px;font-size:11px;font-weight:600;white-space:nowrap}
.ok{background:#12301f;color:var(--ok)}.warn{background:#31260d;color:var(--warn)}
.bad{background:#331915;color:var(--bad)}.live{background:#12262e;color:var(--live)}
.mute{background:var(--line);color:var(--dim)}
.dim{color:var(--dim)}.small{font-size:12px}
.bar{height:6px;background:var(--line);border-radius:3px;overflow:hidden;margin-top:6px}
.bar>i{display:block;height:100%;background:var(--ok)}
form.inline{display:flex;gap:7px;flex-wrap:wrap;align-items:center;margin-top:9px}
input[type=text],input[type=number],textarea{background:#0c1114;border:1px solid var(--line);
color:var(--ink);border-radius:3px;padding:6px 9px;font:inherit;font-size:13px}
textarea{width:100%;min-height:58px;resize:vertical}
input[type=text]{min-width:140px}input[type=number]{width:80px}
button{background:var(--accent);color:#05201d;border:0;border-radius:3px;padding:6px 13px;
font:inherit;font-weight:600;cursor:pointer}
button.sec{background:var(--line);color:var(--ink)}
button:hover{filter:brightness(1.1)}
.q{background:var(--card);border:1px solid var(--line);border-left:3px solid var(--warn);
border-radius:4px;padding:13px 15px;margin-bottom:11px}
.q h3{margin:0 0 5px;font-size:14px;font-weight:600}
.q .lean{color:var(--dim);margin:5px 0}
pre{background:#0b1013;border:1px solid var(--line);border-radius:4px;padding:12px;
overflow-x:auto;font-family:ui-monospace,Consolas,monospace;font-size:12px;line-height:1.5;
white-space:pre-wrap;word-break:break-word;max-height:520px}
.tl{border-left:2px solid var(--line);margin-left:6px;padding-left:16px}
.tl .ev{position:relative;padding-bottom:14px}
.tl .ev:before{content:"";position:absolute;left:-21px;top:5px;width:8px;height:8px;
border-radius:50%;background:var(--line)}
.tl .ev.ok:before{background:var(--ok)}.tl .ev.bad:before{background:var(--bad)}
.tl .ev.warn:before{background:var(--warn)}
.tl b{font-weight:600}
.chain{counter-reset:c}
.chain .step{background:var(--card);border:1px solid var(--line);border-radius:4px;
padding:12px 15px;margin-bottom:8px;display:grid;grid-template-columns:150px 1fr;gap:14px}
.chain .step .who{font-weight:600}
.chain .step .who small{display:block;color:var(--dim);font-weight:400;margin-top:2px}
.tabs{display:flex;gap:4px;margin-bottom:12px}
.tabs a{padding:5px 12px;border-radius:3px;background:var(--card);border:1px solid var(--line);color:var(--dim)}
.tabs a.on{color:var(--ink);border-color:var(--accent)}
footer{color:var(--dim);font-size:12px;padding:18px 24px;border-top:1px solid var(--line);
max-width:1220px;margin:24px auto 0}
@media (max-width:700px){.chain .step{grid-template-columns:1fr}}
` + aboutCSS + `
</style></head><body>
<header>
  <h1>{{.Project}}</h1>
  <nav>{{range .Nav}}<a href="{{.Href}}" class="{{if .Active}}on{{end}}{{if .Alarm}} alarm{{end}}">{{.Label}}{{if .Count}}<span class="n">{{.Count}}</span>{{end}}</a>{{end}}</nav>
  <span class="meta">ledger <span class="pill {{verdictClass .Verdict}}">{{.Verdict}}</span> seq {{.HeadSeq}} · {{.Now}}</span>
</header>
<main>
{{if .Flash}}<div class="banner{{if .FlashBad}} bad{{end}}">{{.Flash}}</div>{{end}}
{{if .Attn.Tampered}}<div class="banner bad"><b>The ledger does not describe itself.</b>
Do not act on anything on this page until that is explained. Run <span class="mono">adlc ledger verify</span>.</div>
{{else if .Attn.Any}}<div class="banner"><b>Waiting on you:</b>
{{if .Attn.Questions}}{{plural .Attn.Questions "blocking question" "blocking questions"}}.{{end}}
{{if .Attn.Approvals}}{{plural .Attn.Approvals "approval" "approvals"}}.{{end}}
{{if .Attn.Blocked}}{{plural .Attn.Blocked "blocked item" "blocked items"}}.{{end}}
{{if .Attn.Stuck}}{{plural .Attn.Stuck "item is" "items are"}} past the rework limit.{{end}}
{{if .Attn.DarkLoops}}{{plural .Attn.DarkLoops "lane is not firing" "lanes are not firing"}}.{{end}}
</div>
{{else}}<div class="banner calm">Nothing needs you. The fleet is working.</div>{{end}}
`

const endHTML = `
</main>
<footer>Every figure here is read from the hash-chained ledger. Nothing is self-reported —
a worker that has never run is counted from the registry, not inferred from its silence.</footer>
</body></html>{{end}}
`

const overviewHTML = `
{{if eq .Page "overview"}}{{with .Body}}
<h2>Running now <span class="sub">what the fleet is doing this second</span></h2>
<div class="wrap"><table>
  <tr><th>run</th><th>role</th><th>item</th><th>stage</th><th class="num">elapsed</th>
      <th class="num">tries</th><th class="num">pass</th><th class="num">fail</th><th class="num">cost</th></tr>
  {{range .Active}}<tr>
    <td class="mono"><a href="/run/{{.RunID}}">{{.RunID}}</a>
      {{if .Stale}}<div><span class="pill bad">open far longer than the dispatch timeout — the process probably died</span></div>{{end}}</td>
    <td>{{.WorkerType}}</td>
    <td class="mono">{{if .Item}}<a href="/item/{{.Item}}">{{.Item}}</a>{{else}}<span class="dim">—</span>{{end}}</td>
    <td class="dim">{{.Stage}}</td><td class="num">{{.Elapsed}}</td>
    <td class="num">{{.Tries}}</td><td class="num">{{.Passed}}</td><td class="num">{{.Failed}}</td>
    <td class="num">{{.Cost}}</td>
  </tr>{{else}}<tr><td colspan="9" class="dim">Nothing is running. Either every lane is idle, or the scheduler is not started —
    <span class="mono">adlc schedule run</span>.</td></tr>{{end}}
</table></div>

<h2>Where the work is</h2>
<div class="grid">
  {{range .Stages}}<div class="card"><div class="n">{{.N}}</div><div class="l">{{.Label}}</div></div>{{end}}
  <div class="card"><div class="n">{{.Spend.Today}}</div><div class="l">spend, last 24h</div></div>
</div>
{{if .Spend.Unpriced}}<div class="banner bad">{{plural .Spend.Unpriced "finished run" "finished runs"}} used a model
with no price entry. Their cost is <b>unknown, not zero</b>, and is not in the figure above. First: <span class="mono">{{.Spend.UnpricedRun}}</span>.</div>{{end}}

{{if .Refusals}}<h2>Recently refused <span class="sub">a refusal is a recorded outcome, not an error that went away</span></h2>
<div class="wrap"><table>
  <tr><th>item</th><th>role</th><th>reason</th><th>what the control plane saw</th></tr>
  {{range .Refusals}}<tr>
    <td class="mono">{{if .ItemID}}<a href="/item/{{.ItemID}}">{{.ItemID}}</a>{{end}}</td>
    <td class="dim">{{.Worker}}</td><td><span class="pill bad">{{.Reason}}</span></td>
    <td class="dim small">{{.Detail}}</td>
  </tr>{{end}}
</table></div>{{end}}

<h2>Recent runs</h2>
<div class="wrap"><table>
  <tr><th>run</th><th>role</th><th>item</th><th>outcome</th><th class="num">in</th><th class="num">out</th>
      <th class="num">cost</th><th>when</th></tr>
  {{range .Recent}}<tr>
    <td class="mono"><a href="/run/{{.RunID}}">{{.RunID}}</a></td><td class="dim">{{.WorkerType}}</td>
    <td class="mono">{{if .ItemID}}<a href="/item/{{.ItemID}}">{{.ItemID}}</a>{{end}}</td>
    <td><span class="pill {{.Class}}">{{.Outcome}}</span></td>
    <td class="num dim">{{.Usage.InputTokens}}</td><td class="num dim">{{.Usage.OutputTokens}}</td>
    <td class="num">{{.Cost}}</td><td class="dim">{{.When}}</td>
  </tr>{{else}}<tr><td colspan="8" class="dim">No runs yet.</td></tr>{{end}}
</table></div>
<p class="dim small">{{.Blobs}} prompt/envelope blob(s) retained — that is what lets a past decision be
re-derived rather than merely described. <span class="mono">adlc run replay &lt;id&gt;</span>.</p>
{{end}}{{end}}
`

const roadmapHTML = `
{{if eq .Page "roadmap"}}
<div class="banner calm">An idea is signed off, researched, decomposed, and the decomposition is
checked against the intent <b>before any of it is built</b>. Signing off is the one planning gate no
machine passes on its own; a deliverable at <b>planned</b> is waiting for the plan review, and
nothing under it is dispatched until it passes.</div>
{{range .Body}}
<div class="card" style="margin-bottom:10px">
  <div style="display:flex;gap:10px;align-items:baseline;flex-wrap:wrap">
    <b class="mono"><a href="/segment/{{.ID}}">{{.ID}}</a></b>
    <span style="font-size:15px">{{.Title}}</span>
    <span class="pill {{stateClass .State}}">{{.State}}</span>
    <span class="dim" style="margin-left:auto">{{.Progress.Done}}/{{.Progress.Total}} items done</span>
  </div>
  {{if .Brief}}<div style="margin-top:7px">{{.Brief}}</div>{{end}}
  {{if .Rationale}}<div class="dim small" style="margin-top:4px">Why: {{.Rationale}}</div>{{end}}
  <div class="bar"><i style="width:{{.Progress.Percent}}%"></i></div>
  <div class="dim small" style="margin-top:6px">{{.Next}}
    {{if .Progress.Blocked}}· <span class="pill bad">{{.Progress.Blocked}} blocked</span>{{end}}
    {{if .Progress.Waiting}}· <span class="pill warn">{{.Progress.Waiting}} awaiting approval</span>{{end}}</div>
  {{if .NeedsYou}}<form class="inline" method="post" action="/signoff">
    <input type="hidden" name="id" value="{{.ID}}">
    <input type="hidden" name="to" value="{{.SignTo}}">
    <input type="text" name="who" placeholder="your name">
    <input type="text" name="why" placeholder="why, in your own words" style="flex:1;min-width:200px">
    <button type="submit">{{.SignVerb}}</button>
  </form>{{end}}
</div>
{{else}}<div class="card dim">No deliverables yet. Add one with a brief and a target and the planner lane
will fill it:<br><span class="mono">adlc segment create -id S1 -title "…" -brief "…" -target 5</span></div>{{end}}
{{end}}
`

const progressHTML = `
{{if eq .Page "progress"}}{{with .Body}}
<h2>Everything, by stage</h2>
<div class="grid">
  {{$t := .Totals}}{{range .Stages}}<div class="card">
    <div class="n">{{index $t .Key}}</div><div class="l">{{.Label}}</div></div>{{end}}
</div>
<p class="dim small">Overall: <b>{{.Overall.Done}}</b> of <b>{{.Overall.Total}}</b> work items done
({{.Overall.Percent}}%). The bar measures items <b>finished</b>, not items touched — how busy the fleet
looks is not the question anyone is asking.</p>

<h2>By deliverable</h2>
<div class="wrap"><table>
  <tr><th>deliverable</th><th>state</th><th style="width:200px">progress</th>
      <th class="num">done</th><th class="num">active</th><th class="num">queued</th>
      <th class="num">blocked</th><th class="num">approval</th><th class="num">total</th></tr>
  {{range .Rows}}<tr>
    <td><a href="/segment/{{.ID}}">{{.ID}}</a> <span class="dim">{{.Title}}</span></td>
    <td><span class="pill {{stateClass .State}}">{{.State}}</span></td>
    <td><div class="bar" style="margin:6px 0"><i style="width:{{.Progress.Percent}}%"></i></div></td>
    <td class="num">{{.Progress.Done}}</td><td class="num">{{.Progress.Active}}</td>
    <td class="num">{{.Progress.Queued}}</td>
    <td class="num">{{if .Progress.Blocked}}<span class="pill bad">{{.Progress.Blocked}}</span>{{else}}0{{end}}</td>
    <td class="num">{{if .Progress.Waiting}}<span class="pill warn">{{.Progress.Waiting}}</span>{{else}}0{{end}}</td>
    <td class="num">{{.Progress.Total}}</td>
  </tr>{{else}}<tr><td colspan="9" class="dim">Nothing on the roadmap yet.</td></tr>{{end}}
</table></div>
{{end}}{{end}}
`

const questionsHTML = `
{{if eq .Page "questions"}}{{with .Body}}
<div class="banner calm">An agent that needs a decision stops and asks rather than guessing. Each question
carries the asker's own recommendation — a question with no lean hands you back the analysis it was
dispatched to do. Your answer is recorded verbatim and unblocks the item.</div>
{{range .Items}}
<div class="q">
  <h3>{{if .Blocking}}<span class="pill bad">blocking</span> {{end}}{{.Text}}</h3>
  <div class="dim mono small">{{.ID}}{{if .ItemID}} · <a href="/item/{{.ItemID}}">{{.ItemID}}</a>{{end}}
    {{if .RaisedBy}} · raised by <a href="/run/{{.RaisedBy}}">{{.RaisedBy}}</a>{{end}}</div>
  {{if .Lean}}<div class="lean"><b>Their recommendation:</b> {{.Lean}}</div>{{end}}
  {{if .Evidence}}<div class="dim small">{{.Evidence}}</div>{{end}}
  <form method="post" action="/answer">
    <input type="hidden" name="id" value="{{.ID}}">
    <textarea name="answer" placeholder="Your answer, recorded verbatim." required></textarea>
    <div class="inline"><input type="text" name="who" placeholder="your name">
      <button type="submit">Answer and unblock</button></div>
  </form>
</div>
{{else}}<div class="card dim">No open questions.</div>{{end}}
{{end}}{{end}}
`

const approvalsHTML = `
{{if eq .Page "approvals"}}{{with .Body}}
<div class="banner calm">Anything whose blast radius exceeds the configured threshold stops here before it
touches a machine. You approve <b>one specific plan</b>: if the plan changes afterwards, the approval stops
applying and the apply is refused.</div>
{{range .Rows}}
<div class="q" {{if .Decided}}style="border-left-color:var(--line)"{{end}}>
  <h3>{{.Item.Title}}</h3>
  <div class="dim mono small">{{.ID}} · <a href="/item/{{.ItemID}}">{{.ItemID}}</a> ·
    radius <b>{{.Radius}}</b> · plan {{short .PlanDigest}} · requested {{ago .RequestedMS}}</div>
  {{if .Summary}}<div class="lean">{{.Summary}}</div>{{end}}
  {{if .Item.Resources}}<div class="dim small">changes: <span class="mono">{{join .Item.Resources}}</span></div>{{end}}
  {{if .Decided}}
    <div style="margin-top:7px"><span class="pill {{if eq .Verdict "approve"}}ok{{else}}bad{{end}}">{{.Verdict}}d</span>
      by {{.Approver}} {{ago .DecidedMS}}{{if .Note}} — {{.Note}}{{end}}</div>
  {{else}}
    <form class="inline" method="post" action="/decide">
      <input type="hidden" name="id" value="{{.ID}}">
      <input type="text" name="approver" placeholder="your name" required>
      <input type="text" name="note" placeholder="why, or any condition" style="flex:1;min-width:200px">
      <button type="submit" name="verdict" value="approve">Approve this plan</button>
      <button type="submit" name="verdict" value="reject" class="sec">Reject</button>
    </form>
  {{end}}
</div>
{{else}}<div class="card dim">Nothing is waiting for approval.</div>{{end}}
{{end}}{{end}}
`

const rolesHTML = `
{{if eq .Page "roles"}}{{with .Body}}
<div class="banner calm">Each role is a prompt file in the repository. The dispatcher reads it at dispatch
time and carries no prompt of its own, so editing the file changes the role — reviewably, in a commit.
The shared preamble below is assembled into every one of them, which is why fleet-wide policy is one edit.</div>
<div class="wrap"><table>
  <tr><th>role</th><th>layer</th><th>can</th><th>areas</th><th>prompt</th>
      <th class="num">runs</th><th class="num">pass</th><th class="num">refused</th></tr>
  {{range .Rows}}<tr>
    <td class="mono">{{.Type}}{{if .LowCadence}} <span class="pill mute">low cadence</span>{{end}}
      <div class="dim small" style="max-width:38ch">{{.Description}}</div></td>
    <td class="dim">{{.Layer}}</td>
    <td class="mono small">{{join .Capabilities}}</td>
    <td class="dim small">{{if .Areas}}{{join .Areas}}{{else}}<i>generalist</i>{{end}}</td>
    <td><a href="/role/{{.Prompt}}">{{.Prompt}}</a>
      {{if .Missing}}<span class="pill bad">prompt file missing</span>{{end}}
      <div class="dim mono small">{{short .SHA}}</div></td>
    <td class="num">{{if .Stat.Runs}}{{.Stat.Runs}}{{else}}<span class="pill bad">0</span>{{end}}</td>
    <td class="num">{{.Stat.Pass}}</td><td class="num">{{.Stat.Refusals}}</td>
  </tr>{{end}}
</table></div>
<h2>Shared preamble <span class="sub">{{.PPath}} · {{short .PSHA}}</span></h2>
<pre>{{.Preamble}}</pre>
{{end}}{{end}}
`

const roleHTML = `
{{if eq .Page "role"}}{{with .Body}}
<h2>{{.ID}} <span class="sub">{{.Version}} · {{.Path}} · {{short .SHA}}</span></h2>
<p class="dim small">Used by: <span class="mono">{{join .UsedBy}}</span>. To tune this role, edit the file
and commit — the change takes effect on the next dispatch, and the gate checks that every mandatory
safety clause survived the edit.</p>
<h2>Role text</h2>
<pre>{{.Body}}</pre>
<h2>What an agent actually receives <span class="sub">preamble + role, assembled at dispatch</span></h2>
<pre>{{.Assembled}}</pre>
{{end}}{{end}}
`

const coordinationHTML = `
{{if eq .Page "coordination"}}{{with .Body}}
<div class="banner calm">Roles never talk to each other. Each one is handed a prompt, does a bounded job,
reports a verdict, and exits — and the control plane decides what happens next. That is why a handoff
cannot be dropped: it is arithmetic on recorded state, not a message somebody has to deliver.</div>
<div class="chain">
{{range .Chain}}<div class="step">
  <div class="who">{{.Stage}}<small>{{.Who}}</small></div>
  <div>{{.Does}}<div class="dim small" style="margin-top:4px">→ {{.HandsTo}}</div></div>
</div>{{end}}
</div>

<h2>Lanes <span class="sub">who runs, how often, and whether they are actually firing</span></h2>
<div class="wrap"><table>
  <tr><th>lane</th><th>scope</th><th class="num">every</th><th class="num">ticks</th>
      <th class="num">dispatches</th><th>last fired</th></tr>
  {{$now := .Now}}{{range .Lanes}}<tr>
    <td class="mono">{{.Loop}} {{if not .Enabled}}<span class="pill mute">paused</span>
      {{else if not (.Fresh $now 3.0)}}<span class="pill bad">not firing</span>{{end}}</td>
    <td class="dim mono small">{{.Scope}}</td><td class="num dim">{{.EverySecond}}s</td>
    <td class="num">{{.Ticks}}</td><td class="num">{{.Dispatches}}</td>
    <td class="dim">{{ago .LastTickMS}}</td>
  </tr>{{else}}<tr><td colspan="6" class="dim">No lanes declared.</td></tr>{{end}}
</table></div>

<h2>Area routing <span class="sub">how work reaches the right specialist</span></h2>
<div class="wrap"><table>
  <tr><th>area</th><th>owned by</th><th>which can</th></tr>
  {{range .Areas}}<tr><td class="mono">{{.Area}}</td><td class="mono">{{.Owner}}</td>
    <td class="dim mono small">{{.Caps}}</td></tr>{{end}}
</table></div>
<p class="dim small">An area with no owner is a configuration error, not a warning: an item filed under one
is unreachable, and an unreachable item looks exactly like one nobody has got round to yet.</p>
{{end}}{{end}}
`

const configHTML = `
{{if eq .Page "config"}}{{with .Body}}
<h2>Lanes <span class="sub">pause one, or change its cadence — takes effect on its next tick</span></h2>
<div class="wrap"><table>
  <tr><th>lane</th><th>status</th><th>scope</th><th>last fired</th><th>controls</th></tr>
  {{range .Lanes}}<tr>
    <td class="mono">{{.Name}}</td>
    <td><span class="pill {{verdictClass .Status}}">{{.Status}}</span></td>
    <td class="dim mono small">{{.Scope}}</td>
    <td class="dim">{{.Since}}</td>
    <td><form class="inline" method="post" action="/loop" style="margin:0">
      <input type="hidden" name="name" value="{{.Name}}">
      <label class="small dim"><input type="checkbox" name="enabled" {{if .Enabled}}checked{{end}}> running</label>
      <input type="number" name="every" value="{{.EverySeconds}}" min="15" title="seconds between firings">
      <input type="number" name="max" value="{{.MaxPerTick}}" min="1" title="dispatches per firing">
      <button type="submit" class="sec">Save</button>
    </form></td>
  </tr>{{else}}<tr><td colspan="5" class="dim">No lanes declared.</td></tr>{{end}}
</table></div>

<h2>Safety policy <span class="sub">read-only here; it lives in {{.Path}}</span></h2>
<div class="wrap"><table>
  <tr><th>setting</th><th>value</th><th>what it means</th></tr>
  <tr><td class="mono">auto_apply_max</td><td class="mono">{{.Cfg.Blast.AutoApplyMax}}</td>
    <td class="dim">The largest blast radius that may be applied with no approval. Anything above it stops and waits for a person.</td></tr>
  <tr><td class="mono">named_approver_min</td><td class="mono">{{.Cfg.Blast.NamedApproverMin}}</td>
    <td class="dim">From this radius up, the approval must name a human rather than merely exist.</td></tr>
  <tr><td class="mono">two_approvals_min</td><td class="mono">{{.Cfg.Blast.TwoApprovalsMin}}</td>
    <td class="dim">From this radius up, two distinct approvers are required.</td></tr>
  <tr><td class="mono">approval_ttl_minutes</td><td class="mono">{{.Cfg.Blast.ApprovalTTLMinutes}}</td>
    <td class="dim">How long an approval stays valid, even if the plan has not moved.</td></tr>
  <tr><td class="mono">max_attempts</td><td class="mono">{{.Cfg.Dispatch.MaxAttempts}}</td>
    <td class="dim">Rework attempts before an item escalates to a person instead of looping.</td></tr>
  <tr><td class="mono">isolation</td><td class="mono">{{.Cfg.Dispatch.Isolation}}</td>
    <td class="dim">How each run's workspace is separated from the shared tree.</td></tr>
  <tr><td class="mono">per_day_micros</td><td class="mono">{{.Cfg.Budget.PerDayMicros}}</td>
    <td class="dim">Daily spend cap. Zero means unlimited, which is not the same as exhausted.</td></tr>
</table></div>
{{if not .PriceSet}}<div class="banner bad">No model prices are configured, so every run reports
<b>UNPRICED</b> — cost unknown, not zero. Set <span class="mono">budget.price_micros_per_mtok</span>
before running unattended, or the spend cap can never be reached.</div>{{end}}

<h2>Gate checks <span class="sub">what the control plane runs itself, and how it reads the answer</span></h2>
<div class="wrap"><table>
  <tr><th>check</th><th>kind</th><th>command</th><th>verdict is</th><th>gates</th></tr>
  {{range .Checks}}<tr>
    <td class="mono">{{.ID}}</td><td class="dim">{{.Kind}}</td>
    <td class="mono small">{{join .Command}}</td>
    <td class="mono small">{{.Verdict}}</td>
    <td class="dim small">{{join .RequiredFor}}</td>
  </tr>{{end}}
</table></div>

<h2>Mandatory clauses <span class="sub">every role prompt must carry these; the gate refuses a commit that drops one</span></h2>
<div class="card"><ul style="margin:0;padding-left:18px">
  {{range .Clauses}}<li class="mono small">{{.}}</li>{{end}}
</ul></div>

{{end}}{{end}}
`

const historyHTML = `
{{if eq .Page "history"}}{{with .Body}}
<div class="tabs">
  <a href="/history?tab=runs" class="{{if eq .Tab "runs"}}on{{end}}">Runs</a>
  <a href="/history?tab=ledger" class="{{if eq .Tab "ledger"}}on{{end}}">Ledger</a>
</div>
{{if eq .Tab "ledger"}}
<div class="banner calm">The raw chain, newest first. Every row is hash-linked to the one before it, and a
kind this build cannot interpret is marked — that is reported as unknown, never as tampering.</div>
<div class="wrap"><table>
  <tr><th class="num">seq</th><th>when</th><th>kind</th><th>subject</th><th>actor</th><th>payload</th></tr>
  {{range .Rows}}<tr>
    <td class="num dim">{{.Seq}}</td><td class="dim small">{{.When}}</td>
    <td class="mono small">{{.Kind}}{{if not .Known}} <span class="pill warn">unknown to this build</span>{{end}}</td>
    <td class="mono small">{{.Subject}}</td><td class="dim small">{{.Actor}}</td>
    <td class="dim small mono" style="max-width:52ch;overflow:hidden;text-overflow:ellipsis">{{.Payload}}</td>
  </tr>{{end}}
</table></div>
{{else}}
<div class="banner calm">What each run actually did, in plain language. Open one to see what it was given,
what the control plane observed, and what it decided — and to re-derive that decision from the record.</div>
<div class="wrap"><table>
  <tr><th>run</th><th>role</th><th>item</th><th>what it did</th><th class="num">cost</th><th>when</th></tr>
  {{range .Runs}}<tr>
    <td class="mono"><a href="/run/{{.RunID}}">{{.RunID}}</a></td>
    <td class="dim">{{.WorkerType}}</td>
    <td class="mono">{{if .ItemID}}<a href="/item/{{.ItemID}}">{{.ItemID}}</a>{{end}}</td>
    <td><span class="pill {{.Class}}">{{.Verdict}}</span> {{.Headline}}</td>
    <td class="num">{{.Cost}}</td><td class="dim">{{.When}}</td>
  </tr>{{else}}<tr><td colspan="6" class="dim">No runs yet.</td></tr>{{end}}
</table></div>
{{end}}
{{end}}{{end}}
`

const runHTML = `
{{if eq .Page "run"}}{{with .Body}}
<div class="card">
  <div style="display:flex;gap:10px;align-items:baseline;flex-wrap:wrap">
    <b class="mono">{{.Run.RunID}}</b>
    <span class="pill {{verdictClass .Run.Verdict}}">{{.Run.Verdict}}</span>
    <span class="dim" style="margin-left:auto">{{.Run.WorkerType}} · {{.Duration}} · {{.Cost}}</span>
  </div>
  <div style="margin-top:8px;font-size:15px">{{.Headline}}</div>
</div>

<h2>Why this mattered</h2>
<div class="card">{{range .Purpose}}<div style="margin-bottom:6px">{{.}}</div>{{end}}</div>

<h2>What happened, in order</h2>
<div class="tl">{{range .Steps}}<div class="ev {{.Verdict}}">
  <b>{{.Title}}</b> <span class="dim small">{{clock .At}}</span>
  <div class="dim small">{{.Detail}}</div>
</div>{{end}}</div>

<h2>Reproducing this decision</h2>
<div class="card">
  {{if .Reproducible}}
    <p>The prompt and the envelope were both retained, so this decision can be re-derived from the record:</p>
    <pre>adlc run replay {{.Run.RunID}}</pre>
    <p class="dim small">That re-parses the exact envelope this run produced, re-runs the declared checks, and
    puts the result back through the same authority. It replays the <b>decision</b>, not the agent —
    re-invoking a language model reproduces nothing, and claiming otherwise would be dishonest.</p>
  {{else}}
    <p><span class="pill warn">not reproducible</span> {{.WhyNot}}</p>
  {{end}}
  <p class="dim small">prompt {{short .Run.PromptSHA}} {{if .PromptRetained}}(retained){{else}}(not retained){{end}}
   · envelope {{short .Run.EnvelopeSHA}} {{if .EnvRetained}}(retained){{else}}(not retained){{end}}
   · base {{short .Run.BaseSHA}} · head {{short .Run.HeadSHA}}</p>
</div>
{{end}}{{end}}
`

const itemHTML = `
{{if eq .Page "item"}}{{with .Body}}
<div class="card">
  <div style="display:flex;gap:10px;align-items:baseline;flex-wrap:wrap">
    <b class="mono">{{.Item.ID}}</b>
    <span class="pill {{stateClass .Item.State}}">{{.Item.State}}</span>
    <span class="pill mute">{{.Stage.Label}}</span>
    <span style="font-size:15px">{{.Item.Title}}</span>
    <span class="dim" style="margin-left:auto">radius {{.Item.Radius}} · area {{.Item.Area}} ·
      attempt {{.Item.Attempts}} · {{.Tries}} run(s), {{.Passed}} passed, {{.Failed}} failed</span>
  </div>
  {{if .Item.Rationale}}<div class="dim" style="margin-top:7px">Why: {{.Item.Rationale}}</div>{{end}}
  {{if .Segment.ID}}<div class="dim small" style="margin-top:4px">Part of
    <a href="/segment/{{.Segment.ID}}">{{.Segment.ID}} {{.Segment.Title}}</a></div>{{end}}
  {{if .Item.BlockedWhy}}<div class="banner bad" style="margin-top:9px">{{.Item.BlockedWhy}}</div>{{end}}
  {{if .Item.Resources}}<div class="dim small" style="margin-top:6px">resources: <span class="mono">{{join .Item.Resources}}</span></div>{{end}}
</div>

<h2>Acceptance criteria <span class="sub">the specification of record — no agent may change these</span></h2>
<div class="card"><ol style="margin:0;padding-left:18px">{{range .Item.Criteria}}<li>{{.}}</li>{{end}}</ol></div>

<h2>Runs</h2>
<div class="wrap"><table>
  <tr><th>run</th><th>role</th><th>verdict</th><th>when</th><th>commit</th></tr>
  {{range .Runs}}<tr>
    <td class="mono"><a href="/run/{{.RunID}}">{{.RunID}}</a></td><td class="dim">{{.WorkerType}}</td>
    <td>{{if .Finished}}<span class="pill {{verdictClass .Verdict}}">{{.Verdict}}</span>
        {{else}}<span class="pill warn">UNKNOWN — started, no end recorded</span>{{end}}</td>
    <td class="dim">{{ago .StartedMS}}</td><td class="mono dim small">{{short .HeadSHA}}</td>
  </tr>{{else}}<tr><td colspan="5" class="dim">No runs yet.</td></tr>{{end}}
</table></div>

<h2>Decisions</h2>
<div class="wrap"><table>
  <tr><th>change</th><th>role</th><th>answer</th><th>detail</th></tr>
  {{range .Proposals}}<tr>
    <td class="mono dim">{{if .From}}{{.From}} → {{end}}{{.To}}</td><td class="dim">{{.Worker}}</td>
    <td>{{if .Admitted}}<span class="pill ok">advanced</span>{{else}}<span class="pill bad">{{.Reason}}</span>{{end}}</td>
    <td class="dim small">{{.Detail}}</td>
  </tr>{{else}}<tr><td colspan="4" class="dim">Nothing yet.</td></tr>{{end}}
</table></div>

{{if .Questions}}<h2>Questions</h2>
{{range .Questions}}<div class="q"><h3>{{.Text}}</h3>
  <div class="dim mono small">{{.ID}}</div>
  {{if .Lean}}<div class="lean">{{.Lean}}</div>{{end}}
  {{if .Answered}}<div><span class="pill ok">answered</span> by {{.AnsweredBy}}: {{.Answer}}</div>{{end}}
</div>{{end}}{{end}}
{{end}}{{end}}
`

const segmentHTML = `
{{if eq .Page "segment"}}{{with .Body}}
<div class="card">
  <div style="display:flex;gap:10px;align-items:baseline;flex-wrap:wrap">
    <b class="mono">{{.Segment.ID}}</b>
    <span class="pill {{stateClass .Segment.State}}">{{.Segment.State}}</span>
    <span style="font-size:15px">{{.Segment.Title}}</span>
    <span class="dim" style="margin-left:auto">{{.Progress.Done}}/{{.Progress.Total}} done</span>
  </div>
  {{if .Segment.Brief}}<div style="margin-top:8px">{{.Segment.Brief}}</div>{{end}}
  {{if .Segment.Rationale}}<div class="dim small" style="margin-top:4px">Why: {{.Segment.Rationale}}</div>{{end}}
  <div class="bar"><i style="width:{{.Progress.Percent}}%"></i></div>
</div>

<h2>Work items</h2>
<div class="wrap"><table>
  <tr><th>item</th><th>state</th><th>stage</th><th>area</th><th>title</th></tr>
  {{range .Items}}<tr>
    <td class="mono"><a href="/item/{{.ID}}">{{.ID}}</a></td>
    <td><span class="pill {{stateClass .State}}">{{.State}}</span></td>
    <td class="dim small">{{.Radius}}</td><td class="dim">{{.Area}}</td><td>{{.Title}}</td>
  </tr>{{else}}<tr><td colspan="5" class="dim">Not decomposed yet.</td></tr>{{end}}
</table></div>

<h2>Roadmap history</h2>
<div class="tl">{{range .Moves}}<div class="ev">
  <b>{{.From}} → {{.To}}</b> <span class="dim small">{{.When}}</span>
  <div class="dim small">{{.Why}}</div>
</div>{{else}}<div class="dim">No moves recorded.</div>{{end}}</div>
{{end}}{{end}}
`
