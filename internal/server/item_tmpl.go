package server

const itemPageCSS = `
.ihead{background:var(--card);border:1px solid var(--line);border-radius:5px;padding:15px 17px}
.ihead .top{display:flex;gap:11px;align-items:baseline;flex-wrap:wrap}
.ihead .top h3{margin:0;font-size:17px;font-weight:600}
.ihead .meta{margin-left:auto;color:var(--dim);font-size:12px;white-space:nowrap}
.ihead .says{margin-top:6px;color:var(--dim)}
.ihead .lede2{margin-top:11px;padding-top:11px;border-top:1px solid var(--line);
font-size:14px;line-height:1.6;max-width:82ch}
.walk{position:relative;margin:0;padding:0 0 0 22px;list-style:none;
border-left:2px solid var(--line)}
.walk li{position:relative;padding:0 0 14px 0}
.walk li::before{content:"";position:absolute;left:-27px;top:5px;width:9px;height:9px;
border-radius:50%;background:var(--ok)}
.walk li:last-child{padding-bottom:0}
.walk .edge{font-family:ui-monospace,Consolas,monospace;font-size:12px;color:var(--dim)}
.walk .what{font-size:14px}
.walk .who{color:var(--dim);font-size:12px;margin-top:2px}
.ref{background:var(--card);border:1px solid var(--line);border-left:3px solid var(--bad);
border-radius:0 4px 4px 0;padding:11px 14px;margin-bottom:8px}
.ref .top{display:flex;gap:9px;align-items:baseline;flex-wrap:wrap}
.ref .edge{font-family:ui-monospace,Consolas,monospace;font-size:12px;color:var(--dim)}
.ref .stamp{margin-left:auto;color:var(--dim);font-size:12px}
.ref .means{margin-top:5px;font-size:14px}
.ref .kept{margin-top:5px;color:var(--dim);font-size:13px}
.ref .then{margin-top:5px;font-size:13px}
.ref .then b,.ref .means b,.ref .kept b{color:var(--dim);font-weight:600}
.ref.cleared{border-left-color:var(--line);opacity:.82}
.ref.yours{border-left-color:var(--warn)}
.ref .n{font-family:ui-monospace,Consolas,monospace;font-size:11px;color:var(--dim)}
.ref .rawwrap{margin-top:6px}
.ref .rawwrap>summary{color:var(--dim);font-size:12px;cursor:pointer}
.ref .raw{margin-top:5px;color:var(--dim);font-size:12px;font-family:ui-monospace,Consolas,monospace;
overflow-wrap:anywhere}
.crit{margin:0;padding-left:20px}
.crit li{margin-bottom:4px}
`

const itemPageHTML = `
{{if eq .Page "item"}}{{with .Body}}
<div class="ihead">
  <div class="top">
    <b class="mono">{{.Item.ID}}</b>
    <span class="pill {{.Class}}">{{.Item.State}}</span>
    <span class="pill mute">{{.Stage.Label}}</span>
    <h3>{{.Item.Title}}</h3>
    <span class="meta">radius {{.Item.Radius}} · area {{.Item.Area}} ·
      {{.Tries}} attempt(s) · {{len .Runs}} run(s)</span>
  </div>
  <div class="says">{{.Says}}{{if .Segment.ID}} · part of
    <a href="/segment/{{.Segment.ID}}">{{.Segment.ID}} {{.Segment.Title}}</a>{{end}}</div>
  {{if .Item.Rationale}}<div class="lede2"><b>Why this exists:</b> {{.Item.Rationale}}</div>{{end}}
  <div class="lede2">{{.Headline}}</div>
</div>

<h2>Acceptance criteria <span class="sub">the specification of record — no agent may change these</span></h2>
<div class="card">
  {{if .Criteria}}<ol class="crit">{{range .Criteria}}<li>{{.}}</li>{{end}}</ol>
  {{else}}<span class="dim">None declared. An item with no criteria cannot be judged, which is why
  the admission rules refuse one — this item predates that check or was written by hand.</span>{{end}}
</div>

<h2>What happened <span class="sub">every accepted move, oldest first</span></h2>
{{if .Steps}}
<div class="card"><ul class="walk">
  {{range .Steps}}<li>
    <div class="edge">{{.From}} → {{.To}}</div>
    <div class="what">{{.Says}}</div>
    <div class="who">{{if .Role}}<a href="/roles/{{.Role}}">{{.Role}}</a>{{else}}the control plane{{end}}
      {{if .RunID}} · <a href="/run/{{.RunID}}">{{.RunID}}</a>{{end}} · {{.When}}</div>
  </li>{{end}}
</ul></div>
{{else}}<div class="card dim">Nothing has moved yet.</div>{{end}}

{{if .Refusals}}
<h2>What was refused <span class="sub">oldest first, with what happened next</span></h2>
{{if .Outstanding}}<div class="banner warn"><b>{{plural .Outstanding "refusal is" "refusals are"}} still
standing.</b> The rest were corrected and the work came back. These are the ones nothing has answered
yet, so they are the reason this item is where it is rather than history.</div>{{end}}
<div class="banner calm">A refusal is the system working, not the work failing. A run asked for
something it had not earned — a stage skipped, a check that came back red, work judging itself — and
the authority said no and said why. The work was then corrected and came back, which is why this item
can be finished and still have red rows behind it. Both answers are kept deliberately: a record that
holds only what it accepted cannot answer the question it will actually be asked, which is why
something did <i>not</i> happen.</div>
{{range .Refusals}}
<div class="ref{{if .Cleared}} cleared{{end}}{{if .NeedsPerson}} yours{{end}}">
  <div class="top">
    <span class="n">{{.N}}</span>
    <span class="pill bad">{{.Reason}}</span>
    <span class="edge">{{.Edge}}</span>
    {{if .Cleared}}<span class="pill ok">cleared</span>
    {{else if .NeedsPerson}}<span class="pill warn">needs you</span>
    {{else}}<span class="pill mute">still open</span>{{end}}
    <span class="stamp">{{if .Role}}<a href="/roles/{{.Role}}">{{.Role}}</a> · {{end}}
      {{if .RunID}}<a href="/run/{{.RunID}}">{{.RunID}}</a> · {{end}}{{.When}}</span>
  </div>
  {{if .Means}}<div class="means"><b>What happened:</b> {{.Means}}.</div>{{end}}
  {{if .Next}}<div class="then"><b>What happens next:</b> {{.Next}}.
    {{if .Cleared}} <i>This one was cleared — the item moved on afterwards.</i>{{end}}</div>{{end}}
  {{if .Guarded}}<div class="kept"><b>What it prevented:</b> {{.Guarded}}.</div>{{end}}
  {{if .Detail}}<details class="rawwrap"><summary>what the authority said, verbatim</summary>
    <div class="raw">{{.Detail}}</div></details>{{end}}
</div>
{{end}}
{{end}}

{{if .NoRuns}}
<div class="banner"><b>Decisions with no runs behind them.</b> This item has recorded transitions but
no run rows, which means it was driven by hand or by a test rather than by a dispatched agent. Every
figure above that counts runs will read zero, and that is accurate rather than missing.</div>
{{end}}

<h2>Runs <span class="sub">{{.Passed}} passed, {{.Failed}} failed</span></h2>
<div class="wrap"><table>
  <tr><th>run</th><th>role</th><th>verdict</th><th>when</th><th>commit</th></tr>
  {{range .Runs}}<tr>
    <td class="mono"><a href="/run/{{.RunID}}">{{.RunID}}</a></td>
    <td class="dim"><a href="/roles/{{.WorkerType}}">{{.WorkerType}}</a></td>
    <td><span class="pill {{if .Finished}}{{verdictClass .Verdict}}{{else}}warn{{end}}">{{if .Finished}}{{.Verdict}}{{else}}UNKNOWN{{end}}</span></td>
    <td class="dim">{{ago .StartedMS}}</td>
    <td class="mono dim">{{short .HeadSHA}}</td>
  </tr>{{else}}<tr><td colspan="5" class="dim">No runs yet.</td></tr>{{end}}
</table></div>

{{if .Questions}}
<h2>Questions <span class="sub">what a run stopped on</span></h2>
{{range .Questions}}<div class="q">
  <h3>{{if .Blocking}}<span class="pill bad">blocking</span> {{end}}{{.Text}}</h3>
  <div class="dim mono small">{{.ID}}{{if .RaisedBy}} · <a href="/run/{{.RaisedBy}}">{{.RaisedBy}}</a>{{end}}</div>
  {{if .Lean}}<div class="lean"><b>Their recommendation:</b> {{.Lean}}</div>{{end}}
  {{if .Answer}}<div class="dim small" style="margin-top:5px"><b>Answered:</b> {{.Answer}}</div>
  {{else}}<div class="dim small" style="margin-top:5px">Unanswered —
    <a href="/questions">answer it on the Questions page</a>.</div>{{end}}
</div>{{end}}
{{end}}

{{if .Approvals}}
<h2>Approvals <span class="sub">what a person had to clear</span></h2>
<div class="wrap"><table>
  <tr><th>id</th><th>radius</th><th>plan</th><th>decision</th><th>who</th></tr>
  {{range .Approvals}}<tr>
    <td class="mono">{{.ID}}</td><td>{{.Radius}}</td>
    <td class="mono dim">{{short .PlanDigest}}</td>
    <td>{{if .Decided}}<span class="pill {{verdictClass .Verdict}}">{{.Verdict}}</span>
      {{else}}<span class="pill warn">waiting</span>{{end}}</td>
    <td class="dim">{{.Approver}}</td>
  </tr>{{end}}
</table></div>
{{end}}
{{end}}{{end}}
`
