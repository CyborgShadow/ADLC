package server

// The "How the data is stored" section of the About page.
//
// It is a separate file for the same reason about_tmpl.go is: this is prose,
// and the page it extends is the only one written for somebody who does not
// already know how the system works. Somebody reading this section is usually
// deciding whether to trust the record, which is a different question from
// whether the lifecycle above it makes sense.
//
// Wiring: aboutDataHTML is a fragment. It renders inside the existing
// {{if eq .Page "about"}}{{with .Body}} block in aboutHTML and opens no block
// of its own, so it must be concatenated inside that one. It reads a single
// field, .Data, off the About page's body struct — set that field to
// aboutDataFacts().

import (
	"sort"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// aboutDataTable is one derived table and the question it answers.
type aboutDataTable struct {
	Name    string
	Answers string
}

// aboutDataGuard is one append-only trigger, named by what it refuses rather
// than by what it is called, because the name is the part nobody needs.
type aboutDataGuard struct {
	Name    string
	Refuses string
}

// aboutDataVerdict is one outcome of a verification pass, written for the
// person who has just been handed it. Then is the whole point: a verdict that
// does not tell an operator what to do next gets read as "something is wrong"
// and acted on as if every verdict were TAMPERED.
type aboutDataVerdict struct {
	Name  string
	Class string // pill colour; verdictClass does not know "STALE PROJECTION"
	Tier  string // the tier whose failure produces it, empty when none did
	Means string
	Then  string
}

// aboutDataInfo is everything the section needs. Whatever the ledger package
// exports is read from there rather than retyped, so a kind added to
// KnownKinds or a bump to SchemaVersion shows up here without anyone
// remembering to come and edit prose.
type aboutDataInfo struct {
	Path          string
	Driver        string
	SchemaVersion int
	Genesis       string
	Chain         string
	Anchor        string
	Meta          string
	BlobTable     string
	ChainColumns  []string
	Kinds         []string
	KindCount     int
	Projections   []aboutDataTable
	Guards        []aboutDataGuard
	BlobKinds     []string
	Verdicts      []aboutDataVerdict
	VerifyCmd     string
	RebuildCmd    string
}

// aboutDataFacts assembles the section's data.
//
// The projection table names and the trigger names are repeated here rather
// than read from the ledger package, because both lists are unexported and
// each entry needs a line of prose beside it that no exported value could
// supply. Everything that can be derived is: the event kinds come from
// ledger.KnownKinds, the schema version and the genesis hash from the
// constants the chain is actually built on, and the verdict strings from the
// constants the verifier prints, so the page cannot claim a verdict this build
// does not produce.
func aboutDataFacts() aboutDataInfo {
	kinds := make([]string, 0, len(ledger.KnownKinds))
	for k := range ledger.KnownKinds {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)

	return aboutDataInfo{
		Path:          ".adlc/ledger.db",
		Driver:        "modernc.org/sqlite",
		SchemaVersion: ledger.SchemaVersion,
		Genesis:       ledger.GenesisHash,
		Chain:         "adlc_event",
		Anchor:        "adlc_head",
		Meta:          "adlc_meta",
		BlobTable:     "adlc_blob",
		ChainColumns: []string{"seq", "ts_ms", "kind", "actor", "subject",
			"payload", "prev_hash", "hash"},
		Kinds:     kinds,
		KindCount: len(kinds),
		Projections: []aboutDataTable{
			{"adlc_segment", "the deliverables, and where each one has got to on the roadmap"},
			{"adlc_item", "every work item: its state, its acceptance criteria, and how many attempts it has had"},
			{"adlc_run", "one row per dispatch — who ran, against what, what it was given, what it cost"},
			{"adlc_proposal", "every state change and every new item an agent asked for, admitted or refused, with the reason"},
			{"adlc_question", "what an agent could not decide for itself, and the answer it was given"},
			{"adlc_approval", "what stopped for a person, which exact plan it named, and who decided"},
			{"adlc_gate", "what the control plane observed when it ran the project's checks itself"},
			{"adlc_worker", "the roles that have been registered, so that “never ran” is a row rather than an absence"},
			{"adlc_prompt_pin", "which bytes a prompt version resolves to"},
			{"adlc_note", "free text somebody attached to something"},
			{"adlc_loop_tick", "every firing of every scheduled lane, including the ones that found nothing to do"},
		},
		Guards: []aboutDataGuard{
			{"adlc_event_no_update", "changing any field of any event that has already been written"},
			{"adlc_event_no_delete", "removing an event"},
			{"adlc_head_monotonic", "moving the head anchor backwards"},
			{"adlc_head_no_delete", "removing the head anchor"},
			{"adlc_blob_no_update", "changing the bytes filed under a content hash"},
		},
		BlobKinds: []string{ledger.BlobPrompt, ledger.BlobEnvelope, ledger.BlobOutput},
		Verdicts: []aboutDataVerdict{
			{
				Name: string(ledger.VerdictIntact), Class: "ok",
				Means: "Every integrity check passed, every projection matches a replay of the chain, and this build understood every event it read.",
				Then:  "Nothing. This is the answer you want and the only one that needs no action.",
			},
			{
				Name: string(ledger.VerdictUnknown), Class: "warn",
				Tier:  string(ledger.TierKnowledge),
				Means: "The chain is fine. This binary is older than the record it was handed, and met an event kind or a schema version it cannot interpret.",
				Then:  "Upgrade the binary. Nothing has been altered — the record simply says more than this build knows how to read, and reporting that as corruption would make every upgrade look like an attack.",
			},
			{
				Name: string(ledger.VerdictStale), Class: "mute",
				Tier:  string(ledger.TierDerived),
				Means: "The chain is fine. A derived table no longer matches what this build derives from the chain, which is what an upgrade that changed a projection looks like.",
				Then:  "Run adlc ledger rebuild. The projections are thrown away and re-derived from the chain in a second. Nothing in the record is touched, because nothing in it is wrong.",
			},
			{
				Name: string(ledger.VerdictTampered), Class: "bad",
				Tier:  string(ledger.TierIntegrity),
				Means: "The record does not describe itself: a hash that does not match its row, a broken link, a gap in the sequence, an anchor that disagrees with the tip, a guard that has stopped refusing, or a blob whose contents no longer hash to its own address.",
				Then:  "Stop and look. This is the only verdict that accuses anyone, and it is kept that way on purpose — a detector that cries tamper at its own obsolescence gets ignored, and an ignored detector is not a control.",
			},
		},
		VerifyCmd:  "adlc ledger verify",
		RebuildCmd: "adlc ledger rebuild",
	}
}

const aboutDataCSS = `
.chips{display:flex;flex-wrap:wrap;gap:5px;margin:8px 0 12px}
.dia .acc2{stroke:#4bb3a8}
.dia .fmute{fill:#8fa3a5}
.dia .grp{fill:none;stroke:#253136;stroke-dasharray:4 4}
`

const aboutDataHTML = `
{{with .Data}}

<h2>How the data is stored</h2>
<p class="lede">Everything on every page of this dashboard comes out of <b>one file</b>:
<span class="mono">{{.Path}}</span>, sitting next to the repository it is running against. There is
no database server to install and nothing extra to keep running. The driver is
<span class="mono">{{.Driver}}</span> — SQLite rewritten in Go, compiled into this binary, no cgo
and no shared library to find at startup — so the tool is still one executable and a file. Backing
the record up is copying that file. Reading it in ten years is opening it with any SQLite there is.</p>
<p class="lede" style="margin-top:12px">The file is opened with one writer at a time and with
writes flushed before they are acknowledged. That costs a little speed on every append and buys the
thing the whole design rests on: a half-written event is a chain that no longer describes itself,
and a chain that no longer describes itself is indistinguishable from one somebody edited.</p>

<div class="grid">
  <div class="card"><div class="n">1</div><div class="l">file, no server</div></div>
  <div class="card"><div class="n">{{.KindCount}}</div><div class="l">event kinds this build reads</div></div>
  <div class="card"><div class="n">{{len .Projections}}</div><div class="l">derived tables, all disposable</div></div>
  <div class="card"><div class="n">{{len .Guards}}</div><div class="l">guards enforced by the database</div></div>
  <div class="card"><div class="n">v{{.SchemaVersion}}</div><div class="l">schema this build writes</div></div>
</div>

<h2>The chain is the record; everything else is derived</h2>
<p class="lede">One table holds the record: <span class="mono">{{.Chain}}</span>. Every decision this
system makes is appended to it as a row and nothing is ever changed afterwards. Each row carries
<span class="mono">{{join .ChainColumns}}</span>, and the hash covers the row's own fields
<i>and the previous row's hash</i>. That is what makes it a chain: altering an old event changes its
hash, which breaks the link every later event was built on, so a single edit anywhere is visible
from the end. The first event links to a hash of all zeroes.</p>

<p class="lede" style="margin-top:12px">The append-only rule is written in SQL, not in a code path,
because the code path is the thing under audit. An <span class="mono">UPDATE</span> or a
<span class="mono">DELETE</span> against the chain is refused by the database itself:</p>
<div class="wrap"><table>
  <tr><th>guard</th><th>refuses</th></tr>
  {{range .Guards}}<tr><td class="mono">{{.Name}}</td><td>{{.Refuses}}</td></tr>{{end}}
</table></div>
<p class="dim small">A control plane with a bug cannot quietly rewrite its own record, and neither
can anything else that happens to hold a connection to the file. Verification does not take their
presence on trust either: it attempts both writes inside a transaction it rolls back, and requires
the database to refuse. A guard that has gone quiet reads exactly like a clean tree.</p>

<h2>What appending one event does</h2>
<div class="dia">
<svg viewBox="0 0 940 300" role="img" aria-label="A payload is hashed onto the previous event's hash, then the event row, the head anchor and the projections are written in one transaction">
  <rect class="box" x="4" y="100" width="136" height="86" rx="4"/>
  <text class="lbl" x="72" y="126" text-anchor="middle">the payload</text>
  <text class="sub2" x="72" y="146" text-anchor="middle">canonical JSON:</text>
  <text class="sub2" x="72" y="162" text-anchor="middle">fixed field order,</text>
  <text class="sub2" x="72" y="178" text-anchor="middle">sorted map keys</text>

  <path class="arw" d="M144 143 h28" marker-end="url(#d1a)"/>

  <rect class="box" x="176" y="88" width="150" height="110" rx="4"/>
  <text class="lbl" x="251" y="114" text-anchor="middle">hash it</text>
  <text class="sub2" x="251" y="134" text-anchor="middle">sha256 over the</text>
  <text class="sub2" x="251" y="150" text-anchor="middle">previous event's hash</text>
  <text class="sub2" x="251" y="166" text-anchor="middle">and this row's fields</text>
  <text class="sub2 facc" x="251" y="186" text-anchor="middle">— that is the link</text>

  <path class="arw" d="M330 143 h28" marker-end="url(#d1a)"/>

  <rect class="grp" x="358" y="34" width="300" height="234" rx="4"/>
  <text class="lbl facc" x="508" y="56" text-anchor="middle">one transaction</text>

  <rect class="box" x="374" y="68" width="268" height="48" rx="4"/>
  <text class="lbl" x="508" y="88" text-anchor="middle">append the row</text>
  <text class="sub2" x="508" y="106" text-anchor="middle">{{.Chain}}, seq = previous + 1</text>

  <path class="arw" d="M508 116 v12" marker-end="url(#d1a)"/>

  <rect class="box" x="374" y="136" width="268" height="48" rx="4"/>
  <text class="lbl" x="508" y="156" text-anchor="middle">move the anchor</text>
  <text class="sub2" x="508" y="174" text-anchor="middle">{{.Anchor}}: the tip, written twice</text>

  <path class="arw" d="M508 184 v12" marker-end="url(#d1a)"/>

  <rect class="box" x="374" y="204" width="268" height="48" rx="4"/>
  <text class="lbl" x="508" y="224" text-anchor="middle">apply the projections</text>
  <text class="sub2" x="508" y="242" text-anchor="middle">the derived tables the pages read</text>

  <path class="arw ok2" d="M662 151 h30" marker-end="url(#d1g)"/>

  <rect class="box" x="696" y="98" width="240" height="112" rx="4"/>
  <text class="lbl fok" x="816" y="124" text-anchor="middle">commit, or none of it</text>
  <text class="sub2" x="816" y="146" text-anchor="middle">the row, the anchor and the</text>
  <text class="sub2" x="816" y="162" text-anchor="middle">tables land together, so a page</text>
  <text class="sub2" x="816" y="178" text-anchor="middle">can never show a fleet that is</text>
  <text class="sub2" x="816" y="194" text-anchor="middle">healthier than the record</text>

  <defs>
    <marker id="d1a" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#253136"/></marker>
    <marker id="d1g" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#5fbf8b"/></marker>
  </defs>
</svg>
</div>
<p class="dim small">The payload is serialised the same way every time — fixed field order, sorted
map keys, no HTML escaping — because the hash covers those bytes. Two encoders that disagree about
how to write a <span class="mono">&lt;</span> would produce two hashes for one event, and a chain
that cannot be recomputed is a chain nobody can check.</p>

<h2>Why the tip is written down twice</h2>
<p class="lede">The last event's hash is stored in a second place, in
<span class="mono">{{.Anchor}}</span>, outside the chain table. That looks redundant until you ask
what a chain protects against. Editing an event in the middle is caught, because every later hash
disagrees. <b>Truncating the end is not</b>: delete the final events and what remains is a shorter
chain that still walks perfectly, with no evidence of what was removed — the removal takes the
evidence of itself with it.</p>
<p class="lede" style="margin-top:12px">So the tip is recorded somewhere the chain does not reach.
A truncated chain no longer agrees with the anchor, and the anchor is only allowed to move forward.
Two rows that must tell the same story, kept apart on purpose, so that one of them has to be
missed.</p>

<h2>Projections are disposable</h2>
<p class="lede">The chain is not a pleasant thing to query, so it is replayed into ordinary tables —
one function, run once per append and again from an empty schema whenever the record is verified.
These are derived data. They carry no append-only guards, nothing outside the replay writes them,
and every row cites the event sequence it came from.</p>
<div class="wrap"><table>
  <tr><th>table</th><th>what it answers</th></tr>
  {{range .Projections}}<tr><td class="mono">{{.Name}}</td><td>{{.Answers}}</td></tr>{{end}}
</table></div>
<p class="dim small">Deleting all of them loses nothing. <span class="mono">{{.RebuildCmd}}</span>
re-derives every one from the chain, and the chain is untouched throughout — the guards on
<span class="mono">{{.Chain}}</span> stay in force during a rebuild, because a repair tool that
could rewrite the record would defeat the point of having one.</p>

<h2>The kinds of event that exist</h2>
<p class="lede">There are {{.KindCount}} of them in this build. They are strings rather than
numbers, so that a ledger written by a newer build is readable by an older one — and reported as
<i>not understood</i> rather than as corrupt.</p>
<div class="chips">{{range .Kinds}}<span class="pill mute mono">{{.}}</span>{{end}}</div>

<h2>Prompts and envelopes are kept under their own hash</h2>
<p class="lede">A run row records which prompt it was given and which envelope it produced, as
digests. On its own that is a claim about a hash nobody can check. So the bytes are retained too, in
<span class="mono">{{.BlobTable}}</span>, filed under the sha256 of their own contents —
{{join .BlobKinds}} — and the database refuses to change the bytes under an address. Identical
prompts across hundreds of runs cost one row, and a decision made three weeks ago can be re-derived
today against exactly the text it was made from. Without the bytes, “we know what this run was
told” is a sentence with nothing behind it.</p>

<h2>How the record checks itself</h2>
<div class="dia">
<svg viewBox="0 0 940 360" role="img" aria-label="Verification walks the chain, runs integrity, derived and knowledge checks, and the worst failing tier decides one of four verdicts">
  <rect class="box" x="4" y="190" width="136" height="76" rx="4"/>
  <text class="lbl" x="72" y="214" text-anchor="middle">walk the chain</text>
  <text class="sub2" x="72" y="234" text-anchor="middle">every event in order,</text>
  <text class="sub2" x="72" y="250" text-anchor="middle">from the genesis hash</text>

  <path class="arw" d="M144 206 h16 V102 H172" marker-end="url(#d2a)"/>
  <path class="arw" d="M144 228 h28" marker-end="url(#d2a)"/>
  <path class="arw" d="M144 250 h16 V310 H172" marker-end="url(#d2a)"/>

  <rect class="box" x="180" y="40" width="220" height="124" rx="4"/>
  <text class="lbl" x="290" y="64" text-anchor="middle">integrity</text>
  <text class="sub2" x="290" y="84" text-anchor="middle">sequence dense, links correct,</text>
  <text class="sub2" x="290" y="100" text-anchor="middle">hashes recomputed, the anchor</text>
  <text class="sub2" x="290" y="116" text-anchor="middle">agreeing with the tip, the guards</text>
  <text class="sub2" x="290" y="132" text-anchor="middle">observed refusing, every blob</text>
  <text class="sub2" x="290" y="148" text-anchor="middle">still hashing to its own address</text>

  <rect class="box" x="180" y="180" width="220" height="76" rx="4"/>
  <text class="lbl" x="290" y="204" text-anchor="middle">derived</text>
  <text class="sub2" x="290" y="224" text-anchor="middle">rebuild the projections from</text>
  <text class="sub2" x="290" y="240" text-anchor="middle">the chain, and diff them</text>

  <rect class="box" x="180" y="272" width="220" height="76" rx="4"/>
  <text class="lbl" x="290" y="296" text-anchor="middle">knowledge</text>
  <text class="sub2" x="290" y="316" text-anchor="middle">event kinds and a schema</text>
  <text class="sub2" x="290" y="332" text-anchor="middle">version this build can read</text>

  <path class="arw bad2"  d="M404 102 h10 V186 H432" marker-end="url(#d2r)"/>
  <path class="arw acc2"  d="M404 218 v-16 h28"      marker-end="url(#d2c)"/>
  <path class="arw warn2" d="M404 310 h22 V220 H432" marker-end="url(#d2w)"/>

  <rect class="box" x="440" y="150" width="150" height="90" rx="4"/>
  <text class="lbl" x="515" y="176" text-anchor="middle">one verdict</text>
  <text class="sub2" x="515" y="196" text-anchor="middle">the worst tier that</text>
  <text class="sub2" x="515" y="212" text-anchor="middle">failed decides it</text>

  <path class="arw ok2"   d="M594 170 h18 V50 H628"  marker-end="url(#d2g)"/>
  <path class="arw warn2" d="M594 182 h28 V128 H628" marker-end="url(#d2w)"/>
  <path class="arw acc2"  d="M594 208 h28 V212 H628" marker-end="url(#d2c)"/>
  <path class="arw bad2"  d="M594 220 h18 V296 H628" marker-end="url(#d2r)"/>

  <rect class="box" x="632" y="20" width="304" height="60" rx="4"/>
  <text class="lbl fok" x="784" y="44" text-anchor="middle">INTACT</text>
  <text class="sub2" x="784" y="64" text-anchor="middle">nothing to do</text>

  <rect class="box" x="632" y="92" width="304" height="72" rx="4"/>
  <text class="lbl fwarn" x="784" y="116" text-anchor="middle">UNKNOWN</text>
  <text class="sub2" x="784" y="136" text-anchor="middle">the chain is fine — this build is</text>
  <text class="sub2" x="784" y="152" text-anchor="middle">older than the record. Upgrade it.</text>

  <rect class="box" x="632" y="176" width="304" height="72" rx="4"/>
  <text class="lbl facc" x="784" y="200" text-anchor="middle">STALE PROJECTION</text>
  <text class="sub2" x="784" y="220" text-anchor="middle">the chain is fine — a derived table</text>
  <text class="sub2" x="784" y="236" text-anchor="middle">needs re-deriving. Rebuild it.</text>

  <rect class="box" x="632" y="260" width="304" height="72" rx="4"/>
  <text class="lbl fbad" x="784" y="284" text-anchor="middle">TAMPERED</text>
  <text class="sub2" x="784" y="304" text-anchor="middle">the record does not describe</text>
  <text class="sub2" x="784" y="320" text-anchor="middle">itself. Stop and look.</text>

  <defs>
    <marker id="d2a" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#253136"/></marker>
    <marker id="d2g" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#5fbf8b"/></marker>
    <marker id="d2w" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#d8a750"/></marker>
    <marker id="d2r" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#e0796d"/></marker>
    <marker id="d2c" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" fill="#4bb3a8"/></marker>
  </defs>
</svg>
</div>
<p class="dim small">Two questions are asked separately, and keeping them apart is the whole design.
“Does this record describe itself?” is about the file. “Can this binary read all of it?” is about
the binary. Answering them with one word meant that a build one release behind reported
LEDGER FAILED VERIFICATION over a chain whose hashes, anchor, replay and guards were all perfect.</p>

<h2>The four verdicts, and what each one asks of you</h2>
{{range .Verdicts}}<div class="stage">
  <div class="hd"><span class="pill {{.Class}}">{{.Name}}</span>
    <small>{{if .Tier}}{{.Tier}} check failed{{else}}every check passed{{end}}</small></div>
  <div>
    {{.Means}}
    <div class="gate"><b>What to do:</b> {{.Then}}</div>
  </div>
</div>{{end}}
<p class="dim small">Run <span class="mono">{{.VerifyCmd}}</span> to get one of these. The
separation costs an extra verdict and buys the only thing that matters about an alarm: that when it
goes off, somebody believes it. An alarm that fires on ordinary upgrades is one people learn to
ignore, which costs exactly the one time it was real.</p>

<h2>Changing the shape of the file</h2>
<p class="lede">The schema is forward-only. Nothing here drops, renames or retypes a column, because
the chain is the record and a migration that <i>can</i> lose data is a migration that eventually
does. Every statement is applied on every open, and its “duplicate column” error is the success
case — which is what makes running them repeatedly safe.</p>
<p class="lede" style="margin-top:12px">The obvious approach does not work, and this is worth saying
plainly because it is the kind of bug that hides for months.
<span class="mono">CREATE TABLE IF NOT EXISTS</span> does exactly nothing to a table that already
exists. Add a column to the schema and every ledger created before that day carries on without it:
new files are correct, old files are silently a version behind, and the tool only finds out when it
reads a column that is not there. So additive columns are separate
<span class="mono">ALTER TABLE</span> statements that run every time. This build writes schema
v{{.SchemaVersion}}, the number is stored in <span class="mono">{{.Meta}}</span>, and a build that
meets a file newer than itself says so before it says anything else.</p>

{{end}}
`
