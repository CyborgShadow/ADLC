package server

// The Roles page's markup and styles.
//
// Two columns, and the split is the point: the rail is every role the fleet
// has, the panel is the prompt for the one you picked. The old page put every
// prompt behind a second navigation and showed none of them here, which meant
// the answer to "what is this role actually told?" was always one page away.
//
// No JavaScript, like the rest of the dashboard. The selector is links, the
// editors are forms, and the long assembled text is a <details> element, which
// is markup rather than script.

const rolesPageCSS = `
.roles{display:grid;grid-template-columns:290px minmax(0,1fr);gap:18px;align-items:start}
.rail{display:flex;flex-direction:column;gap:14px;position:sticky;top:62px;max-height:calc(100vh - 84px);overflow-y:auto}
.rail .lay .lh{font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--dim);
font-weight:600;margin:0 0 5px 2px}
.rail .lay .lh small{display:block;text-transform:none;letter-spacing:0;font-weight:400;
font-size:12px;color:var(--dim);opacity:.8;margin-top:2px}
.rail a.rrole{display:block;background:var(--card);border:1px solid var(--line);border-left:3px solid var(--line);
border-radius:4px;padding:8px 11px;margin-bottom:5px;color:var(--ink)}
.rail a.rrole:hover{border-color:var(--accent);text-decoration:none}
.rail a.rrole.on{border-left-color:var(--accent);background:#182225}
.rail a.rrole .rt{font-weight:600;font-family:ui-monospace,Consolas,monospace;font-size:13px}
.rail a.rrole .rd{color:var(--dim);font-size:12px;line-height:1.4;margin-top:2px}
.rail a.rrole .rm{color:var(--dim);font-size:11px;font-family:ui-monospace,Consolas,monospace;margin-top:4px}
.panel{min-width:0}
.panel .hd .sub{font-weight:400;color:var(--dim);font-size:12px;margin-left:8px}
.facts{display:flex;flex-wrap:wrap;gap:6px;margin:8px 0 4px}
.facts .f{background:var(--card);border:1px solid var(--line);border-radius:3px;padding:4px 9px;font-size:12px}
.facts .f b{font-variant-numeric:tabular-nums}
.facts .f span{color:var(--dim)}
.ed textarea.promptbox{min-height:440px;font-family:ui-monospace,Consolas,monospace;font-size:12px;line-height:1.5}
.ed .foot{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-top:8px}
details.asm{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:9px 13px;margin-top:12px}
details.asm summary{cursor:pointer;font-weight:600;font-size:13px}
details.asm summary span{font-weight:400;color:var(--dim);margin-left:6px}
details.asm pre{margin-top:10px}
@media (max-width:860px){.roles{grid-template-columns:1fr}.rail{position:static;max-height:none}}
`

const rolesPageHTML = `
{{if eq .Page "roles"}}{{with .Body}}
<div class="banner calm">A role <b>is</b> its prompt file. The dispatcher reads it at dispatch time and
carries no prompt of its own, so what you edit here is what the next run is given — no build, no restart.
Pick a role to read the whole thing: the role file, and the text an agent actually receives once the
shared preamble is joined to it.</div>

{{if .NoLib}}<div class="banner bad"><b>No prompt library is loaded.</b> Nothing can be dispatched:
every worker resolves its instructions through <span class="mono">prompts.dir</span>, and that directory
could not be read. Fix the config before anything else on this page will mean much.</div>{{end}}
{{if .Unknown}}<div class="banner bad">Nothing here is called <span class="mono">{{.Unknown}}</span> —
not a role in the config, not a prompt in <span class="mono">{{.Dir}}</span>. A renamed role leaves old
links pointing at nothing; the rail below is the current list.</div>{{end}}
{{if .NeverRun}}<div class="banner">{{plural .NeverRun "role has" "roles have"}} never run.
That is counted from the registry, not inferred from silence — a worker that never runs never files a row
saying so, so its absence has to be computed from outside.</div>{{end}}

<div class="roles">
  <div class="rail">
  {{range .Layers}}<div class="lay">
    <div class="lh">{{.Label}}<small>{{.What}}</small></div>
    {{range .Roles}}<a class="rrole{{if .Selected}} on{{end}}" href="/roles/{{.Type}}">
      <div class="rt">{{.Type}}</div>
      {{if .Description}}<div class="rd">{{.Description}}</div>{{end}}
      <div class="rm">{{join .Caps}}{{if .Areas}} · {{join .Areas}}{{else}} · any area{{end}}</div>
      <div class="rm">{{.Runs}} run{{if ne .Runs 1}}s{{end}} · {{.Pass}} pass · {{.Refusals}} refused
        {{if .Missing}}<span class="pill bad">no prompt file</span>
        {{else if .Never}}<span class="pill {{if .LowCadence}}mute{{else}}bad{{end}}">never run</span>{{end}}</div>
    </a>{{end}}
  </div>{{end}}
  {{if .Orphans}}<div class="lay">
    <div class="lh">Unused prompt files<small>In the prompt directory, dispatched by nobody.</small></div>
    {{range .Orphans}}<a class="rrole" href="/roles/{{.ID}}">
      <div class="rt">{{.ID}}</div><div class="rm">{{.Path}}</div></a>{{end}}
  </div>{{end}}
  </div>

  <div class="panel">
  {{with .Sel}}
    <h2 class="hd">{{if .Type}}{{.Type}}{{else}}{{.PromptID}}{{end}}
      <span class="sub">{{if .Layer}}{{.Layer}} · {{end}}prompt <span class="mono">{{.PromptID}}</span>
      {{if .Version}}{{.Version}} · {{short .SHA}}{{end}}</span></h2>
    {{if .Description}}<p class="dim" style="max-width:78ch;margin:0 0 4px">{{.Description}}</p>{{end}}

    {{if .Missing}}
      <div class="banner bad"><b>This role has no prompt file.</b> {{.Why}} Nothing can be dispatched for
      it, and the lane that would have used it simply produces nothing — which looks the same as a quiet
      queue. Either add the file or take the role out of the config.</div>
    {{else}}
      <div class="facts">
        <div class="f"><b>{{.Runs}}</b> <span>runs</span></div>
        <div class="f"><b>{{.Pass}}</b> <span>passed</span></div>
        <div class="f"><b>{{.Fail}}</b> <span>failed</span></div>
        <div class="f"><b>{{.Refusals}}</b> <span>refused</span></div>
        <div class="f"><span>last run</span> <b>{{.Last}}</b></div>
        <div class="f"><span>spent</span> <b>{{.Cost}}</b></div>
        {{if .Never}}<div class="f"><span class="pill {{if .LowCadence}}mute{{else}}bad{{end}}">never run</span></div>{{end}}
      </div>
      <p class="dim small" style="margin:2px 0 0">
        can <span class="mono">{{join .Caps}}</span> ·
        {{if .Areas}}areas <span class="mono">{{join .Areas}}</span>{{else}}any area{{end}}
        {{if gt (len .UsedBy) 1}} · this file is also dispatched as
        <span class="mono">{{join .UsedBy}}</span>, so an edit here changes all of them{{end}}
        {{if .Vars}} · filled in per run: <span class="mono">{{join .Vars}}</span>{{end}}
      </p>

      {{if .Lost}}<div class="banner bad"><b>This role is missing a mandatory clause right now.</b>
      {{range .Lost}}<div class="mono small">{{.}}</div>{{end}}
      A clause can go missing from a role without that role's file changing, because the preamble carries
      most of them. The gate refuses a commit in this state.</div>{{end}}

      <h2>The role file <span class="sub">{{.Path}}</span></h2>
      <form class="ed" method="post" action="/prompt/save">
        <input type="hidden" name="id" value="{{.PromptID}}">
        <input type="hidden" name="back" value="/roles/{{if .Type}}{{.Type}}{{else}}{{.PromptID}}{{end}}">
        <textarea name="body" class="promptbox" spellcheck="false">{{.Body}}</textarea>
        <div class="foot">
          <button type="submit">Save this prompt</button>
          <span class="dim small">Writes the file. The next dispatch assembles from it; a run already in
          flight keeps the bytes it was given, which is why its record still matches what it was told.
          An edit that drops a mandatory clause is refused and nothing is written. The front matter —
          <span class="mono">id</span>, <span class="mono">version</span> — is kept as it is, because a
          past run's record names a version and rewriting it would point that record at text nobody ran.</span>
        </div>
      </form>

      <details class="asm">
        <summary>What an agent actually receives<span>preamble + this role, assembled at dispatch —
          variables left unsubstituted</span></summary>
        <pre>{{.Assembled}}</pre>
      </details>
    {{end}}
  {{else}}
    {{if not .Unknown}}<div class="card dim">Pick a role on the left. Its full prompt opens here —
      readable, and editable in place.
      <div style="margin-top:7px">Each card names what the role may do, which areas it owns, and how much
      it has actually run. A role with no runs is not a role that is between jobs: nothing has ever
      dispatched it, and that is usually a lane that is paused or a capability nobody claims.</div>
      <div style="margin-top:7px">Below the rail is the shared preamble. It is joined above every role at
      dispatch, so most of what any single agent is told is not in that role's file at all.</div>
    </div>{{end}}
  {{end}}
  </div>
</div>

<h2>The shared preamble <span class="sub">assembled into every role, which is why fleet policy is one edit</span></h2>
{{with .Preamble}}
  {{if .Absent}}
    <div class="card dim">This project declares no preamble file, so every role carries its own copy of
    any fleet-wide rule — and duplicated policy drifts the moment one copy is edited.</div>
  {{else}}
    {{if .Lost}}<div class="banner bad"><b>The preamble is missing a mandatory clause.</b>
    {{range .Lost}}<div class="mono small">{{.}}</div>{{end}}
    Any role that does not restate it is being dispatched without it.</div>{{end}}
    <p class="dim small" style="margin:0 0 8px">{{.Path}} · {{short .SHA}} · joined above every one of the
    {{.Roles}} declared roles, at the visible <span class="mono">---</span> seam. A role prompt is written to be
    read starting at that seam: what is above it is policy the role does not restate and cannot override.</p>
    <form class="ed" method="post" action="/preamble/save">
      <input type="hidden" name="back" value="/roles">
      <textarea name="body" class="promptbox" spellcheck="false">{{.Text}}</textarea>
      <div class="foot">
        <button type="submit">Save the preamble</button>
        <span class="dim small">One edit reaches every role, including the ones you are not looking at.
        Removing a mandatory clause is refused by name and by role, because this file is the only copy of
        it that most roles have.</span>
      </div>
    </form>
  {{end}}
{{end}}

{{if .Clauses}}
<h2>Mandatory clauses <span class="sub">must appear verbatim in what every role is given</span></h2>
<div class="card"><ul style="margin:0;padding-left:18px">
  {{range .Clauses}}<li class="mono small">{{.}}</li>{{end}}
</ul>
<p class="dim small" style="margin:9px 0 0">An edit on this page that removes one of these is refused and
nothing is written. A fresh session with no memory will re-derive a design from first principles, violate a
constraint nobody restated, and then defend the violation coherently — so the clause has to survive every
edit, including the ones made in a hurry from this dashboard.</p></div>
{{end}}
{{end}}{{end}}
`
