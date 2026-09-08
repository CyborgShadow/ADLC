package server

// The About page's own markup and styles.
//
// It is kept in its own file because it is the one page that is prose rather
// than a projection, and mixing it into the templates that render the ledger
// makes both harder to change.

const aboutCSS = `
.lede{font-size:15px;line-height:1.65;max-width:72ch;color:var(--ink)}
.lede b{color:var(--accent);font-weight:600}
.rail{display:flex;gap:0;flex-wrap:wrap;margin:18px 0 6px;align-items:stretch}
.rail .st{flex:1 1 108px;min-width:108px;background:var(--card);border:1px solid var(--line);
border-radius:4px;padding:9px 11px;margin-right:16px;position:relative}
.rail .st:last-child{margin-right:0}
.rail .st:after{content:"";position:absolute;right:-13px;top:50%;width:10px;height:10px;
border-top:2px solid var(--line);border-right:2px solid var(--line);
transform:translateY(-50%) rotate(45deg)}
.rail .st:last-child:after{display:none}
.rail .st .k{font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--dim)}
.rail .st .v{font-weight:600;margin-top:2px}
.rail .st.term{border-color:#2b4636}
.rail .st.stop{border-color:#3d2a26}
.stage{background:var(--card);border:1px solid var(--line);border-radius:4px;
padding:14px 16px;margin-bottom:9px;display:grid;grid-template-columns:170px 1fr;gap:16px}
.stage .hd{font-weight:600}
.stage .hd small{display:block;color:var(--dim);font-weight:400;margin-top:3px}
.stage .gate{margin-top:7px;padding-left:11px;border-left:2px solid var(--accent);color:var(--dim);font-size:13px}
.stage .states{margin-top:7px}
.two{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.guard{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:13px 15px}
.guard b{display:block;margin-bottom:4px}
.guard span{color:var(--dim);font-size:13px}
.dia{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:14px;overflow-x:auto}
.dia svg{display:block;min-width:640px;max-width:100%;height:auto}
.dia text{font:12px ui-sans-serif,system-ui,"Segoe UI",Roboto,sans-serif}
.dia .box{fill:#0c1114;stroke:#253136}
.dia .lbl{fill:#dde6e6;font-weight:600}
.dia .sub2{fill:#8fa3a5;font-size:11px}
.dia .arw{stroke:#253136;stroke-width:2;fill:none}
.dia .ok2{stroke:#5fbf8b}.dia .bad2{stroke:#e0796d}.dia .warn2{stroke:#d8a750}
.dia .fok{fill:#5fbf8b}.dia .fbad{fill:#e0796d}.dia .fwarn{fill:#d8a750}
.dia .facc{fill:#4bb3a8}
@media (max-width:820px){.stage{grid-template-columns:1fr}.two{grid-template-columns:1fr}}
`

const aboutHTML = `
{{if eq .Page "about"}}{{with .Body}}

<p class="lede">This is an <b>agentic delivery lifecycle</b>. It sits alongside whatever you are
building — it is not part of your product and your product does not depend on it — and it runs the
loop that turns an idea into merged work: decide what to build, build it, check it, land it, learn
from it. Agents do the work. This tool decides what happens next, and writes down both.</p>

<p class="lede" style="margin-top:12px">The whole design follows from one sentence:
<b>an agent gets executed, reads a prompt, does a bounded job, and reports.</b> It never says what
state the work should move to, because a worker that can name its own next state can route around
every rule below. It says <span class="mono">pass</span>, <span class="mono">fail</span>,
<span class="mono">reject</span> or <span class="mono">blocked</span>, and everything after that is
arithmetic.</p>

<h2>The lifecycle at a glance</h2>
<div class="rail">
{{range .Stages}}{{if ne .Key "stopped"}}<div class="st{{if eq .Key "done"}} term{{end}}">
  <div class="k">{{.Key}}</div><div class="v">{{.Label}}</div>
</div>{{end}}{{end}}
</div>
<p class="dim small">Underneath those phases are {{.StateCount}} machine states. Most phases have
two — one for <i>waiting to be picked up</i>, one for <i>being worked on right now</i> — and that
split is the whole reason the board is readable: “four items waiting for review” and “four items in
review” are different problems and call for different responses.
An item can also leave the line at any point and sit in
<span class="pill mute">stopped</span>: blocked on a question, rejected, cancelled, or replaced.
Nothing is ever quietly dropped — a stopped item is a row somebody has to clear.</p>

<h2>What happens in each phase</h2>
{{range .Stages}}<div class="stage">
  <div class="hd">{{.Label}}<small>{{.Who}}</small></div>
  <div>
    {{.Plain}}
    {{if .Gate}}<div class="gate"><b>To leave:</b> {{.Gate}}.</div>{{end}}
    <div class="states dim mono small">{{join .States}}</div>
  </div>
</div>{{end}}

<h2>Before any of that: how an idea becomes work</h2>
<p class="lede">Deciding what to build and building it are different jobs with different failure
modes. A fleet that starts building the moment an idea is written down builds exactly what was
written — including the parts that do not add up to the thing that was wanted, which nobody finds
out until the deliverable is finished and wrong. So planning is its own pipeline, with its own
states, and work cannot be dispatched until a plan has been checked against the intent that
produced it.</p>
<div class="wrap"><table>
  <tr><th>state</th><th>what is happening</th><th>who</th></tr>
  {{range .Roadmap}}<tr>
    <td class="mono">{{.State}}{{if .Person}} <span class="pill warn">you</span>{{end}}</td>
    <td>{{.Plain}}</td><td class="dim mono small">{{.Who}}</td>
  </tr>{{end}}
</table></div>
<p class="dim small">Exactly one of those gates is a person's: <span class="mono">roadmap →
signed_off</span>. Everything else is done by an agent and decided by this tool.</p>

<h2>What one run actually is</h2>
<div class="dia">
<svg viewBox="0 0 940 250" role="img" aria-label="A lane picks work, an agent runs, the control plane gates and decides, and the ledger records it">
  <rect class="box" x="4" y="86" width="122" height="60" rx="4"/>
  <text class="lbl" x="65" y="110" text-anchor="middle">a lane fires</text>
  <text class="sub2" x="65" y="128" text-anchor="middle">on its schedule</text>

  <path class="arw" d="M130 116 h34" marker-end="url(#a)"/>

  <rect class="box" x="168" y="70" width="140" height="92" rx="4"/>
  <text class="lbl" x="238" y="94" text-anchor="middle">pick + lease</text>
  <text class="sub2" x="238" y="112" text-anchor="middle">nearest the finish</text>
  <text class="sub2" x="238" y="128" text-anchor="middle">line goes first</text>
  <text class="sub2" x="238" y="146" text-anchor="middle">state moves now</text>

  <path class="arw" d="M312 116 h34" marker-end="url(#a)"/>

  <rect class="box" x="350" y="70" width="150" height="92" rx="4"/>
  <text class="lbl" x="425" y="94" text-anchor="middle">the agent</text>
  <text class="sub2" x="425" y="112" text-anchor="middle">prompt + preamble,</text>
  <text class="sub2" x="425" y="128" text-anchor="middle">its own worktree,</text>
  <text class="sub2" x="425" y="146" text-anchor="middle">writes an envelope</text>

  <path class="arw" d="M504 116 h34" marker-end="url(#a)"/>

  <rect class="box" x="542" y="52" width="160" height="128" rx="4"/>
  <text class="lbl" x="622" y="76" text-anchor="middle">the control plane</text>
  <text class="sub2" x="622" y="96" text-anchor="middle">runs the checks itself</text>
  <text class="sub2" x="622" y="113" text-anchor="middle">compares claim to fact</text>
  <text class="sub2" x="622" y="130" text-anchor="middle">applies the rules</text>
  <text class="sub2" x="622" y="147" text-anchor="middle">computes the next state</text>
  <text class="sub2" x="622" y="167" text-anchor="middle">— never the agent</text>

  <path class="arw ok2" d="M706 90 h44 v-42 h44" marker-end="url(#g)"/>
  <path class="arw bad2" d="M706 142 h44 v50 h44" marker-end="url(#r)"/>

  <rect class="box" x="796" y="26" width="140" height="44" rx="4"/>
  <text class="lbl fok" x="866" y="46" text-anchor="middle">admitted</text>
  <text class="sub2" x="866" y="62" text-anchor="middle">it moves on</text>

  <rect class="box" x="796" y="170" width="140" height="44" rx="4"/>
  <text class="lbl fbad" x="866" y="190" text-anchor="middle">refused</text>
  <text class="sub2" x="866" y="206" text-anchor="middle">with a named reason</text>

  <path class="arw facc" d="M866 70 v40 M866 170 v-40" stroke="#4bb3a8" fill="none"/>
  <text class="sub2 facc" x="866" y="128" text-anchor="middle">both are</text>
  <text class="sub2 facc" x="866" y="144" text-anchor="middle">appended to the ledger</text>

  <defs>
    <marker id="a" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#253136"/></marker>
    <marker id="g" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#5fbf8b"/></marker>
    <marker id="r" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#e0796d"/></marker>
  </defs>
</svg>
</div>
<p class="dim small">The agent never touches the record. It writes one JSON file — an envelope —
saying what it did and what it ran. This tool re-runs those commands, compares what was claimed
against what it observed, and appends the outcome. A worker with write access to its own audit
record is not auditable, which is the whole reason the two are separate.</p>

<h2>Why a check has three answers</h2>
<div class="dia">
<svg viewBox="0 0 720 176" role="img" aria-label="A check resolves to green, red or unknown, and only green is a pass">
  <rect class="box" x="4" y="62" width="150" height="52" rx="4"/>
  <text class="lbl" x="79" y="84" text-anchor="middle">a declared check</text>
  <text class="sub2" x="79" y="102" text-anchor="middle">run by this tool</text>

  <path class="arw ok2"   d="M158 78 h60 v-42 h44" marker-end="url(#g2)"/>
  <path class="arw bad2"  d="M158 88 h30 v42 h74"  marker-end="url(#r2)"/>
  <path class="arw warn2" d="M158 88 h94"          marker-end="url(#w2)"/>

  <rect class="box" x="266" y="14" width="152" height="44" rx="4"/>
  <text class="lbl fok" x="342" y="34" text-anchor="middle">GREEN</text>
  <text class="sub2" x="342" y="50" text-anchor="middle">it ran, and it passed</text>

  <rect class="box" x="266" y="66" width="152" height="44" rx="4"/>
  <text class="lbl fwarn" x="342" y="86" text-anchor="middle">UNKNOWN</text>
  <text class="sub2" x="342" y="102" text-anchor="middle">it could not be run</text>

  <rect class="box" x="266" y="118" width="152" height="44" rx="4"/>
  <text class="lbl fbad" x="342" y="138" text-anchor="middle">RED</text>
  <text class="sub2" x="342" y="154" text-anchor="middle">it ran, and it failed</text>

  <path class="arw ok2"   d="M422 36 h60" marker-end="url(#g2)"/>
  <path class="arw warn2" d="M422 88 h60" marker-end="url(#w2)"/>
  <path class="arw bad2"  d="M422 140 h60" marker-end="url(#r2)"/>

  <rect class="box" x="490" y="14" width="226" height="44" rx="4"/>
  <text class="lbl fok" x="603" y="40" text-anchor="middle">the only thing that is a pass</text>

  <rect class="box" x="490" y="66" width="226" height="96" rx="4"/>
  <text class="lbl" x="603" y="92" text-anchor="middle">not a pass</text>
  <text class="sub2" x="603" y="112" text-anchor="middle">a missing tool, a host that</text>
  <text class="sub2" x="603" y="128" text-anchor="middle">did not answer, a suite that</text>
  <text class="sub2" x="603" y="144" text-anchor="middle">matched no tests — all of it</text>
  <text class="sub2" x="603" y="158" text-anchor="middle">stops the transition</text>

  <defs>
    <marker id="g2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#5fbf8b"/></marker>
    <marker id="r2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#e0796d"/></marker>
    <marker id="w2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#d8a750"/></marker>
  </defs>
</svg>
</div>
<p class="dim small">Two answers would be enough if every check could always be run. They cannot, and
the failure mode of collapsing three into two is always the same direction: a check that did not
happen reads as a check that passed. Nothing here treats an absence as evidence.</p>

<h2>The guards, and what each one prevents</h2>
<div class="two">
{{range .Guards}}<div class="guard"><b>{{.Name}}</b><span>{{.Prevents}}</span></div>{{end}}
</div>

<h2>The roles in this project <span class="sub">{{.Workers}} declared</span></h2>
<p class="lede">A role is a prompt file in the repository plus a list of what it is allowed to do.
The dispatcher carries no prompt of its own — it reads the file at dispatch time — so changing a
role is an ordinary commit somebody can review. Roles never talk to each other; a handoff is
arithmetic on recorded state, not a message somebody has to deliver, which is why one cannot be
dropped.</p>
{{range .Roster}}
<h2 style="margin-top:18px">{{.Layer}}</h2>
<div class="wrap"><table>
  <tr><th>role</th><th>what it does</th><th>can</th></tr>
  {{range .Roles}}<tr><td class="mono">{{.Type}}</td><td>{{.Does}}</td>
    <td class="dim mono small">{{.Can}}</td></tr>{{end}}
</table></div>
{{end}}

<h2>Running it</h2>
<p class="lede">There is no thread pool. Concurrency is <b>{{.Lanes}} scheduled lanes</b>, each
draining one kind of work on its own cadence, staggered so two never fire on the same second and
contend for the same claim. Every firing writes a tick whether or not it found anything, so a lane
that has quietly stopped shows up as a stale timestamp instead of looking like a lane with nothing
to report. Cadences are editable on the Config page and take effect on the next tick.</p>
<p class="lede" style="margin-top:12px">Stopping is safe at any point. Work happens in a throwaway
worktree, claims expire on their own, and a run that was killed halfway is recorded as
<span class="mono">UNKNOWN</span> rather than as a failure or a pass — because nobody knows which it
was, and pretending otherwise is how a record stops being worth reading.</p>
<p class="dim small">This project declares {{.Checks}} checks, {{.Workers}} roles and {{.Lanes}}
lanes. See <a href="/config">Config</a> for the live values, <a href="/roles">Roles</a> for the
prompt each role is given, and <a href="/coordination">Coordination</a> for the handoffs in order.</p>

{{end}}{{end}}
`
