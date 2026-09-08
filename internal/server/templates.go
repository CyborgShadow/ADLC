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
	pageHTML, overviewHTML, roadmapPageHTML, progressHTML, questionsPageHTML, approvalsPageHTML,
	rolesPageHTML, coordinationHTML, configPageHTML, historyPageHTML, runHTML, itemPageHTML,
	segmentHTML, aboutHTML, aboutDataHTML, consoleHTML, endHTML,
	// After endHTML: this one is its own template, not part of the page body,
	// and a define nested inside another define is a parse error.
	consoleDockHTML, fieldsHTML,
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
a.card.tile{display:block;color:var(--ink)}
a.card.tile:hover{border-color:var(--accent);text-decoration:none}
a.card.tile.on{border-color:var(--accent)}
a.card.tile .l{color:var(--dim)}
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
.tip{position:relative;display:inline-flex;align-items:center;justify-content:center;
width:14px;height:14px;border-radius:50%;border:1px solid var(--line);color:var(--dim);
font-size:10px;font-weight:600;cursor:help;vertical-align:middle;margin-left:4px;flex:none}
.tip:hover{border-color:var(--accent);color:var(--accent)}
.tip::after{content:attr(data-tip);position:absolute;bottom:calc(100% + 7px);left:50%;
transform:translateX(-50%);background:#0b1013;border:1px solid var(--accent);border-radius:4px;
padding:8px 10px;width:max-content;max-width:280px;color:var(--ink);font-size:12px;font-weight:400;
line-height:1.5;text-align:left;white-space:normal;opacity:0;visibility:hidden;
transition:opacity .12s;z-index:60;box-shadow:0 6px 18px rgba(0,0,0,.55);pointer-events:none}
.tip:hover::after{opacity:1;visibility:visible}
.tip.up::after{bottom:auto;top:calc(100% + 7px)}
.tip.left::after{left:auto;right:0;transform:none}
tr:nth-last-child(-n+3) .tip::after{bottom:auto;top:calc(100% + 7px)}
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
.decide{margin-top:10px;border-top:1px solid var(--line);padding-top:10px}
.decide .ask{margin-bottom:7px;color:var(--dim);font-size:13px}
.decide .ask b{display:block;color:var(--ink);font-size:14px;margin-bottom:2px}
.tabs{display:flex;gap:4px;margin-bottom:12px}
.tabs a{padding:5px 12px;border-radius:3px;background:var(--card);border:1px solid var(--line);color:var(--dim)}
.tabs a.on{color:var(--ink);border-color:var(--accent)}
footer{color:var(--dim);font-size:12px;padding:18px 24px;border-top:1px solid var(--line);
max-width:1220px;margin:24px auto 0}
@media (max-width:700px){.chain .step{grid-template-columns:1fr}}
` + aboutCSS + consoleCSS + historyPageCSS + roadmapPageCSS +
	itemPageCSS + gatesCSS + rolesPageCSS + configPageCSS + aboutDataCSS + `
</style></head><body>
<header>
  <h1>{{.Project}}</h1>
  <nav>{{range .Nav}}<a href="{{.Href}}" class="{{if .Active}}on{{end}}{{if .Alarm}} alarm{{end}}">{{.Label}}{{if .Count}}<span class="n">{{.Count}}</span>{{end}}</a>{{end}}</nav>
  <span class="meta"><a href="/history?tab=ledger">ledger <span class="pill {{verdictClass .Verdict}}">{{.Verdict}}</span> seq {{.HeadSeq}}</a> · {{.Now}}</span>
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
{{template "dock" .}}
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
  <a class="card tile" href="/config#money"><div class="n">{{.Cost.Day}}</div>
    <div class="l">spend, last 24h{{if .Cost.OnDefaults}} · default rates{{end}}</div></a>
  <a class="card tile" href="/history?tab=runs"><div class="n">{{.Cost.Week}}</div>
    <div class="l">spend, last 7 days</div></a>
</div>
{{if .Cost.OnDefaults}}<div class="banner"><b>These figures use the built-in price table.</b>
Nobody here has confirmed those rates against the provider's current pricing, so treat them as an
estimate — <a href="/config">the Config page</a> shows what each model is being charged at.
{{if .Cost.TopRole}} Most of it went to <a href="/roles/{{.Cost.TopRole}}">{{.Cost.TopRole}}</a>
({{.Cost.TopRoleSpend}}).{{end}}</div>
{{else if .Cost.TopRole}}<div class="banner calm">Most of the last 24 hours went to
<a href="/roles/{{.Cost.TopRole}}">{{.Cost.TopRole}}</a> ({{.Cost.TopRoleSpend}}).</div>{{end}}
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

const progressHTML = `
{{if eq .Page "progress"}}{{with .Body}}
<h2>Everything, by stage <span class="sub">click a tile for the work behind it</span></h2>
<div class="grid">
  {{$t := .Totals}}{{$sel := .Stage}}{{range .Stages}}<a class="card tile{{if eq $sel .Key}} on{{end}}"
    href="/progress?stage={{.Key}}">
    <div class="n">{{index $t .Key}}</div><div class="l">{{.Label}}</div></a>{{end}}
</div>
{{if .Stage}}
<h2>{{.StageLabel}} <span class="sub">{{len .Drill}} item(s) · <a href="/progress">show every stage</a></span></h2>
<div class="wrap"><table>
  <tr><th>item</th><th>deliverable</th><th>area</th><th>state</th><th>what that means</th></tr>
  {{range .Drill}}<tr>
    <td><a href="/item/{{.ID}}">{{.ID}}</a> {{.Title}}</td>
    <td class="mono small">{{if .SegmentID}}<a href="/segment/{{.SegmentID}}">{{.SegmentID}}</a>{{end}}</td>
    <td class="dim small">{{.Area}}</td>
    <td><span class="pill {{.Class}}">{{.State}}</span></td>
    <td class="dim">{{.Says}}</td>
  </tr>{{else}}<tr><td colspan="5" class="dim">Nothing is in this stage.</td></tr>{{end}}
</table></div>
{{end}}
<p class="dim small">Overall: <b>{{.Overall.Done}}</b> of <b>{{.Overall.Total}}</b> work items done
({{.Overall.Percent}}%). The bar measures items <b>finished</b>, not items touched — how busy the fleet
looks is not the question anyone is asking.</p>

<h2>By deliverable</h2>
<div class="wrap"><table>
  <tr><th>deliverable</th><th>state</th><th style="width:200px">progress</th>
      <th class="num">done</th><th class="num">active</th><th class="num">queued</th>
      <th class="num">blocked</th><th class="num">approval</th><th class="num">total</th></tr>
  {{range .Rows}}<tr>
    <td><a href="/segment/{{.ID}}"><span class="mono">{{.ID}}</span> {{.Title}}</a></td>
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
