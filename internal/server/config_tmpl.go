package server

// The Config page.
//
// The version before this one explained every setting at length, in place, and
// showed forty of them — most read-only. The result was a page you scrolled
// past rather than used, with the two or three controls that actually do
// something buried among the prose.
//
// So: what you can change is at the top and is short. What you cannot change
// from here is folded away, present and findable but not competing. And the
// explanation of a setting sits behind it rather than in front of it — one
// line, on the row, saying what goes wrong at the wrong value.

const configPageCSS = `
.cfg{display:grid;gap:9px;margin-bottom:20px}
.cfg .box{background:var(--card);border:1px solid var(--line);border-radius:5px;padding:13px 15px}
.cfg .box>h3{margin:0 0 2px;font-size:14px;font-weight:600}
.cfg .box>.sub2{color:var(--dim);font-size:12px;margin-bottom:10px}
.cfg form{display:flex;gap:9px;flex-wrap:wrap;align-items:flex-end}
.fld{display:flex;flex-direction:column;gap:3px}
.fld label{color:var(--dim);font-size:11px;text-transform:uppercase;letter-spacing:.05em}
.fld select,.fld input{min-width:104px}
.fld label{display:flex;align-items:center}
select{background:#0c1114;border:1px solid var(--line);color:var(--ink);border-radius:3px;
padding:6px 9px;font:inherit;font-size:13px}
.lanes td{padding:5px 11px}
.lanes .nm{font-family:ui-monospace,Consolas,monospace;font-size:12px}
.lanes form{display:flex;gap:5px;align-items:center;margin:0}
.lanes input[type=number]{width:64px;padding:3px 6px}
.lanes button{padding:3px 9px;font-size:12px}
.levels{display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin-top:9px}
.lvl{border:1px solid var(--line);border-radius:4px;padding:9px 11px;font-size:12px}
.lvl.on{border-color:var(--accent)}
.lvl b{display:block;font-size:13px;margin-bottom:3px}
.lvl .d{color:var(--dim)}
details.ref2{background:var(--card);border:1px solid var(--line);border-radius:5px;
padding:0;margin-bottom:8px}
details.ref2>summary{padding:11px 15px;cursor:pointer;font-weight:600;font-size:13px;
list-style:none;display:flex;gap:9px;align-items:baseline}
details.ref2>summary::-webkit-details-marker{display:none}
details.ref2>summary::before{content:"▸";color:var(--dim);font-size:11px}
details.ref2[open]>summary::before{content:"▾"}
details.ref2>summary .c{color:var(--dim);font-weight:400;font-size:12px;margin-left:auto}
details.ref2 .in{padding:0 15px 13px}
.kv{display:grid;grid-template-columns:auto auto 1fr;gap:4px 14px;font-size:13px;align-items:baseline}
.kv .k{font-family:ui-monospace,Consolas,monospace;font-size:12px;color:var(--dim)}
.kv .v{font-family:ui-monospace,Consolas,monospace;font-size:12px}
.kv .m{color:var(--dim);font-size:12px}
@media (max-width:820px){.levels{grid-template-columns:1fr}.kv{grid-template-columns:1fr}}
`

const configPageHTML = `
{{if eq .Page "config"}}{{with .Body}}

<h2>What you can change here</h2>
<div class="cfg">

  <div class="box">
    <h3>Lanes<span class="tip" data-tip="A lane is a timer that picks up one kind of work. Saving rewrites the config and takes effect on that lane&#39;s next tick — no restart, and nothing already in flight is interrupted. The cadence floor is 15 seconds; below that a lane spends more time starting runs than doing them.">?</span></h3>
    {{if .AllDark}}<div class="sub2">Every lane reads NEVER RUN because the scheduler is not
      running. Start it with <span class="mono">adlc schedule run</span>.</div>{{end}}
    <div class="wrap"><table class="lanes">
      <tr><th>lane</th><th>drains</th><th>status</th><th>last fired</th>
        <th class="num">ticks<span class="tip" data-tip="Every firing writes a tick, including the ones that found nothing to do. That is what makes a silent lane detectable: no ticks means the lane stopped, not that there was no work.">?</span></th>
        <th class="num">dispatched</th><th>cadence · max per tick</th></tr>
      {{range .Lanes}}<tr>
        <td class="nm">{{.Name}}</td>
        <td class="dim small mono">{{.Scope}}</td>
        <td><span class="pill {{verdictClass .Status}}">{{.Status}}</span></td>
        <td class="dim small">{{.Since}}</td>
        <td class="num dim">{{.Ticks}}</td><td class="num dim">{{.Dispatches}}</td>
        <td><form method="post" action="/loop">
          <input type="hidden" name="name" value="{{.Name}}">
          <input type="checkbox" name="enabled" {{if .Enabled}}checked{{end}} title="running">
          <input type="number" name="every" value="{{.EverySeconds}}" min="15" title="seconds between firings">s
          <input type="number" name="max" value="{{.MaxPerTick}}" min="1" title="most dispatches per firing">max
          <button type="submit">Save</button>
        </form></td>
      </tr>{{end}}
    </table></div>
  </div>

  <div class="box">
    <h3>Safety<span class="tip" data-tip="A blast radius is how far a change reaches if it is wrong. Every work item declares one, and these four settings decide what has to happen before a change that size is applied to anything real.">?</span></h3>
    <form method="post" action="/config/blast">
      <div class="fld"><label>apply unattended up to<span class="tip" data-tip="The widest change the fleet applies with nobody watching. Anything above this stops on the Approvals page. Raising it trades a person&#39;s attention for speed, and the thing you lose is the last checkpoint before something real changes.">?</span></label>
        <select name="auto_apply_max">{{range .Radii}}<option value="{{.}}"
          {{if eq . $.Body.Blast.AutoApplyMax}}selected{{end}}>{{.}}</option>{{end}}</select></div>
      <div class="fld"><label>one named approver from<span class="tip" data-tip="From this radius up, an approval has to carry a person&#39;s name. Below it, an approval can be recorded without one — fine for a change that touches only source, useless as an audit trail for anything else.">?</span></label>
        <select name="named_approver_min">{{range .Radii}}<option value="{{.}}"
          {{if eq . $.Body.Blast.NamedApproverMin}}selected{{end}}>{{.}}</option>{{end}}</select></div>
      <div class="fld"><label>two approvers from<span class="tip" data-tip="From this radius up, two distinct people have to approve. It has to be at or above the one-approver setting, or the wider change would ask for less than the narrower one.">?</span></label>
        <select name="two_approvals_min">{{range .Radii}}<option value="{{.}}"
          {{if eq . $.Body.Blast.TwoApprovalsMin}}selected{{end}}>{{.}}</option>{{end}}</select></div>
      <div class="fld"><label>approval lasts (minutes)<span class="tip" data-tip="How long an approval stays good. Past it the apply is refused even on the same plan, because an approval given a week ago was given about a system that has since moved.">?</span></label>
        <input type="number" name="ttl" value="{{.Blast.ApprovalTTLMinutes}}" min="1"></div>
      <button type="submit">Save safety</button>
    </form>
    <div class="wrap" style="margin-top:11px"><table>
      <tr><th>radius</th><th>what it reaches</th><th>before it is applied</th></tr>
      {{range .Radii2}}<tr>
        <td class="mono">{{.Radius}}</td><td class="dim">{{.Reaches}}</td>
        <td><span class="pill {{.Class}}">{{.Requires}}</span></td>
      </tr>{{end}}
    </table></div>
  </div>

  <div class="box">
    <h3>Money<span class="tip" data-tip="Caps refuse a dispatch once recorded spend passes them. Blank or zero means unlimited, and is reported as unlimited everywhere — never as a cap of nothing, because &#39;no budget set&#39; and &#39;budget exhausted&#39; must not look alike.">?</span></h3>
    {{if .Money.Unpriced}}<div class="sub2"><b>{{.Money.Unpriced}} finished run(s) used a model with
      no price entry.</b> Their cost is unknown rather than zero, and is not in the figure below.</div>{{end}}
    <form method="post" action="/config/budget">
      <div class="fld"><label>$ per run<span class="tip" data-tip="Refuses a single run whose recorded usage would exceed this. Catches a runaway agent before it finishes rather than after.">?</span></label>
        <input type="text" name="per_run" value="{{.CapRun}}" placeholder="unlimited"></div>
      <div class="fld"><label>$ per day<span class="tip" data-tip="Once the day&#39;s recorded spend passes this, nothing more dispatches. The fleet stops rather than slows.">?</span></label>
        <input type="text" name="per_day" value="{{.CapDay}}" placeholder="unlimited"></div>
      <div class="fld"><label>$ per deliverable<span class="tip" data-tip="The same, scoped to one deliverable — so one runaway piece of work cannot spend the whole day&#39;s budget.">?</span></label>
        <input type="text" name="per_segment" value="{{.CapSegment}}" placeholder="unlimited"></div>
      <div class="fld"><label>default model<span class="tip" data-tip="Recorded on a run that did not name its own model, and used to price it. A model missing from the table below reports UNPRICED rather than free.">?</span></label>
        <input type="text" name="model" value="{{.Money.DefaultModel}}"></div>
      <button type="submit">Save caps</button>
    </form>
    <div class="dim small" style="margin-top:9px">Spent in the last 24 hours: <b>{{.Money.SpentToday}}</b>
      against a daily cap of {{.Money.DayCap}}.</div>
    <div class="wrap" style="margin-top:9px"><table>
      <tr><th>model</th><th>rate from<span class="tip left" data-tip="A rate you set is one somebody here checked. A default is one that shipped in this binary and may be out of date. Unpriced means neither, and a run on that model reports its cost as unknown rather than as zero.">?</span></th>
        <th class="num">input</th><th class="num">output</th>
        <th class="num">cache read</th><th class="num">cache write</th><th>set your own</th></tr>
      {{range .Money.Prices}}<tr>
        <td class="mono">{{.Model}}{{if .Default}} <span class="pill live">default model</span>{{end}}</td>
        <td><span class="pill {{if .Unpriced}}bad{{else if .FromDefaults}}warn{{else}}ok{{end}}">{{.Source}}</span></td>
        <td class="num">{{.In}}</td><td class="num">{{.Out}}</td>
        <td class="num dim">{{.CacheRead}}</td><td class="num dim">{{.CacheWrite}}</td>
        <td><form method="post" action="/config/price" class="lanes">
          <input type="hidden" name="model" value="{{.Model}}">
          <input type="number" step="0.01" name="in" placeholder="in" style="width:70px">
          <input type="number" step="0.01" name="out" placeholder="out" style="width:70px">
          <button type="submit">Set</button>
        </form></td>
      </tr>{{end}}
    </table></div>
    <div class="dim small" style="margin-top:6px">Dollars per million tokens.
      {{if .OnDefaults}}These are the rates that shipped in the binary — nobody here has confirmed
      them against your provider. Setting one replaces it with yours.{{end}}</div>
  </div>

  <div class="box">
    <h3>The console<span class="tip" data-tip="The panel on every page. It drafts actions from what you ask for; this setting decides how many of them it may carry out on its own. The three gates a person owns — signing off an idea, answering a blocking question, approving something irreversible — are only pressed by the console at full.">?</span></h3>
    <form method="post" action="/config/console">
      <div class="fld"><label>panel</label>
        <span><input type="checkbox" name="enabled" {{if .Console.Enabled}}checked{{end}}> on</span></div>
      <div class="fld"><label>authority</label>
        <select name="authority">{{range .Console.Levels}}<option value="{{.Name}}"
          {{if .Current}}selected{{end}}>{{.Name}}</option>{{end}}</select></div>
      <button type="submit">Save console</button>
      <span class="dim small">runs as <a href="/roles/{{.Console.Worker}}">{{.Console.Worker}}</a></span>
    </form>
    <div class="levels">
      {{range .Console.Levels}}<div class="lvl{{if .Current}} on{{end}}">
        <b>{{.Name}}{{if .Current}} <span class="pill warn">current</span>{{end}}</b>
        <div class="d">{{.Gives}}</div>
      </div>{{end}}
    </div>
  </div>

  <div class="box">
    <h3>Limits</h3>
    <form method="post" action="/config/dispatch">
      <div class="fld"><label>rework attempts<span class="tip" data-tip="How many times an item may be sent back before it escalates to a person instead of looping. An item that fails the same way five times is not going to pass on the sixth.">?</span></label>
        <input type="number" name="attempts" value="{{.Attempts}}" min="1"></div>
      <div class="fld"><label>run timeout (s)<span class="tip" data-tip="How long one agent may run. A run killed at this limit is recorded as UNKNOWN rather than failed, because nobody knows how it was going — which is accurate, and why setting this too low is expensive.">?</span></label>
        <input type="number" name="timeout" value="{{.Timeout}}" min="60"></div>
      <div class="fld"><label>page refresh (s)<span class="tip" data-tip="How often this dashboard reloads itself. It is also how quickly a console answer appears, since the panel has no JavaScript. 0 stops it refreshing.">?</span></label>
        <input type="number" name="refresh" value="{{.Refresh}}" min="0"></div>
      <button type="submit">Save limits</button>
    </form>
  </div>
</div>

<h2>Everything else <span class="sub">edited in {{.Path}}, then <span class="mono">adlc config check</span></span></h2>

<details class="ref2">
  <summary>Checks <span class="c">{{len .Checks}} declared</span></summary>
  <div class="in">
    <p class="dim small">Not editable here on purpose: a check is a command line, and a browser form
    that writes argv into something the control plane executes is a remote shell with extra steps.</p>
    <div class="wrap"><table>
      <tr><th>check</th><th>runs</th><th>verdict is</th><th>gates</th></tr>
      {{range .Checks}}<tr>
        <td class="mono">{{.ID}}</td>
        <td class="mono small dim">{{.Command}}</td>
        <td><span class="pill {{if .ExitDriven}}mute{{else}}live{{end}}">{{if .ExitDriven}}the exit code{{else}}the output{{end}}</span>
          <span class="dim small">{{.Verdict}}</span></td>
        <td class="dim small">{{if .Ungated}}<span class="pill warn">nothing</span>{{else}}{{join .Gates}}{{end}}</td>
      </tr>{{end}}
    </table></div>
  </div>
</details>

{{range .Groups}}
<details class="ref2">
  <summary>{{.Title}} <span class="c">{{.Sub}}</span></summary>
  <div class="in"><div class="kv">
    {{range .Rows}}<span class="k">{{.Key}}</span><span class="v">{{.Value}}</span><span class="m">{{.Means}}</span>{{end}}
  </div></div>
</details>
{{end}}

<p class="dim small">Roles and area routing are structure rather than policy, so they live in the
file next to the prompt files they name — see <a href="/roles">Roles</a> for what is declared and
what each one is given.</p>
{{end}}{{end}}
`
