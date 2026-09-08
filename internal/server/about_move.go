package server

// "The control plane is supposed to move things forward — how, and where is the
// gap closed?" is the question this section answers, because it is the one
// thing about the design that is genuinely non-obvious.
//
// Nothing is handed from one role to the next. There is no queue between them
// and no message. An agent finishes, the tool writes a new state, and the agent
// exits — and what picks the work up is a different lane, on its own timer,
// asking "is there anything in a state my capability answers for?" The handoff
// is a fact about the record, not an event in flight, which is why it cannot be
// dropped: there is nothing to drop.
//
// The cost of that design is worth stating plainly rather than hiding, and this
// section states it: the delay between one stage finishing and the next
// starting is the next lane's cadence, and a state no lane drains is a state
// work stops in. Both are visible on the Config page rather than being things
// you find out.

const aboutMoveCSS = `
.mv{display:grid;gap:9px;margin:14px 0 6px}
.mv .step2{background:var(--card);border:1px solid var(--line);border-radius:4px;
padding:11px 14px;display:grid;grid-template-columns:26px 1fr;gap:13px;align-items:start}
.mv .step2 .num2{width:22px;height:22px;border-radius:50%;background:var(--line);
color:var(--ink);display:flex;align-items:center;justify-content:center;font-size:12px;font-weight:600}
.mv .step2 b{display:block;margin-bottom:2px}
.mv .step2 .d2{color:var(--dim);font-size:13px}
.mv .step2 .who2{color:var(--dim);font-size:11px;font-family:ui-monospace,Consolas,monospace;margin-top:4px}
.gapbox{background:var(--card);border:1px solid var(--line);border-left:3px solid var(--warn);
border-radius:0 4px 4px 0;padding:12px 15px;margin-bottom:9px}
.gapbox b{display:block;margin-bottom:3px}
.gapbox .d3{color:var(--dim);font-size:13px}
`

const aboutMoveHTML = `
{{if eq .Page "about"}}{{with .Body}}

<h2>How work actually moves</h2>
<p class="lede">Nothing is handed over. There is no queue between one role and the next, and no
message that could be lost — an agent finishes, the tool writes a new state, and the agent exits.
What picks the work up is a <b>different lane, on its own timer</b>, asking whether anything is
sitting in a state its capability answers for. The handoff is a fact about the record rather than
an event in flight, which is why it cannot go missing: there is nothing to go missing.</p>

<div class="dia">
<svg viewBox="0 0 940 320" role="img" aria-label="A lane fires, picks work, runs an agent, the control plane computes the next state, and a different lane picks it up on its own tick">
  <rect class="box" x="4" y="24" width="150" height="66" rx="4"/>
  <text class="lbl" x="79" y="48" text-anchor="middle">the judge lane</text>
  <text class="sub2" x="79" y="66" text-anchor="middle">fires every 180s</text>
  <text class="sub2" x="79" y="82" text-anchor="middle">asks: anything to judge?</text>

  <path class="arw" d="M158 57 h34" marker-end="url(#m1)"/>

  <rect class="box" x="196" y="16" width="164" height="82" rx="4"/>
  <text class="lbl" x="278" y="40" text-anchor="middle">S1-004 is ready_for_review</text>
  <text class="sub2" x="278" y="58" text-anchor="middle">CapabilityFor says that</text>
  <text class="sub2" x="278" y="74" text-anchor="middle">state needs judge work,</text>
  <text class="sub2" x="278" y="90" text-anchor="middle">so this lane takes it</text>

  <path class="arw" d="M364 57 h34" marker-end="url(#m1)"/>

  <rect class="box" x="402" y="24" width="150" height="66" rx="4"/>
  <text class="lbl" x="477" y="48" text-anchor="middle">the agent runs</text>
  <text class="sub2" x="477" y="66" text-anchor="middle">and reports one word:</text>
  <text class="sub2 fok" x="477" y="82" text-anchor="middle">pass</text>

  <path class="arw" d="M556 57 h34" marker-end="url(#m1)"/>

  <rect class="box" x="594" y="8" width="176" height="98" rx="4"/>
  <text class="lbl" x="682" y="32" text-anchor="middle">NextState()</text>
  <text class="sub2" x="682" y="52" text-anchor="middle">a pure function of</text>
  <text class="sub2" x="682" y="68" text-anchor="middle">state + capability +</text>
  <text class="sub2" x="682" y="84" text-anchor="middle">verdict + blast radius</text>
  <text class="sub2 facc" x="682" y="100" text-anchor="middle">→ ready_for_validation</text>

  <path class="arw" d="M774 57 h30 v96 h-30" marker-end="url(#m1)"/>
  <text class="sub2" x="856" y="52" text-anchor="middle">the agent has</text>
  <text class="sub2" x="856" y="68" text-anchor="middle">exited by now.</text>
  <text class="sub2" x="856" y="84" text-anchor="middle">nothing is waiting</text>
  <text class="sub2" x="856" y="100" text-anchor="middle">on it.</text>

  <rect class="box" x="594" y="196" width="176" height="82" rx="4"/>
  <text class="lbl" x="682" y="220" text-anchor="middle">the review lane</text>
  <text class="sub2" x="682" y="238" text-anchor="middle">fires every 240s</text>
  <text class="sub2" x="682" y="254" text-anchor="middle">asks its own question,</text>
  <text class="sub2" x="682" y="270" text-anchor="middle">finds S1-004, takes it</text>

  <path class="arw acc2" d="M594 237 h-34" marker-end="url(#m2)"/>

  <rect class="box" x="196" y="196" width="364" height="82" rx="4"/>
  <text class="lbl" x="378" y="220" text-anchor="middle">the gap between the two is a poll, not a handoff</text>
  <text class="sub2" x="378" y="240" text-anchor="middle">Worst case, the work waits one cadence of the next lane.</text>
  <text class="sub2" x="378" y="256" text-anchor="middle">Nothing coordinates them; each reads the same record and</text>
  <text class="sub2" x="378" y="272" text-anchor="middle">recognises its own work by the state it is in.</text>

  <defs>
    <marker id="m1" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#253136"/></marker>
    <marker id="m2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#4bb3a8"/></marker>
  </defs>
</svg>
</div>

<h2>Every pass, before it dispatches anything</h2>
<p class="lede">A lane firing does more than look for work. It first recomputes everything the tool
can work out for itself from the record — which is how the states that <i>no</i> capability answers
for still get moved.</p>

<div class="mv">
  <div class="step2"><div class="num2">1</div><div>
    <b>Open what is ready</b>
    <div class="d2">An item waiting on dependencies becomes dispatchable the moment the last one is
      done. Nothing has to notice and push it.</div>
    <div class="who2">queued → ready</div></div></div>

  <div class="step2"><div class="num2">2</div><div>
    <b>Resume what was blocked</b>
    <div class="d2">An item stopped on a question goes back to exactly the state it stopped in, once
      the last blocking question has an answer — whoever answered it, and however. If the record
      does not say what it was doing, it is reported rather than placed somewhere invented.</div>
    <div class="who2">blocked → wherever it came from</div></div></div>

  <div class="step2"><div class="num2">3</div><div>
    <b>Route rework</b>
    <div class="d2">A rejection goes back to a builder while the item still has attempts, and each
      round spends one. Past the budget it stays put, which is the limit doing its job — that item
      genuinely does need a person.</div>
    <div class="who2">rejected → in_progress</div></div></div>

  <div class="step2"><div class="num2">4</div><div>
    <b>Then look for work</b>
    <div class="d2">Candidates are sorted so whatever is nearest the finish line goes first. A fleet
      that starts new work ahead of finishing old work looks busy and delivers nothing.</div>
    <div class="who2">nearest the finish line first</div></div></div>
</div>

<h2>Where it can still stop</h2>
<p class="lede">Three places, and they are all visible rather than silent.</p>

<div class="gapbox">
  <b>A stage nothing drains</b>
  <div class="d3">If no enabled lane holds a capability, every item that reaches that stage sits
    there. <span class="mono">adlc config check</span> reports it, and the Config page shows every
    lane with what it drains — but the tool will not invent a lane for you.</div>
</div>
<div class="gapbox">
  <b>The scheduler is not running</b>
  <div class="d3">Lanes are timers in a process. With nothing running, nothing polls and nothing
    moves. Every lane reads NEVER RUN, and the banner at the top of every page says so, because a
    lane firing writes a tick even when it finds nothing.</div>
</div>
<div class="gapbox">
  <b>Something is waiting for you</b>
  <div class="d3">A blocking question, an approval, or an idea nobody has signed off. These are not
    failures — they are the checkpoints working, and they are counted in the banner so they cannot
    sit unnoticed.</div>
</div>
{{end}}{{end}}
`
