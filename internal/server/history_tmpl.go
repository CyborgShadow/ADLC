package server

const historyPageCSS = `
.ev{background:var(--card);border:1px solid var(--line);border-radius:4px;margin-bottom:7px}
.ev>summary{padding:9px 13px;cursor:pointer;list-style:none;display:flex;gap:10px;
align-items:baseline;flex-wrap:wrap}
.ev>summary::-webkit-details-marker{display:none}
.ev>summary::before{content:"▸";color:var(--dim);font-size:11px}
.ev[open]>summary::before{content:"▾"}
.ev .seq{font-family:ui-monospace,Consolas,monospace;font-size:12px;color:var(--dim);min-width:44px}
.ev .kind{font-family:ui-monospace,Consolas,monospace;font-size:12px;color:var(--accent)}
.ev .says{flex:1;min-width:220px;font-size:13px}
.ev .stamp{color:var(--dim);font-size:12px;white-space:nowrap}
.ev .fields{border-top:1px solid var(--line);padding:10px 13px}
.fl{display:grid;grid-template-columns:170px 1fr;gap:4px 14px;font-size:13px;align-items:baseline}
.fl>dt{color:var(--dim);font-family:ui-monospace,Consolas,monospace;font-size:12px;
overflow-wrap:anywhere}
.fl>dd{margin:0;overflow-wrap:anywhere}
.fl>dd.prose{white-space:pre-wrap}
.fl>dd .said{border-left:2px solid var(--line);padding-left:9px;display:block}
.fl details{margin:0}
.fl details>summary{color:var(--accent);font-size:12px;cursor:pointer}
.fl pre{margin:5px 0 0;max-height:280px;font-size:12px}
.nest{border-left:1px solid var(--line);padding-left:11px;margin-top:4px}
.kindbar{display:flex;flex-wrap:wrap;gap:4px;margin-bottom:12px}
.kindbar a{font-family:ui-monospace,Consolas,monospace;font-size:11px;padding:3px 8px;
border-radius:3px;background:var(--card);border:1px solid var(--line);color:var(--dim)}
.kindbar a.on{border-color:var(--accent);color:var(--ink)}
.kindbar a .n{color:var(--dim);margin-left:4px}
@media (max-width:700px){.fl{grid-template-columns:1fr}}
`

// fieldsHTML renders a payload's decoded fields, and calls itself for nested
// ones. Defined separately so it can recurse — a payload that carries JSON
// inside a JSON string is normal here, not exotic.
const fieldsHTML = `
{{define "fields"}}<dl class="fl">
{{range .}}
  <dt>{{.Key}}</dt>
  <dd class="{{if .Prose}}prose{{end}}">
    {{if .Nested}}<div class="nest">{{template "fields" .Nested}}</div>
    {{else if .Long}}<details><summary>show</summary><pre>{{.Value}}</pre></details>
    {{else if .Link}}<a href="{{.Link}}">{{.Value}}</a>
    {{else if .Prose}}<span class="said">{{.Value}}</span>
    {{else}}{{.Value}}{{end}}
  </dd>
{{end}}
</dl>{{end}}
`

const historyPageHTML = `
{{if eq .Page "history"}}{{with .Body}}
<div class="tabs">
  <a href="/history?tab=runs" class="{{if eq .Tab "runs"}}on{{end}}">Runs</a>
  <a href="/history?tab=ledger" class="{{if eq .Tab "ledger"}}on{{end}}">Ledger</a>
</div>

{{if eq .Tab "ledger"}}
<div class="banner calm">Every event, newest first. Each row is hash-linked to the one before it, so
this is the record itself rather than a summary of it — the tables everywhere else in this
dashboard are derived from these rows and can be rebuilt from them.
<br><br>Open a row for what it actually recorded. Text an agent wrote is shown indented, because it
is worth knowing which words are the tool's and which are an agent's.</div>

<div class="kindbar">
  <a href="/history?tab=ledger" class="{{if not .Kind}}on{{end}}">all<span class="n">{{.Total}}</span></a>
  {{range .Kinds}}<a href="/history?tab=ledger&kind={{.Kind}}" class="{{if .On}}on{{end}}">{{.Kind}}<span class="n">{{.N}}</span></a>{{end}}
</div>

{{range .Rows}}
<details class="ev">
  <summary>
    <span class="seq">{{.Seq}}</span>
    <span class="kind">{{.Kind}}</span>
    {{if not .Known}}<span class="pill warn">unknown to this build</span>{{end}}
    <span class="says">{{if .Says}}{{.Says}}{{else}}{{.Subject}}{{end}}</span>
    <span class="stamp">{{.Actor}} · {{.When}}</span>
  </summary>
  <div class="fields">
    <dl class="fl">
      <dt>subject</dt>
      <dd>{{if .Link}}<a href="{{.Link}}">{{.Subject}}</a>{{else if .Subject}}{{.Subject}}{{else}}—{{end}}</dd>
      <dt>recorded by</dt><dd>{{.Actor}}</dd>
      <dt>when</dt><dd>{{.When}} · {{.Ago}}</dd>
      <dt>this row's hash</dt><dd class="mono">{{.Hash}}</dd>
      <dt>links back to</dt><dd class="mono">{{if .PrevHash}}{{.PrevHash}}{{else}}nothing — this is the first event{{end}}</dd>
    </dl>
    <div style="margin-top:9px">{{template "fields" .Fields}}</div>
  </div>
</details>
{{else}}<div class="card dim">No events match that filter.</div>{{end}}

{{if lt .Showing .Total}}<p class="dim small">Showing the {{.Showing}} most recent of {{.Total}}.
Use <span class="mono">adlc ledger events</span> for the whole chain.</p>{{end}}

{{else}}
<div class="banner calm">What each run actually did, in plain language. Open one to see what it was
given, what the control plane observed, and what it decided — and to re-derive that decision from
the record.</div>
<div class="wrap"><table>
  <tr><th>run</th><th>role</th><th>item</th><th>what it did</th><th class="num">cost</th><th>when</th></tr>
  {{range .Runs}}<tr>
    <td class="mono"><a href="/run/{{.RunID}}">{{.RunID}}</a></td>
    <td class="dim"><a href="/roles/{{.WorkerType}}">{{.WorkerType}}</a></td>
    <td class="mono">{{if .ItemID}}<a href="/item/{{.ItemID}}">{{.ItemID}}</a>{{end}}</td>
    <td><span class="pill {{.Class}}">{{if .Finished}}{{.Verdict}}{{else}}UNKNOWN{{end}}</span> {{.Headline}}</td>
    <td class="num">{{.Cost}}</td><td class="dim">{{.When}}</td>
  </tr>{{else}}<tr><td colspan="6" class="dim">No runs yet.</td></tr>{{end}}
</table></div>
{{end}}
{{end}}{{end}}
`
