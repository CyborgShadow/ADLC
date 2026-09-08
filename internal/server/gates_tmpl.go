package server

// The two human gates, and why they are two.
//
// The operator asked why Questions and Approvals are separate pages when they
// look like the same thing: a list of things waiting on a person. They are not
// the same thing, and the dashboard was not saying so.
//
// A question is an agent stopping. It hit a decision it was not equipped to
// make — a design fork, an ambiguity in the spec — and the rule says raise it
// rather than guess, because a guess parked in a comment is a decision nobody
// reviewed. Any agent can raise one, about anything, at any point. Answering it
// unblocks analysis.
//
// An approval is the control plane stopping. Nothing is ambiguous; the work is
// finished and reaches something real, and policy says a named person must
// clear this exact plan before it is applied. It is raised by the machinery
// from the item's declared blast radius, not by an agent's judgement, and it is
// bound to a plan digest so that approving one plan and applying another is
// caught rather than trusted. Deciding it authorises consequence.
//
// Hence the split. Anyone can answer a question and the answer is advice;
// approving is signing your name to an irreversible act, and collapsing the two
// into one queue would let the second be cleared with the habits of the first.

// gatesCSS is only what these two pages add: the side-by-side header that
// contrasts them, and a settled state for an approval already decided.
const gatesCSS = `
.gates{display:grid;gap:10px;grid-template-columns:1fr 1fr;margin-bottom:18px}
.gates>div{background:var(--card);border:1px solid var(--line);border-left:3px solid var(--line);
border-radius:4px;padding:12px 15px}
.gates>div.here{border-left-color:var(--accent)}
.gates h3{margin:0 0 5px;font-size:13px;font-weight:600}
.gates p{margin:0 0 6px}
.gates p:last-child{margin-bottom:0}
.q.settled{border-left-color:var(--line)}
@media (max-width:700px){.gates{grid-template-columns:1fr}}
`

// questionsPageHTML replaces questionsHTML. Body is the same
// struct{ Items []ledger.Question } the questions handler already returns.
const questionsPageHTML = `
{{if eq .Page "questions"}}{{with .Body}}
<div class="gates">
  <div class="here">
    <h3>Questions — an agent could not decide</h3>
    <p>A run stopped because it hit a call it was not equipped to make: a design fork, a policy
      question, an ambiguity in the spec. The rule is to ask rather than guess, because a guess
      parked in a comment is a decision nobody ever reviewed.</p>
    <p class="dim small">Anyone can answer. Your answer is recorded word for word and unblocks the
      analysis the run was dispatched to do.</p>
  </div>
  <div>
    <h3>Not this page: <a href="/approvals">Approvals</a></h3>
    <p>An approval is not a question. Nothing there is unclear — the work is done and about to touch
      something real, and policy says a named person clears that specific plan first.</p>
    <p class="dim small">Answering a question unblocks thinking. Approving authorises consequence.</p>
  </div>
</div>
{{range .Items}}
<div class="q">
  <h3>{{if .Blocking}}<span class="pill bad">blocking</span> {{else}}<span class="pill mute">not blocking</span> {{end}}{{.Text}}</h3>
  <div class="dim mono small">{{.ID}}{{if .ItemID}} · <a href="/item/{{.ItemID}}">{{.ItemID}}</a>{{end}}
    {{if .RaisedBy}} · raised by <a href="/run/{{.RaisedBy}}">{{.RaisedBy}}</a>{{end}}</div>
  {{if .Lean}}<div class="lean"><b>Their recommendation:</b> {{.Lean}}</div>
  {{else}}<div class="lean dim">No recommendation offered. A question with no lean and no evidence hands
    back the analysis the run was dispatched to do — worth saying so when you answer it.</div>{{end}}
  {{if .Evidence}}<div class="dim small">{{.Evidence}}</div>{{end}}
  {{if .Blocking}}<div class="dim small">Nothing moves on this item until this is answered.</div>
  {{else}}<div class="dim small">Work continues without this. It was raised so the decision is on the
    record rather than made quietly inside a run.</div>{{end}}
  <form method="post" action="/answer">
    <input type="hidden" name="id" value="{{.ID}}">
    <textarea name="answer" placeholder="Your answer, recorded verbatim. Say why, not only what — the reasoning sets the severity of everything decomposed from it." required></textarea>
    <div class="inline"><input type="text" name="who" placeholder="your name">
      <button type="submit">Answer and unblock</button></div>
  </form>
</div>
{{else}}<div class="card dim">No open questions. Every run so far has had what it needed to decide.</div>{{end}}
{{end}}{{end}}
`

// approvalsPageHTML replaces approvalsHTML. Body is the same
// struct{ Rows []approvalRow } the approvals handler already returns, so each
// row carries both the ledger.Approval and the ledger.Item it is about.
const approvalsPageHTML = `
{{if eq .Page "approvals"}}{{with .Body}}
<div class="gates">
  <div class="here">
    <h3>Approvals — a change reaches something real</h3>
    <p>The work is finished and its declared blast radius is above what this project applies
      unattended, so the control plane stopped it. You are clearing <b>one specific plan</b>, named by
      the digest of the dry run below.</p>
    <p class="dim small">If the plan changes after you approve it, the approval stops applying and the
      apply is refused. That is the check that catches approving one plan and performing another.</p>
  </div>
  <div>
    <h3>Not this page: <a href="/questions">Questions</a></h3>
    <p>A question is an agent that could not decide something and stopped to ask, with its own
      recommendation attached. Anyone can answer one.</p>
    <p class="dim small">Nothing here is ambiguous. An approval is a person putting their name on an
      act with consequences outside the source tree.</p>
  </div>
</div>
{{range .Rows}}
<div class="q{{if .Decided}} settled{{end}}">
  <h3>{{if .Item.Title}}{{.Item.Title}}{{else}}<span class="dim">untitled item</span>{{end}}</h3>
  <div class="dim mono small">{{.ID}} · <a href="/item/{{.ItemID}}">{{.ItemID}}</a> ·
    radius <b>{{.Radius}}</b> · plan {{short .PlanDigest}} · requested {{ago .RequestedMS}}</div>
  {{if .Summary}}<div class="lean">{{.Summary}}</div>{{end}}
  {{if .Item.Resources}}<div class="dim small">touches: <span class="mono">{{join .Item.Resources}}</span></div>{{end}}
  {{if .Decided}}
    <div style="margin-top:7px"><span class="pill {{if eq .Verdict "approve"}}ok{{else}}bad{{end}}">{{.Verdict}}d</span>
      by {{if .Approver}}{{.Approver}}{{else}}<span class="dim">nobody named</span>{{end}} {{ago .DecidedMS}}{{if .Note}} — {{.Note}}{{end}}</div>
    <div class="dim small" style="margin-top:4px">This decision covers plan {{short .PlanDigest}} only.
      If the item is replanned it comes back here.</div>
  {{else}}
    <form class="inline" method="post" action="/decide">
      <input type="hidden" name="id" value="{{.ID}}">
      <input type="text" name="approver" placeholder="your name" required>
      <input type="text" name="note" placeholder="why, or any condition" style="flex:1;min-width:200px">
      <button type="submit" name="verdict" value="approve">Approve this plan</button>
      <button type="submit" name="verdict" value="reject" class="sec">Reject</button>
    </form>
    <div class="dim small" style="margin-top:5px">Your name is required. An approval nobody signed is an
      approval nobody gave, and this is the last gate before the change is performed.</div>
  {{end}}
</div>
{{else}}<div class="card dim">Nothing is waiting for approval. Either nothing in flight reaches past the
  source tree, or everything that did has already been decided.</div>{{end}}
{{end}}{{end}}
`
