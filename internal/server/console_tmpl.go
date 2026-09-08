package server

// The console is an overlay, not a page.
//
// It is the one surface you want while looking at something else — you read the
// roadmap, you have a question about the roadmap, you ask it without losing the
// roadmap. A tab makes you leave the thing you were asking about, and then the
// answer arrives somewhere the question no longer makes sense.
//
// Open or closed is remembered in a cookie rather than in JavaScript, so it
// survives navigation, the fifteen-second refresh, and a restart of the
// browser. The panel is a plain form; the transcript is plain HTML.

const consoleCSS = `
.dock{position:fixed;right:18px;bottom:18px;width:400px;max-width:calc(100vw - 36px);
z-index:40;display:flex;flex-direction:column;align-items:flex-end}
.dock .tab{background:var(--accent);color:#05201d;border-radius:20px;padding:9px 16px;
font-weight:600;font-size:13px;box-shadow:0 4px 14px rgba(0,0,0,.4);display:flex;gap:8px;align-items:center}
.dock .tab:hover{filter:brightness(1.08);text-decoration:none}
.dock .tab .n{background:#05201d;color:var(--accent);border-radius:9px;padding:0 6px;font-size:11px}
.dock .panel{background:var(--card);border:1px solid var(--line);border-radius:6px;width:100%;
box-shadow:0 10px 34px rgba(0,0,0,.5);display:flex;flex-direction:column;max-height:min(70vh,620px)}
.dock .head{display:flex;align-items:center;gap:8px;padding:9px 13px;border-bottom:1px solid var(--line)}
.dock .head b{font-size:13px}
.dock .head .shut{margin-left:auto;color:var(--dim);font-size:18px;line-height:1;padding:0 4px}
.dock .head .shut:hover{color:var(--ink);text-decoration:none}
.dock .log{overflow-y:auto;padding:11px 13px;display:flex;flex-direction:column;gap:10px;flex:1}
.dock .foot{border-top:1px solid var(--line);padding:10px 13px}
.dock textarea{width:100%;min-height:52px;font-size:13px}
.dock .foot .row{display:flex;gap:6px;margin-top:6px;align-items:center}
.dock .foot input[type=text]{flex:1;min-width:0}
.msg .who{font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:var(--dim);margin-bottom:3px}
.msg .body{white-space:pre-wrap;word-break:break-word;font-size:13px;line-height:1.5}
.msg.me .body{background:#101619;border:1px solid var(--line);border-radius:4px;padding:8px 10px}
.msg .body.wait{color:var(--dim);font-style:italic}
.msg .body.fail{color:var(--bad)}
.pact{border-left:2px solid var(--warn);padding:7px 0 7px 9px;margin-top:7px;font-size:13px}
.pact.ran{border-left-color:var(--ok)}
.pact.no{border-left-color:var(--line);color:var(--dim)}
.pact b{display:block;margin-bottom:2px}
.pact .why{color:var(--dim);font-size:12px}
.pact form{display:flex;gap:5px;margin-top:6px;flex-wrap:wrap}
.pact button{padding:4px 10px;font-size:12px}
.vocab{display:grid;gap:8px}
.vrow{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:11px 13px}
.vrow b{display:block}
.vrow .d{color:var(--dim);font-size:13px;margin-top:2px}
.vrow .m{color:var(--dim);font-size:11px;font-family:ui-monospace,Consolas,monospace;margin-top:4px}
.talk{display:flex;flex-direction:column;gap:10px;margin-bottom:18px}
.turn{background:var(--card);border:1px solid var(--line);border-radius:5px;overflow:hidden}
.turn .you{padding:11px 15px;border-bottom:1px solid var(--line);background:#101619}
.turn .you b{color:var(--dim);font-weight:600;font-size:11px;text-transform:uppercase;
letter-spacing:.06em;display:block;margin-bottom:3px}
.turn .them{padding:12px 15px;white-space:pre-wrap;word-break:break-word;line-height:1.55}
.turn .them.wait{color:var(--dim);font-style:italic}
.turn .them.fail{color:var(--bad)}
.acts{border-top:1px solid var(--line);background:#0d1215}
.act{padding:10px 15px;border-bottom:1px solid var(--line);display:flex;gap:12px;
align-items:baseline;flex-wrap:wrap}
.act:last-child{border-bottom:none}
.act .what{flex:1;min-width:220px}
.act .why{color:var(--dim);font-size:12px;margin-top:3px}
.act form{display:flex;gap:6px;align-items:center}
@media (max-width:560px){.dock{left:12px;right:12px;width:auto}}
`

// consoleDockHTML renders on every page, from the shell rather than from a page
// handler.
const consoleDockHTML = `
{{define "dock"}}{{with .Dock}}{{if .Show}}
<div class="dock">
{{if .Open}}
  <div class="panel">
    <div class="head">
      <b>Console</b>
      <span class="pill {{if eq .Authority "full"}}bad{{else if eq .Authority "act"}}warn{{else}}mute{{end}}">{{.Authority}}</span>
      <a class="shut" href="{{.CloseHref}}" title="Close">×</a>
    </div>
    <div class="log">
      {{if .Blocked}}<div class="msg"><div class="body fail">{{.Blocked}}</div></div>{{end}}
      {{range .Turns}}
        <div class="msg me"><div class="who">{{.AskedBy}}</div><div class="body">{{.Asked}}</div></div>
        <div class="msg">
          <div class="who">console</div>
          {{if .Running}}<div class="body wait">{{template "live" .}}</div>
          {{else if not .Replied}}<div class="body wait">This turn was interrupted and never answered.</div>
          {{else if .Failure}}<div class="body fail">The turn failed: {{.Failure}}</div>
          {{else}}<div class="body">{{.Reply}}</div>{{end}}
          {{range .Actions}}<div class="pact {{if .Pressable}}{{else if eq .Outcome "executed"}}ran{{else}}no{{end}}">
            <b>{{.Summary}}</b>
            <div class="why">{{.Human}}{{if .Detail}} — {{.Detail}}{{end}}</div>
            {{if .Pressable}}
              <form method="post" action="/console/do">
                <input type="hidden" name="action" value="{{.ID}}">
                <input type="hidden" name="back" value="{{$.Dock.Here}}">
                <button type="submit" name="press" value="yes">Do it</button>
                <button type="submit" name="press" value="no" class="sec">No</button>
              </form>
            {{else}}<span class="pill {{verdictClass .Outcome}}">{{.Outcome}}</span>{{end}}
          </div>{{end}}
        </div>
      {{else}}<div class="msg"><div class="body wait">Ask about anything on screen. Try “what needs me?”</div></div>{{end}}
    </div>
    <form class="foot" method="post" action="/console/ask">
      <textarea name="text" placeholder="Ask about what you are looking at…" required></textarea>
      <input type="hidden" name="back" value="{{.Here}}">
      <div class="row">
        <input type="text" name="who" placeholder="your name">
        <button type="submit">Ask</button>
      </div>
    </form>
  </div>
{{else}}
  <a class="tab" href="{{.OpenHref}}">Ask the console{{if .Pending}}<span class="n">{{.Pending}}</span>{{end}}</a>
{{end}}
</div>
{{end}}{{end}}{{end}}
`

// consoleHTML is the full-page view: the whole transcript, and the reference
// for what the console can do. Reachable from the panel, not from the nav.
const consoleHTML = `
{{if eq .Page "console"}}{{with .Body}}

{{if .Blocked}}<div class="banner bad"><b>The console cannot run.</b> {{.Blocked}}</div>{{end}}

<div class="banner calm">The whole conversation. You can ask from the panel on any page — this is
here for reading back what was said and what came of it.
<br><br>A turn is a run like any other: it is dispatched as <span class="mono">{{.Worker}}</span>,
it writes an envelope, and everything it wants to happen goes through the same authority as
everything a lane produces. It can do nothing here the rest of the system would refuse.</div>

<h2>What it is allowed to do <span class="sub">this project's setting</span></h2>
<div class="card">
  <b>{{.Authority}}</b> — it {{.AuthWhat}}.
  {{if eq .Authority "full"}}<div style="margin-top:6px" class="dim">There is no human checkpoint
  left in this project. Every gate an agent cleared says so on the record.</div>{{end}}
  <div class="dim small" style="margin-top:6px">Change it with <span class="mono">console.authority</span>
  in the config: <span class="mono">propose</span>, <span class="mono">act</span> or
  <span class="mono">full</span>.</div>
</div>

<h2>The conversation</h2>
<div class="talk">
{{range .Turns}}<div class="turn">
  <div class="you"><b>{{.AskedBy}} asked</b>{{.Asked}}</div>
  {{if .Running}}<div class="them wait">{{template "live" .}}</div>
  {{else if not .Replied}}<div class="them wait">This turn was interrupted and never answered.
    Nobody knows what it would have said, so nothing was recorded as if they did.</div>
  {{else if .Failure}}<div class="them fail">The turn failed: {{.Failure}}</div>
  {{else}}<div class="them">{{.Reply}}</div>{{end}}

  {{if .Actions}}<div class="acts">
    {{range .Actions}}<div class="act">
      <div class="what">
        <b>{{.Summary}}</b>
        {{if .Gated}}<span class="pill warn">yours to press</span>{{end}}
        <div class="why">{{.Human}}{{if .Detail}} — {{.Detail}}{{end}}</div>
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
</div>{{else}}<div class="card dim">Nothing has been asked yet.</div>{{end}}
</div>

<h2>What you can ask it to do</h2>
<div class="vocab">
{{range .Vocab}}<div class="vrow">
  <b>{{.Label}}{{if .Gated}} <span class="pill warn">a gate you own</span>{{end}}</b>
  <div class="d">{{.Detail}}</div>
  <div class="d dim small">{{.Who}}</div>
</div>{{end}}
</div>
<p class="dim small">Anything outside that list is refused with the reason, and the refusal is on the
record with everything else. Every exchange is recorded — including the turns that failed and the
actions you declined — because a conversation that steers the fleet but leaves no trace turns
"why did this happen" into a question nobody can answer.</p>
{{end}}{{end}}
`
