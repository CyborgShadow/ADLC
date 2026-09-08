package server

// The Config page's own markup and styles.
//
// Kept apart from templates.go for the same reason the About page is: this one
// is mostly explanation, and mixing prose into the templates that project the
// ledger makes both harder to change without breaking the other.

const configPageCSS = `
.cfg-note{color:var(--dim);font-size:13px;max-width:84ch;margin:0 0 11px}
.cfg-note b{color:var(--ink);font-weight:600}
.cfg-lv{display:grid;gap:9px;grid-template-columns:repeat(auto-fit,minmax(230px,1fr));margin-bottom:8px}
.cfg-lv .l{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:12px 14px}
.cfg-lv .l.on{border-color:var(--accent)}
.cfg-lv .l .hd{display:flex;gap:7px;align-items:baseline;margin-bottom:6px;flex-wrap:wrap}
.cfg-lv .l .hd b{font-family:ui-monospace,Consolas,monospace}
.cfg-lv .l p{margin:0 0 6px;font-size:13px}
.cfg-lv .l p.adv{color:var(--dim);font-size:12px;margin:0}
.cfg-k{white-space:nowrap;font-family:ui-monospace,Consolas,monospace;font-size:12px}
.cfg-v{font-family:ui-monospace,Consolas,monospace;font-size:12px}
.cfg-m{color:var(--dim);font-size:13px}
.cfg-ctl{display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin:0}
.cfg-ctl label{display:flex;gap:5px;align-items:center;color:var(--dim);font-size:12px;white-space:nowrap}
@media (max-width:760px){.cfg-lv{grid-template-columns:1fr}}
`

const configPageHTML = `
{{if eq .Page "config"}}{{with .Body}}
<div class="banner calm">Two kinds of setting live here. <b>Lanes are editable from this page</b> and take
effect on the next tick. Everything else is edited in
{{if .HasPath}}<span class="mono">{{.Path}}</span>{{else}}the config file{{end}} and takes effect
when the fleet next loads it — it is shown anyway, with its value and its consequence, because a policy
you cannot see is one you cannot review.</div>

<h2>Lanes <span class="sub">the schedule — pause one, or change how often it fires</span></h2>
<p class="cfg-note">A lane is a timer that picks up work of one kind and dispatches it. Saving here rewrites
the config file and takes effect <b>on that lane's next tick</b> — no restart, and nothing already in flight
is interrupted. The floor is {{.CadenceFloor}} seconds: below that a lane spends more time starting runs than
running them. <b>Max per tick</b> is how many dispatches one firing may make, so it is the ceiling on how
fast this lane can spend money.</p>
<div class="wrap"><table>
  <tr><th>lane</th><th>status</th><th>scope</th><th>last fired</th>
      <th class="num">ticks</th><th class="num">dispatched</th><th style="width:330px">cadence and pause</th></tr>
  {{range .Lanes}}<tr>
    <td class="mono">{{.Name}}<div class="cfg-m small" style="max-width:34ch">{{.Note}}</div></td>
    <td><span class="pill {{verdictClass .Status}}">{{.Status}}</span></td>
    <td class="dim mono small">{{.Scope}}</td>
    <td class="dim">{{.Since}}</td>
    <td class="num dim">{{.Ticks}}</td>
    <td class="num dim">{{.Dispatches}}</td>
    <td><form class="cfg-ctl" method="post" action="/loop">
      <input type="hidden" name="name" value="{{.Name}}">
      <label><input type="checkbox" name="enabled" {{if .Enabled}}checked{{end}}> running</label>
      <label>every <input type="number" name="every" value="{{.EverySeconds}}" min="15"
        title="seconds between firings; 15 is the floor"> s</label>
      <label>max <input type="number" name="max" value="{{.MaxPerTick}}" min="1"
        title="dispatches this lane may make in one firing"> per tick</label>
      <button type="submit" class="sec">Save</button>
    </form></td>
  </tr>{{else}}<tr><td colspan="7" class="dim">No lanes are declared, so nothing is scheduled. Work only moves
    when somebody runs a dispatch by hand.</td></tr>{{end}}
</table></div>

<h2>The console <span class="sub">how much of what it drafts it may actually do — file-only{{if .HasPath}}, in {{.Path}}{{end}}</span></h2>
{{with .Console}}
<p class="cfg-note">The console is the panel that lets you drive this fleet by talking to it. It is
<b>{{if .Enabled}}on{{else}}off{{end}}</b>, running as <span class="mono">{{.Worker}}</span>, at authority
<b class="mono">{{.Authority}}</b>{{if not .Known}} <span class="pill bad">not a level this build knows — it would
execute nothing</span>{{end}}. Each turn is a real agent run: it costs money, it is bounded at
{{.Timeout}}s, and it is replayed the last {{.History}} turns because the agent has no memory between
invocations. Changing the level means editing <span class="mono">console.authority</span> in the file —
there is deliberately no control for it here, because a surface that can widen what agents may do from a
browser is one nobody can review.</p>
<div class="cfg-lv">
  {{range .Levels}}<div class="l{{if .Current}} on{{end}}">
    <div class="hd"><b>{{.Name}}</b>
      {{if .Current}}<span class="pill ok">current</span>{{end}}
      <span class="pill mute">executes {{.Executes}}</span></div>
    <p>{{.Gives}}</p>
    <p class="adv">{{.Advice}}</p>
  </div>{{end}}
</div>
<p class="cfg-note">The three gates a person owns are: signing off an idea, answering a blocking question,
and approving something irreversible. At <span class="mono">act</span> the console drafts those and leaves
them for you; at <span class="mono">full</span> it presses them itself, and the record says so on every one.</p>
{{end}}

<h2>Safety policy <span class="sub">what stops a change reaching a real machine unattended — file-only{{if .HasPath}}, in {{.Path}}{{end}}</span></h2>
<p class="cfg-note">A <b>blast radius</b> is how far a change reaches if it is wrong: none touches only
source, global touches everything. Every work item declares one, and these four settings decide what has to
happen before a change of that size is applied. The table below is what your current policy actually does —
not the settings, but their effect.</p>
<div class="wrap"><table>
  <tr><th>radius</th><th>what it reaches</th><th>before it is applied</th></tr>
  {{range .Radii}}<tr>
    <td class="mono">{{.Radius}}</td>
    <td class="cfg-m">{{.Reaches}}</td>
    <td><span class="pill {{.Class}}">{{.Requires}}</span></td>
  </tr>{{end}}
</table></div>
<p class="cfg-note">{{.Approval}}</p>
<div class="wrap"><table>
  <tr><th style="width:190px">setting</th><th style="width:110px">value</th><th>what you give up by moving it</th></tr>
  {{range .Blast}}<tr><td class="cfg-k">{{.Key}}</td><td class="cfg-v">{{.Value}}</td>
    <td class="cfg-m">{{.Means}}</td></tr>{{end}}
</table></div>

<h2>Money <span class="sub">the caps that refuse work, and the table that prices it — file-only{{if .HasPath}}, in {{.Path}}{{end}}</span></h2>
{{with .Money}}
{{if not .AnyPrices}}<div class="banner bad"><b>No model prices are configured.</b> Every run therefore reports
UNPRICED — cost unknown, not zero — so the daily cap can never be reached and the fleet has no throttle at
all. Set <span class="mono">budget.price_micros_per_mtok</span> before running this unattended.</div>
{{else if not .DefaultPriced}}<div class="banner bad">The default model
<span class="mono">{{.DefaultModel}}</span> has <b>no price entry</b>, so runs on it report UNPRICED rather
than a number. That is cost unknown, not cost nothing, and it is not counted against any cap.</div>{{end}}
<div class="grid">
  <div class="card"><div class="n">{{if .SpentToday}}{{.SpentToday}}{{else}}—{{end}}</div>
    <div class="l">spent, last 24h</div></div>
  <div class="card"><div class="n">{{.DayCap}}</div><div class="l">daily cap</div></div>
  <div class="card"><div class="n" style="font-size:15px;padding-top:6px">{{.DefaultModel}}</div>
    <div class="l">default model{{if not .DefaultPriced}} — UNPRICED{{end}}</div></div>
</div>
{{if .Unpriced}}<p class="cfg-note"><b>{{plural .Unpriced "finished run" "finished runs"}}</b> used a model with
no price entry, so their cost is unknown and is not in the figure above. First one:
<span class="mono"><a href="/run/{{.UnpricedRun}}">{{.UnpricedRun}}</a></span>.</p>{{end}}
<div class="wrap"><table>
  <tr><th style="width:190px">cap</th><th style="width:110px">value</th><th>what it does at that value</th></tr>
  {{range .Caps}}<tr><td class="cfg-k">{{.Key}}</td>
    <td class="cfg-v">{{if .Unlimited}}<span class="pill warn">unlimited</span>{{else}}{{.Value}}{{end}}</td>
    <td class="cfg-m">{{.Means}}</td></tr>{{end}}
</table></div>
{{if .Prices}}
<p class="cfg-note">Dollars per <b>million tokens</b>. A model absent from this table is not free — it reports
<b>UNPRICED</b>, its spend never counts against a cap, and the runs that used it are listed separately on the
Overview. These are a shipped default, not a fact: confirm them against your provider before you trust a
cost report.</p>
<div class="wrap"><table>
  <tr><th>model</th><th class="num">input</th><th class="num">output</th>
      <th class="num">cache read</th><th class="num">cache write</th></tr>
  {{range .Prices}}<tr>
    <td class="mono">{{.Model}}{{if .Default}} <span class="pill ok">default</span>{{end}}</td>
    <td class="num">{{.In}}</td><td class="num">{{.Out}}</td>
    <td class="num dim">{{.CacheRead}}</td><td class="num dim">{{.CacheWrite}}</td>
  </tr>{{end}}
</table></div>
{{end}}
{{end}}

<h2>Checks <span class="sub">what the control plane runs itself, and which channel it reads the answer from — file-only{{if .HasPath}}, in {{.Path}}{{end}}</span></h2>
<p class="cfg-note">These are run by the control plane, never by an agent, and an agent's account of one is
judged in the <b>same channel</b> the control plane read it in. That is the distinction the verdict rule
carries: for some commands the exit code is the answer, and for others the exit code says nothing at all and
the <b>output</b> is the answer. Read the wrong one and a truthful report gets refused while a doctored one
gets in.</p>
<div class="wrap"><table>
  <tr><th>check</th><th>what it runs</th><th>the verdict is</th><th>gates</th></tr>
  {{range .Checks}}<tr>
    <td class="mono">{{.ID}}
      <div class="cfg-m small">{{.Kind}}{{if .Binds}} · binds artifact{{end}} · timeout {{.Timeout}}</div>
      <div class="cfg-m small" style="max-width:30ch">{{.KindMeans}}</div></td>
    <td class="mono small">{{.Command}}{{if .Dir}}<div class="cfg-m small">in {{.Dir}}</div>{{end}}</td>
    <td style="max-width:44ch">
      <span class="pill {{if .ExitDriven}}live{{else}}warn{{end}}">{{if .ExitDriven}}the exit code{{else}}the output{{end}}</span>
      <span class="mono small">{{.Verdict}}</span>
      {{if .Detail}}<div class="mono small dim">{{.Detail}}</div>{{end}}
      <div class="cfg-m small">{{.Means}}</div></td>
    <td class="dim mono small">{{range .Gates}}{{.}}<br>{{end}}
      {{if .Ungated}}<span class="pill warn">gates nothing</span>
      <div class="cfg-m small" style="max-width:26ch">It runs on demand but no lifecycle edge requires it, so
        nothing is stopped when it fails.</div>{{end}}</td>
  </tr>{{else}}<tr><td colspan="4" class="dim">No checks are declared, which means the gate reports green over
    nothing. The config loader refuses this, so seeing it here is a defect.</td></tr>{{end}}
</table></div>

<h2>Everything else <span class="sub">the rest of the declared surface, and what each value costs you</span></h2>
<p class="cfg-note">None of this is editable from a browser, deliberately: this file is the fleet's policy, and
a surface that can rewrite policy from a page with no authentication is a surface that can quietly widen what
agents may do. Edit {{if .HasPath}}<span class="mono">{{.Path}}</span>{{else}}the config file{{end}}, then run
<span class="mono">adlc config check</span> — it validates the file and names, in words, anything that would
leave work sitting.</p>
{{range .Groups}}
<h2>{{.Title}} <span class="sub">{{.Sub}}</span></h2>
<div class="wrap"><table>
  <tr><th style="width:210px">setting</th><th style="width:190px">value</th><th>what it does</th></tr>
  {{range .Rows}}<tr><td class="cfg-k">{{.Key}}</td><td class="cfg-v">{{.Value}}</td>
    <td class="cfg-m">{{.Means}}</td></tr>{{end}}
</table></div>
{{end}}

<h2>Mandatory clauses <span class="sub">text every role prompt must carry verbatim; the gate refuses a commit that drops one</span></h2>
<div class="card">{{if .Clauses}}<ul style="margin:0;padding-left:18px">
  {{range .Clauses}}<li class="mono small">{{.}}</li>{{end}}
</ul>{{else}}<span class="dim">None declared. Nothing stops a run editing a safety clause out of its own
instructions, because there is nothing it is required to keep.</span>{{end}}</div>
{{end}}{{end}}
`
