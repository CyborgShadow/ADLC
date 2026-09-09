package server

const roadmapPageCSS = `
.deliv{background:var(--card);border:1px solid var(--line);border-radius:5px;
padding:15px 17px;margin-bottom:12px}
.deliv .top{display:flex;gap:11px;align-items:baseline;flex-wrap:wrap}
.deliv .top h3{margin:0;font-size:16px;font-weight:600}
.deliv .top .name{display:flex;gap:9px;align-items:baseline;color:var(--ink)}
.deliv .top .name:hover{color:var(--accent);text-decoration:none}
.deliv .top .name .mono{color:var(--dim)}
.deliv .top .count{margin-left:auto;color:var(--dim);font-size:13px;white-space:nowrap}
.deliv .intent{margin-top:8px;font-size:14px;line-height:1.6;max-width:80ch}
.deliv .whyfor{margin-top:5px;color:var(--dim);font-size:13px;max-width:80ch}
.deliv .waiting{margin-top:9px;color:var(--dim);font-size:13px}
.split{display:grid;grid-template-columns:repeat(3,1fr);gap:9px;margin-top:13px}
.col{background:#101619;border:1px solid var(--line);border-radius:4px;padding:9px 11px}
.col.done{border-color:#2b4636}
.col h4{margin:0 0 2px;font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.06em}
.col h4 .n{color:var(--dim);font-weight:400;margin-left:5px}
.col .why{color:var(--dim);font-size:11px;margin-bottom:7px}
.col ul{margin:0;padding:0;list-style:none;display:flex;flex-direction:column;gap:6px}
.col li{font-size:13px;line-height:1.4}
.col li .id{font-family:ui-monospace,Consolas,monospace;font-size:11px;color:var(--dim)}
.col li .st{display:block;color:var(--dim);font-size:11px}
.col .none{color:var(--dim);font-size:12px;font-style:italic}
.stuck{margin-top:11px;border-left:3px solid var(--bad);background:#1a1113;
border-radius:0 4px 4px 0;padding:9px 12px}
.stuck h4{margin:0 0 6px;font-size:12px;text-transform:uppercase;letter-spacing:.06em;color:var(--bad)}
.stuck li{list-style:none;font-size:13px;margin-bottom:5px}
.stuck ul{margin:0;padding:0}
.nobrief{margin-top:8px;border-left:2px solid var(--warn);padding-left:10px;
color:var(--dim);font-size:13px}
@media (max-width:820px){.split{grid-template-columns:1fr}}
`

const roadmapPageHTML = `
{{if eq .Page "roadmap"}}{{with .Body}}
<div class="banner calm">An idea is signed off, researched, decomposed, and the decomposition is
checked against the intent <b>before any of it is built</b>. Signing off is the one planning gate no
machine passes on its own; a deliverable at <b>planned</b> is waiting for the plan review, and
nothing under it is dispatched until it passes.</div>

{{range .Rows}}
<div class="deliv">
  <div class="top">
    <a class="name" href="/segment/{{.ID}}"><span class="mono">{{.ID}}</span><h3>{{.Title}}</h3></a>
    <span class="pill {{stateClass .State}}">{{.State}}</span>
    <span class="count">{{if .Progress.Total}}{{.Progress.Done}} of {{.Progress.Total}} done{{else}}no work items yet{{end}}</span>
  </div>

  {{if .Brief}}<div class="intent">{{.Brief}}</div>{{end}}
  {{if .Rationale}}<div class="whyfor"><b>Why it matters:</b> {{.Rationale}}</div>{{end}}
  {{if .Undescribed}}<div class="nobrief">Nobody wrote down what this is for. A deliverable with no
    intent cannot be researched or decomposed — a planner given only a title invents the scope, and
    nobody finds out until the work is finished and wrong. Add one with
    <span class="mono">adlc segment create</span> next time, or ask the console to add one.</div>{{end}}

  {{if .Progress.Total}}<div class="bar"><i style="width:{{.Progress.Percent}}%"></i></div>{{end}}
  <div class="waiting">{{.Next}}</div>

  {{if .Progress.Total}}
  <div class="split">
    {{range .Buckets}}<div class="col {{.Key}}">
      <h4>{{.Label}}<span class="n">{{len .Items}}</span></h4>
      <div class="why">{{.Why}}</div>
      {{if .Items}}<ul>
        {{range .Items}}<li>
          <a href="/item/{{.ID}}">{{.Title}}</a>
          <span class="id">{{.ID}}{{if .Area}} · {{.Area}}{{end}}</span>
          <span class="st">{{if not .Waiting}}{{.Says}}{{end}}{{if .Note}} — {{.Note}}{{end}}</span>
          {{template "deps" .}}
        </li>{{end}}
      </ul>{{else}}<div class="none">nothing here</div>{{end}}
    </div>{{end}}
  </div>
  {{end}}

  {{if .Trouble}}<div class="stuck">
    <h4>Not moving</h4>
    <ul>{{range .Trouble}}<li>
      <a href="/item/{{.ID}}">{{.Title}}</a>
      <span class="id mono">{{.ID}}</span> — {{.Note}}
    </li>{{end}}</ul>
  </div>{{end}}

  {{if .Parked}}<form class="inline" method="post" action="/resume">
    <input type="hidden" name="id" value="{{.ID}}">
    <input type="text" name="who" placeholder="your name">
    <input type="text" name="why" placeholder="what changed" style="flex:1;min-width:180px">
    <button type="submit" class="sec">Bring it back</button>
  </form>{{end}}

  {{if .NeedsYou}}<div class="decide">
    <div class="ask"><b>{{.AskLabel}}</b> {{.AskWhy}}</div>
    {{if .Approach}}<details class="approach" open><summary>The approach you are being asked about</summary><pre>{{.Approach}}</pre></details>{{end}}
    <form method="post" action="/signoff">
      <input type="hidden" name="id" value="{{.ID}}">
      <input type="hidden" name="to" value="{{.SignTo}}">
      <textarea name="why" placeholder="{{.WhyHint}}"></textarea>
      <div class="inline">
        <input type="text" name="who" placeholder="your name">
        <button type="submit" name="verdict" value="yes">{{.SignVerb}}</button>
        <button type="submit" name="verdict" value="no" class="sec">{{.DeclineVerb}}</button>
      </div>
      <div class="dim small" style="margin-top:5px">Your words are recorded as written and travel
        with this deliverable — a researcher reads them, and so does anybody asking months later why
        this was or was not built.</div>
    </form>
  </div>{{end}}
</div>
{{else}}<div class="card dim">No deliverables yet. Add one with a brief and a target, and a planner
will fill it:<br><span class="mono">adlc segment create -id S1 -title "…" -brief "…" -why "…" -target 5</span>
<br><br>Or ask the console for one — it will interview you for the brief.</div>{{end}}
{{end}}{{end}}
`
