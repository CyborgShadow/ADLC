package server

// The console page's own markup and styles.

const consoleCSS = `
.talk{display:flex;flex-direction:column;gap:12px;margin-bottom:18px}
.turn{background:var(--card);border:1px solid var(--line);border-radius:4px;overflow:hidden}
.turn .you{padding:11px 15px;border-bottom:1px solid var(--line);background:#101619}
.turn .you b{color:var(--dim);font-weight:600;font-size:12px;text-transform:uppercase;
letter-spacing:.06em;display:block;margin-bottom:3px}
.turn .them{padding:12px 15px;white-space:pre-wrap;word-break:break-word}
.turn .them.wait{color:var(--dim);font-style:italic}
.turn .them.fail{color:var(--bad)}
.acts{border-top:1px solid var(--line);background:#0d1215}
.act{padding:10px 15px;border-bottom:1px solid var(--line);display:flex;gap:12px;
align-items:baseline;flex-wrap:wrap}
.act:last-child{border-bottom:none}
.act .what{flex:1;min-width:220px}
.act .what code{color:var(--dim);font-size:11px}
.act .why{color:var(--dim);font-size:12px;margin-top:3px}
.act form{display:flex;gap:6px;align-items:center}
.ask{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:14px 15px}
.ask textarea{min-height:74px}
.ask .row{display:flex;gap:8px;margin-top:8px;align-items:center;flex-wrap:wrap}
.vocab{color:var(--dim);font-size:12px;white-space:pre-wrap;font-family:ui-monospace,Consolas,monospace}
`

const consoleHTML = `
{{if eq .Page "console"}}{{with .Body}}

{{if .Blocked}}<div class="banner bad"><b>The console cannot run.</b> {{.Blocked}}</div>{{end}}

<div class="banner calm">Talk to this tool instead of assembling the flags yourself. A turn is a
run like any other: it is dispatched as <span class="mono">{{.Worker}}</span>, it writes an
envelope, and everything it wants to happen goes through the same authority as everything a lane
produces — so it can do nothing here the rest of the system would refuse.
<br><br>Authority is <span class="pill {{if eq .Authority "full"}}bad{{else if eq .Authority "act"}}warn{{else}}mute{{end}}">{{.Authority}}</span>
— it {{.AuthWhat}}.{{if eq .Authority "full"}} <b>There is no human checkpoint left in this
project.</b> Every gate an agent cleared says so on the record.{{end}}</div>

<div class="talk">
{{range .Turns}}<div class="turn">
  <div class="you"><b>{{.AskedBy}} asked</b>{{.Asked}}</div>
  {{if .Running}}<div class="them wait">thinking… this page refreshes on its own; the answer
    appears when the turn finishes.</div>
  {{else if not .Replied}}<div class="them wait">This turn was interrupted and never answered.
    Nobody knows what it would have said, so nothing was recorded as if they did.</div>
  {{else if .Failure}}<div class="them fail">The turn failed: {{.Failure}}</div>
  {{else}}<div class="them">{{.Reply}}</div>{{end}}

  {{if .Actions}}<div class="acts">
    {{range .Actions}}<div class="act">
      <div class="what">
        <b>{{.Summary}}</b>
        {{if .Gated}}<span class="pill warn">yours to press</span>{{end}}
        <code>{{.Kind}}</code>
        {{if .Detail}}<div class="why">{{.Detail}}</div>{{end}}
      </div>
      {{if .Pressable}}
        <form method="post" action="/console/do">
          <input type="hidden" name="action" value="{{.ID}}">
          <input type="text" name="who" placeholder="your name" style="min-width:110px">
          <button type="submit" name="press" value="yes">Do it</button>
          <button type="submit" name="press" value="no" class="sec">Decline</button>
        </form>
      {{else}}<span class="pill {{verdictClass .Outcome}}">{{.Outcome}}</span>{{end}}
    </div>{{end}}
  </div>{{end}}
</div>{{else}}<div class="card dim">Nothing has been asked yet. Try
  <i>"what needs me right now?"</i> or <i>"why is S1-004 blocked?"</i></div>{{end}}
</div>

<form class="ask" method="post" action="/console/ask">
  <textarea name="text" placeholder="Ask for something, or ask what is going on." required></textarea>
  <div class="row">
    <input type="text" name="who" placeholder="your name">
    <button type="submit">Ask</button>
    <span class="dim small">The turn runs in the background. This page refreshes on its own.</span>
  </div>
</form>

<h2>What it can propose <span class="sub">the whole vocabulary; anything else is refused</span></h2>
<div class="card"><div class="vocab">{{.Actions}}</div></div>
<p class="dim small">Every exchange is on the ledger, including the turns that failed and the
actions you declined. A conversation that steers the fleet but leaves no record turns "why did this
happen" into a question nobody can answer.</p>
{{end}}{{end}}
`
