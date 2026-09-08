package server

const homePageCSS = `
.hero{max-width:760px;margin:0 auto}
.hero h1{font-size:24px;font-weight:600;margin:0 0 4px;letter-spacing:-.02em}
.hero .lead{color:var(--dim);font-size:14px;margin-bottom:18px}
.hero .lead b{color:var(--ink);font-weight:600}
.need{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:16px}
.need a{background:var(--card);border:1px solid var(--line);border-radius:20px;
padding:6px 14px;font-size:13px;color:var(--ink);display:flex;gap:7px;align-items:center}
.need a:hover{border-color:var(--accent);text-decoration:none}
.need a .c{font-weight:600}
.need a.bad{border-left:3px solid var(--bad)}
.need a.warn{border-left:3px solid var(--warn)}
.need .calm2{color:var(--dim);font-size:13px;padding:6px 0}
.chat{background:var(--card);border:1px solid var(--line);border-radius:8px;overflow:hidden}
.chat .scroll{max-height:min(52vh,520px);overflow-y:auto;padding:16px 18px;
display:flex;flex-direction:column;gap:14px}
.chat .empty{color:var(--dim);padding:26px 18px;text-align:center;font-size:14px}
.chat .empty .eg{display:flex;gap:8px;justify-content:center;flex-wrap:wrap;margin-top:12px}
.chat .empty .eg span{background:#101619;border:1px solid var(--line);border-radius:14px;
padding:5px 12px;font-size:13px;color:var(--ink)}
.bubble .who3{font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:var(--dim);
margin-bottom:4px}
.bubble .say{white-space:pre-wrap;word-break:break-word;font-size:14px;line-height:1.6}
.bubble.mine .say{background:#101619;border:1px solid var(--line);border-radius:6px;padding:10px 13px}
.bubble .say.dimmed{color:var(--dim);font-style:italic}
.bubble .say.bad2{color:var(--bad)}
.did{border-left:2px solid var(--ok);padding:7px 0 7px 11px;margin-top:9px;font-size:13px}
.did.todo2{border-left-color:var(--warn)}
.did.nope{border-left-color:var(--line);color:var(--dim)}
.did b{display:block;margin-bottom:2px}
.did .sub3{color:var(--dim);font-size:12px}
.did form{display:flex;gap:6px;margin-top:7px;flex-wrap:wrap;align-items:center}
.chat .compose{border-top:1px solid var(--line);padding:14px 18px;background:#0f1518}
.chat .compose textarea{width:100%;min-height:74px;font-size:14px;line-height:1.5}
.chat .compose .row2{display:flex;gap:8px;margin-top:9px;align-items:center;flex-wrap:wrap}
.chat .compose button{padding:8px 20px}
.chat .compose .note{color:var(--dim);font-size:12px;margin-left:auto}
.hero .more{margin-top:20px;color:var(--dim);font-size:13px;text-align:center}
.adv{display:grid;gap:9px;grid-template-columns:repeat(auto-fit,minmax(300px,1fr))}
.adv a{background:var(--card);border:1px solid var(--line);border-radius:5px;
padding:13px 15px;color:var(--ink);display:block}
.adv a:hover{border-color:var(--accent);text-decoration:none}
.adv a b{display:flex;gap:8px;align-items:center}
.adv a .w{color:var(--dim);font-size:13px;margin-top:4px;display:block;line-height:1.5}
`

const homePageHTML = `
{{if eq .Page "home"}}{{with .Body}}
<div class="hero">
  <h1>{{.Project}}</h1>
  <div class="lead">Ask for what you want. The console reads the whole record, answers in plain
    language, and does what it can on its own — it is running as
    <a href="/roles/{{.Worker}}">{{.Worker}}</a> at authority
    <b>{{.Authority}}</b>, which means it {{.AuthWhat}}.</div>

  <div class="need">
    {{range .Waiting}}<a class="{{.Class}}" href="{{.Href}}"><span class="c">{{.N}}</span> {{.Label}}</a>{{end}}
    {{if .Quiet}}<span class="calm2">Nothing is waiting on you.</span>{{end}}
  </div>

  {{if .Blocked}}<div class="banner bad"><b>The console cannot answer.</b> {{.Blocked}}</div>{{end}}

  <div class="chat">
    <div class="scroll">
      {{range .Turns}}
        <div class="bubble mine"><div class="who3">{{.AskedBy}}</div><div class="say">{{.Asked}}</div></div>
        <div class="bubble">
          <div class="who3">console</div>
          {{if .Running}}<div class="say dimmed">thinking… this page refreshes on its own, so the
            answer appears here shortly.</div>
          {{else if not .Replied}}<div class="say dimmed">This turn was interrupted and never
            answered. Nobody knows what it would have said, so nothing was recorded as if they did.</div>
          {{else if .Failure}}<div class="say bad2">The turn failed: {{.Failure}}</div>
          {{else}}<div class="say">{{.Reply}}</div>{{end}}

          {{range .Actions}}<div class="did {{if .Pressable}}todo2{{else if eq .Outcome "executed"}}{{else}}nope{{end}}">
            <b>{{.Summary}}</b>
            <div class="sub3">{{.Human}}{{if .Detail}} — {{.Detail}}{{end}}</div>
            {{if .Pressable}}
              <form method="post" action="/console/do">
                <input type="hidden" name="action" value="{{.ID}}">
                <input type="hidden" name="back" value="/">
                <input type="text" name="who" placeholder="your name" style="min-width:110px">
                <button type="submit" name="press" value="yes">Do it</button>
                <button type="submit" name="press" value="no" class="sec">No thanks</button>
              </form>
            {{else}}<span class="pill {{verdictClass .Outcome}}">{{.Outcome}}</span>{{end}}
          </div>{{end}}
        </div>
      {{else}}
        <div class="empty">
          Nothing has been asked yet.
          <div class="eg">
            <span>what needs me right now?</span>
            <span>what is the fleet working on?</span>
            <span>start work on …</span>
            <span>why is nothing moving?</span>
          </div>
        </div>
      {{end}}
    </div>

    <form class="compose" method="post" action="/console/ask">
      <textarea name="text" placeholder="Ask for anything — or describe something you want built." required></textarea>
      <input type="hidden" name="back" value="/">
      <div class="row2">
        <input type="text" name="who" placeholder="your name">
        <button type="submit">Ask</button>
        <span class="note">Runs in the background; the answer appears above.</span>
      </div>
    </form>
  </div>

  <div class="more">Everything else — the roadmap, the record, the roles, the settings —
    is under <a href="/advanced">Advanced</a>.</div>
</div>
{{end}}{{end}}
`

const advancedPageHTML = `
{{if eq .Page "advanced"}}{{with .Body}}
<div class="banner calm">Every one of these is a projection of the same ledger. Home answers most
questions faster by asking; these are here for when you want to read the record yourself, change a
setting, or work out why something stopped.</div>
<div class="adv">
  {{range .Links}}<a href="{{.Href}}">
    <b>{{.Label}}{{if .Count}} <span class="pill {{if .Alarm}}warn{{else}}mute{{end}}">{{.Count}}</span>{{end}}</b>
    <span class="w">{{.What}}</span>
  </a>{{end}}
</div>
{{end}}{{end}}
`
