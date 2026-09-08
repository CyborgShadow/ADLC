// Package ledger is the append-only, hash-chained record of everything the
// delivery system did, and the only thing in this module that writes it.
//
// The rule it exists to enforce is one sentence long: a worker with write
// access to its own audit record is not auditable. Workers emit an envelope;
// the control plane decides what that envelope earns and appends the result. A
// refused proposal is appended too — the attempts that were declined are as
// much a part of the record as the ones that were admitted, and a system that
// only records its successes cannot be used to answer the question everyone
// eventually asks it, which is what went wrong.
//
// The chain is projected into ordinary tables for reading. Those projections
// are derived and are written by exactly one function, which Verify then
// re-runs from an empty schema and diffs. That makes "the reports agree with
// the record" a checked property rather than a convention.
package ledger

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// GenesisHash is the prev_hash of the first event.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Kind is an event kind. Kinds are strings rather than enum positions so that
// a ledger written by a newer build is readable — and reported as unknown
// rather than as corrupt — by an older one. See Verify.
type Kind string

// The event kinds this build understands.
const (
	KindSegmentCreated     Kind = "segment.created"
	KindItemCreated        Kind = "item.created"
	KindItemTransitioned   Kind = "item.transitioned"
	KindItemAmended        Kind = "item.amended"
	KindRunStarted         Kind = "run.started"
	KindRunFinished        Kind = "run.finished"
	KindRunActorCorrected  Kind = "run.actor_corrected"
	KindTransitionAdmitted Kind = "transition.admitted"
	KindTransitionRefused  Kind = "transition.refused"
	KindGateObserved       Kind = "gate.observed"
	KindQuestionRaised     Kind = "question.raised"
	KindQuestionAnswered   Kind = "question.answered"
	KindApprovalRequested  Kind = "approval.requested"
	KindApprovalDecided    Kind = "approval.decided"
	KindPromptPinned       Kind = "prompt.pinned"
	KindWorkerRegistered   Kind = "worker.registered"
	KindNoteRecorded       Kind = "note.recorded"
	KindItemProposed       Kind = "item.proposed"
	KindLoopTicked         Kind = "loop.ticked"
	KindSegmentAdvanced    Kind = "segment.advanced"
)

// KnownKinds is the set this build can project. Verify reports an event kind
// outside it as UNKNOWN, which is emphatically not TAMPERED: a stale binary
// meeting a newer ledger must not report a healthy chain as corrupt, because
// the one signal a tamper detector cannot afford to spend is a false positive.
var KnownKinds = map[Kind]bool{
	KindSegmentCreated: true, KindItemCreated: true, KindItemTransitioned: true,
	KindItemAmended: true, KindRunStarted: true, KindRunFinished: true,
	KindRunActorCorrected: true, KindTransitionAdmitted: true,
	KindTransitionRefused: true, KindGateObserved: true, KindQuestionRaised: true,
	KindQuestionAnswered: true, KindApprovalRequested: true, KindApprovalDecided: true,
	KindPromptPinned: true, KindWorkerRegistered: true, KindNoteRecorded: true,
	KindItemProposed: true, KindLoopTicked: true, KindSegmentAdvanced: true,
}

// ---------------------------------------------------------------- payloads

// The two roadmap states this package needs to name when it seeds a segment.
// The planning lifecycle itself lives in the authority package, which imports
// this one, so the strings are repeated rather than imported. Every other
// roadmap state arrives here as a recorded segment.advanced event.
const (
	segTheory = "theory"
	segReady  = "ready"
)

// SegmentCreated registers a build segment.
type SegmentCreated struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Brief is the intent a researcher works up and a planner decomposes. It is the whole of what
	// turns this from a backlog someone typed in into something that can be
	// pointed at a goal and left running.
	Brief string `json:"brief"`
	// TargetOpen is how many unfinished items the planner keeps in flight in
	// this segment. Zero means the segment is hand-filled and generation never
	// runs against it.
	TargetOpen int `json:"target_open"`
	// Rationale is why this deliverable matters, in the language of the person
	// who wanted it. It is what a run cites when it explains why its work
	// mattered to the product, months later.
	Rationale string   `json:"rationale"`
	Rank      int      `json:"rank"`
	DependsOn []string `json:"depends_on"`
}

// ItemCreated registers one independently verifiable unit of work.
type ItemCreated struct {
	ID        string   `json:"id"`
	SegmentID string   `json:"segment_id"`
	Title     string   `json:"title"`
	Area      string   `json:"area"`
	Radius    string   `json:"blast_radius"`
	Resources []string `json:"resources"`
	FileScope []string `json:"file_scope"`
	DependsOn []string `json:"depends_on"`
	Criteria  []string `json:"criteria"`
	// Rationale ties the item to the part of the deliverable it serves.
	Rationale string `json:"rationale"`
}

// ItemTransitioned is the only way an item's state changes.
type ItemTransitioned struct {
	ItemID      string `json:"item_id"`
	From        string `json:"from"`
	To          string `json:"to"`
	RunID       string `json:"run_id"`
	Reason      string `json:"reason"`
	PlanDigest  string `json:"plan_digest,omitempty"`
	BlockedWhy  string `json:"blocked_why,omitempty"`
	BumpAttempt bool   `json:"bump_attempt,omitempty"`
}

// SegmentAdvanced moves a deliverable along the roadmap.
type SegmentAdvanced struct {
	SegmentID string `json:"segment_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	RunID     string `json:"run_id,omitempty"`
	Why       string `json:"why"`
}

// ItemAmended corrects a field on an item. Corrections are appended with the
// authority for them, never applied by editing the row that was wrong: a
// record that can be silently rewritten is not a record.
type ItemAmended struct {
	ItemID string `json:"item_id"`
	Field  string `json:"field"`
	Value  string `json:"value"`
	Why    string `json:"why"`
}

// RunStarted records the facts that exist before a worker does anything, so
// that a run which produces nothing is still visible. A run exists only if the
// control plane wrote this row.
type RunStarted struct {
	RunID      string `json:"run_id"`
	WorkerType string `json:"worker_type"`
	ItemID     string `json:"item_id"`
	SegmentID  string `json:"segment_id"`
	PromptID   string `json:"prompt_id"`
	PromptSHA  string `json:"prompt_sha"`
	BaseSHA    string `json:"base_sha"`
	WorkDir    string `json:"workdir"`
	// Branch is the ref the run's commits live on. The merge queue needs it
	// months later, when the workspace that produced them is long gone.
	Branch string `json:"branch,omitempty"`
	Model  string `json:"model"`
}

// Usage is the token accounting for one run.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// RunFinished records the end facts, including what the run cost.
type RunFinished struct {
	RunID       string `json:"run_id"`
	Verdict     string `json:"verdict"`
	EnvelopeSHA string `json:"envelope_sha"`
	HeadSHA     string `json:"head_sha"`
	Artifact    string `json:"artifact"`
	Model       string `json:"model"`
	Usage       Usage  `json:"usage"`
	CostMicros  int64  `json:"cost_micros"`
}

// RunActorCorrected reattributes a run. Appended, never edited.
type RunActorCorrected struct {
	RunID string `json:"run_id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Why   string `json:"why"`
}

// TransitionOutcome is one proposal and the authority's answer to it.
type TransitionOutcome struct {
	RunID  string `json:"run_id"`
	ItemID string `json:"item_id"`
	Worker string `json:"worker"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// GateObserved is what the control plane saw when it ran the checks itself.
type GateObserved struct {
	RunID    string `json:"run_id"`
	ItemID   string `json:"item_id"`
	Edge     string `json:"edge"`
	TreeSHA  string `json:"tree_sha"`
	Dirty    bool   `json:"dirty"`
	Artifact string `json:"artifact"`
	Status   string `json:"status"`
	Checks   string `json:"checks"`
}

// QuestionRaised is how an agent asks for a decision. It carries the raiser's
// own lean and the evidence for it: a question with neither is not answerable
// and hands back the analysis the run was dispatched to do.
type QuestionRaised struct {
	ID       string `json:"id"`
	ItemID   string `json:"item_id"`
	Blocking bool   `json:"blocking"`
	Text     string `json:"text"`
	Lean     string `json:"lean"`
	Evidence string `json:"evidence"`
	RaisedBy string `json:"raised_by"`
}

// QuestionAnswered records the answer verbatim. Summarising a human's note
// loses the reasoning that sets the severity of everything decomposed from it.
type QuestionAnswered struct {
	ID         string `json:"id"`
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"`
}

// ApprovalRequested is raised when a change's blast radius exceeds what may be
// applied unattended. It names the plan digest it is asking about, so that
// applying a different plan than the one approved is detectable.
type ApprovalRequested struct {
	ID         string `json:"id"`
	ItemID     string `json:"item_id"`
	Radius     string `json:"radius"`
	PlanDigest string `json:"plan_digest"`
	Summary    string `json:"summary"`
}

// ApprovalDecided closes an approval request.
type ApprovalDecided struct {
	ID       string `json:"id"`
	Verdict  string `json:"verdict"`
	Approver string `json:"approver"`
	Note     string `json:"note"`
}

// PromptPinned records the exact bytes a run was given. A past run was given
// the text it was given; prompt versions are never edited in place, or the pin
// stops resolving and the run becomes unreproducible.
type PromptPinned struct {
	PromptID string `json:"prompt_id"`
	Version  string `json:"version"`
	SHA      string `json:"sha"`
	Path     string `json:"path"`
}

// WorkerRegistered seeds a declared worker so that "never ran" is a detectable
// condition rather than an absence of rows.
type WorkerRegistered struct {
	Type        string `json:"type"`
	Layer       string `json:"layer"`
	Description string `json:"description"`
	PromptID    string `json:"prompt_id"`
	LowCadence  bool   `json:"low_cadence"`
}

// NoteRecorded is a free-text action by the coordinating actor.
type NoteRecorded struct {
	About string `json:"about"`
	Text  string `json:"text"`
}

// ---------------------------------------------------------------- the store

// Ledger is an open ledger database.
type Ledger struct {
	db  *sql.DB
	now func() time.Time
}

// Event is one row of the chain.
type Event struct {
	Seq      int64
	TsMS     int64
	Kind     Kind
	Actor    string
	Subject  string
	Payload  []byte
	PrevHash string
	Hash     string
}

// Open opens or creates a ledger.
func Open(path string) (*Ledger, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create ledger directory %s: %w", dir, err)
		}
	}
	dsn := path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer at a time.
	db.SetMaxOpenConns(1)
	l := &Ledger{db: db, now: time.Now}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate (%s): %w", m, err)
		}
	}
	if err := l.ensureMeta(); err != nil {
		db.Close()
		return nil, err
	}
	return l, nil
}

// SetClock injects a clock. Tests need determinism; nothing else calls it.
func (l *Ledger) SetClock(f func() time.Time) { l.now = f }

// DB exposes the handle for read-only projections.
func (l *Ledger) DB() *sql.DB { return l.db }

// Close closes the database.
func (l *Ledger) Close() error { return l.db.Close() }

func (l *Ledger) ensureMeta() error {
	var v string
	err := l.db.QueryRow(`SELECT value FROM adlc_meta WHERE key='schema_version'`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		_, err = l.db.Exec(`INSERT INTO adlc_meta(key,value) VALUES('schema_version',?)`,
			strconv.Itoa(SchemaVersion))
		return err
	case err != nil:
		return err
	}
	return nil
}

// StoredSchemaVersion is the version recorded in the file, which may be newer
// than this build's.
func (l *Ledger) StoredSchemaVersion() (int, error) {
	var v string
	if err := l.db.QueryRow(`SELECT value FROM adlc_meta WHERE key='schema_version'`).Scan(&v); err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

// Head returns the chain tip as recorded by the anchor.
func (l *Ledger) Head() (seq int64, hash string, err error) {
	err = l.db.QueryRow(`SELECT seq, hash FROM adlc_head WHERE id=1`).Scan(&seq, &hash)
	if err == sql.ErrNoRows {
		return 0, GenesisHash, nil
	}
	return seq, hash, err
}

// canonical marshals a payload deterministically. The hash covers these bytes,
// so two runs of the same append must produce them identically: struct field
// order is fixed at compile time, map keys are sorted by encoding/json, and
// HTML escaping is off so that a `<` in a finding does not change the hash
// depending on which encoder wrote it.
func canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// HashEvent computes an event's hash. It is exported because Verify recomputes
// it, and because a caller auditing a ledger from outside this package must be
// able to reproduce the arithmetic without trusting the code that wrote it.
func HashEvent(prevHash string, seq, tsMS int64, kind Kind, actor, subject string, payload []byte) string {
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0x1e}) }
	write(prevHash)
	write(strconv.FormatInt(seq, 10))
	write(strconv.FormatInt(tsMS, 10))
	write(string(kind))
	write(actor)
	write(subject)
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// Append writes one event and applies its projection in the same transaction.
//
// The two must be atomic. If the projection could lag the chain, a report
// would be able to disagree with the record it is generated from, which is the
// exact condition that lets a status page say a fleet is healthy while the
// record says a third of it never ran.
func (l *Ledger) Append(actor string, kind Kind, subject string, payload any) (Event, error) {
	if strings.TrimSpace(actor) == "" {
		return Event{}, fmt.Errorf("append %s: actor is required — an unattributable row on an append-only chain cannot be corrected, only annotated", kind)
	}
	body, err := canonical(payload)
	if err != nil {
		return Event{}, fmt.Errorf("append %s: %w", kind, err)
	}
	tx, err := l.db.Begin()
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()

	var prevSeq int64
	prevHash := GenesisHash
	row := tx.QueryRow(`SELECT seq, hash FROM adlc_head WHERE id=1`)
	if err := row.Scan(&prevSeq, &prevHash); err != nil && err != sql.ErrNoRows {
		return Event{}, err
	}
	ev := Event{
		Seq: prevSeq + 1, TsMS: l.now().UnixMilli(), Kind: kind,
		Actor: actor, Subject: subject, Payload: body, PrevHash: prevHash,
	}
	ev.Hash = HashEvent(ev.PrevHash, ev.Seq, ev.TsMS, ev.Kind, ev.Actor, ev.Subject, ev.Payload)

	if _, err := tx.Exec(
		`INSERT INTO adlc_event(seq,ts_ms,kind,actor,subject,payload,prev_hash,hash) VALUES(?,?,?,?,?,?,?,?)`,
		ev.Seq, ev.TsMS, string(ev.Kind), ev.Actor, ev.Subject, string(ev.Payload), ev.PrevHash, ev.Hash,
	); err != nil {
		return Event{}, err
	}
	if prevSeq == 0 {
		_, err = tx.Exec(`INSERT INTO adlc_head(id,seq,hash,updated_ms) VALUES(1,?,?,?)`, ev.Seq, ev.Hash, ev.TsMS)
	} else {
		_, err = tx.Exec(`UPDATE adlc_head SET seq=?, hash=?, updated_ms=? WHERE id=1`, ev.Seq, ev.Hash, ev.TsMS)
	}
	if err != nil {
		return Event{}, err
	}
	if err := apply(tx, ev); err != nil {
		return Event{}, fmt.Errorf("project %s at seq %d: %w", kind, ev.Seq, err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// Events walks the chain in order. A zero limit means all of them.
func (l *Ledger) Events(from int64, limit int) ([]Event, error) {
	q := `SELECT seq,ts_ms,kind,actor,subject,payload,prev_hash,hash FROM adlc_event WHERE seq>=? ORDER BY seq`
	args := []any{from}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var kind, payload string
		if err := rows.Scan(&e.Seq, &e.TsMS, &kind, &e.Actor, &e.Subject, &payload, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		e.Kind = Kind(kind)
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- projection

// apply is the only function that writes a projection table. Replay calls it
// over an empty schema; Append calls it once per event. Verify relies on those
// two paths being the same code.
func apply(tx *sql.Tx, ev Event) error {
	dec := func(v any) error { return json.Unmarshal(ev.Payload, v) }
	switch ev.Kind {

	case KindSegmentCreated:
		var p SegmentCreated
		if err := dec(&p); err != nil {
			return err
		}
		// A deliverable with a brief enters the planning pipeline as a theory:
		// it gets accepted onto the roadmap, signed off by a person, researched,
		// decomposed and validated before any of it is built.
		//
		// A deliverable with NO brief was filled in by hand. There is no agent
		// breakdown to research, decompose or review, and nothing to wait for —
		// holding it would stall work a person wrote themselves, waiting on a
		// planning pass nobody is going to run. So it opens for work directly.
		state := string(segReady)
		if strings.TrimSpace(p.Brief) != "" {
			state = string(segTheory)
		}
		_, err := tx.Exec(`INSERT INTO adlc_segment(id,title,brief,rationale,state,rank,target_open,depends_on,created_seq,updated_seq)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Title, p.Brief, p.Rationale, state, p.Rank,
			p.TargetOpen, jsonList(p.DependsOn), ev.Seq, ev.Seq)
		return err

	case KindItemCreated:
		var p ItemCreated
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO adlc_item(id,segment_id,title,area,state,blast_radius,resources,file_scope,depends_on,criteria,rationale,attempts,created_seq,updated_seq)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,
			p.ID, p.SegmentID, p.Title, p.Area, "queued", p.Radius,
			jsonList(p.Resources), jsonList(p.FileScope), jsonList(p.DependsOn), jsonList(p.Criteria),
			p.Rationale, ev.Seq, ev.Seq)
		return err

	case KindItemTransitioned:
		var p ItemTransitioned
		if err := dec(&p); err != nil {
			return err
		}
		bump := 0
		if p.BumpAttempt {
			bump = 1
		}
		_, err := tx.Exec(`UPDATE adlc_item SET state=?, blocked_why=?, plan_digest=CASE WHEN ?<>'' THEN ? ELSE plan_digest END,
			attempts=attempts+?, updated_seq=? WHERE id=?`,
			p.To, p.BlockedWhy, p.PlanDigest, p.PlanDigest, bump, ev.Seq, p.ItemID)
		return err

	case KindItemAmended:
		var p ItemAmended
		if err := dec(&p); err != nil {
			return err
		}
		col, ok := amendableColumns[p.Field]
		if !ok {
			return fmt.Errorf("field %q is not amendable", p.Field)
		}
		_, err := tx.Exec(`UPDATE adlc_item SET `+col+`=?, updated_seq=? WHERE id=?`, p.Value, ev.Seq, p.ItemID)
		return err

	case KindRunStarted:
		var p RunStarted
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO adlc_run(run_id,worker_type,actor,item_id,segment_id,prompt_id,prompt_sha,base_sha,workdir,branch,model,started_ms,started_seq)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.RunID, p.WorkerType, ev.Actor, p.ItemID, p.SegmentID, p.PromptID, p.PromptSHA,
			p.BaseSHA, p.WorkDir, p.Branch, p.Model, ev.TsMS, ev.Seq)
		return err

	case KindRunFinished:
		var p RunFinished
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE adlc_run SET finished_ms=?, verdict=?, envelope_sha=?, head_sha=?, artifact=?,
			model=CASE WHEN ?<>'' THEN ? ELSE model END,
			tok_in=?, tok_out=?, tok_cache_r=?, tok_cache_w=?, cost_micros=?, finished_seq=? WHERE run_id=?`,
			ev.TsMS, p.Verdict, p.EnvelopeSHA, p.HeadSHA, p.Artifact, p.Model, p.Model,
			p.Usage.InputTokens, p.Usage.OutputTokens, p.Usage.CacheReadTokens, p.Usage.CacheWriteTokens,
			p.CostMicros, ev.Seq, p.RunID)
		return err

	case KindRunActorCorrected:
		var p RunActorCorrected
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE adlc_run SET actor=? WHERE run_id=?`, p.To, p.RunID)
		return err

	case KindTransitionAdmitted, KindTransitionRefused:
		var p TransitionOutcome
		if err := dec(&p); err != nil {
			return err
		}
		admitted := 0
		if ev.Kind == KindTransitionAdmitted {
			admitted = 1
		}
		_, err := tx.Exec(`INSERT INTO adlc_proposal(seq,run_id,item_id,worker,from_state,to_state,admitted,reason,detail,ts_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			ev.Seq, p.RunID, p.ItemID, p.Worker, p.From, p.To, admitted, p.Reason, p.Detail, ev.TsMS)
		return err

	case KindGateObserved:
		var p GateObserved
		if err := dec(&p); err != nil {
			return err
		}
		dirty := 0
		if p.Dirty {
			dirty = 1
		}
		_, err := tx.Exec(`INSERT INTO adlc_gate(seq,run_id,item_id,edge,tree_sha,dirty,artifact,status,checks,ts_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			ev.Seq, p.RunID, p.ItemID, p.Edge, p.TreeSHA, dirty, p.Artifact, p.Status, p.Checks, ev.TsMS)
		return err

	case KindQuestionRaised:
		var p QuestionRaised
		if err := dec(&p); err != nil {
			return err
		}
		blocking := 0
		if p.Blocking {
			blocking = 1
		}
		_, err := tx.Exec(`INSERT INTO adlc_question(id,item_id,blocking,text,lean,evidence,raised_by,raised_seq)
			VALUES(?,?,?,?,?,?,?,?)`,
			p.ID, p.ItemID, blocking, p.Text, p.Lean, p.Evidence, p.RaisedBy, ev.Seq)
		return err

	case KindQuestionAnswered:
		var p QuestionAnswered
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE adlc_question SET answer=?, answered_by=?, answered_seq=? WHERE id=?`,
			p.Answer, p.AnsweredBy, ev.Seq, p.ID)
		return err

	case KindApprovalRequested:
		var p ApprovalRequested
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO adlc_approval(id,item_id,radius,plan_digest,summary,requested_seq,requested_ms)
			VALUES(?,?,?,?,?,?,?)`, p.ID, p.ItemID, p.Radius, p.PlanDigest, p.Summary, ev.Seq, ev.TsMS)
		return err

	case KindApprovalDecided:
		var p ApprovalDecided
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE adlc_approval SET verdict=?, approver=?, note=?, decided_seq=?, decided_ms=? WHERE id=?`,
			p.Verdict, p.Approver, p.Note, ev.Seq, ev.TsMS, p.ID)
		return err

	case KindPromptPinned:
		var p PromptPinned
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO adlc_prompt_pin(prompt_id,version,sha,path,seq) VALUES(?,?,?,?,?)`,
			p.PromptID, p.Version, p.SHA, p.Path, ev.Seq)
		return err

	case KindWorkerRegistered:
		var p WorkerRegistered
		if err := dec(&p); err != nil {
			return err
		}
		low := 0
		if p.LowCadence {
			low = 1
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO adlc_worker(type,layer,description,prompt_id,low_cadence,seq)
			VALUES(?,?,?,?,?,?)`, p.Type, p.Layer, p.Description, p.PromptID, low, ev.Seq)
		return err

	case KindSegmentAdvanced:
		var p SegmentAdvanced
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE adlc_segment SET state=?, updated_seq=? WHERE id=?`, p.To, ev.Seq, p.SegmentID)
		return err

	case KindItemProposed:
		var p ItemProposed
		if err := dec(&p); err != nil {
			return err
		}
		admitted := 0
		if p.Admitted {
			admitted = 1
		}
		// Item proposals land in the same table transitions do, so that every
		// refusal in the system is queryable from one place and one report section
		// covers all of them.
		_, err := tx.Exec(`INSERT INTO adlc_proposal(seq,run_id,item_id,worker,from_state,to_state,admitted,reason,detail,ts_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			ev.Seq, p.RunID, p.ProposedID, p.Worker, "", "created", admitted, p.Reason, p.Detail, ev.TsMS)
		return err

	case KindLoopTicked:
		var p LoopTicked
		if err := dec(&p); err != nil {
			return err
		}
		dispatched := 0
		if p.Dispatched {
			dispatched = 1
		}
		_, err := tx.Exec(`INSERT INTO adlc_loop_tick(seq,loop,scope,dispatched,run_id,detail,ts_ms)
			VALUES(?,?,?,?,?,?,?)`, ev.Seq, p.Loop, p.Scope, dispatched, p.RunID, p.Detail, ev.TsMS)
		return err

	case KindNoteRecorded:
		var p NoteRecorded
		if err := dec(&p); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO adlc_note(seq,actor,about,text,ts_ms) VALUES(?,?,?,?,?)`,
			ev.Seq, ev.Actor, p.About, p.Text, ev.TsMS)
		return err
	}
	// An unknown kind is not an error here. It is a fact for Verify to report,
	// and refusing to open a ledger because it contains an event a newer build
	// wrote would make every upgrade a one-way door.
	return nil
}

var amendableColumns = map[string]string{
	"title":        "title",
	"area":         "area",
	"blast_radius": "blast_radius",
	"resources":    "resources",
	"file_scope":   "file_scope",
	"criteria":     "criteria",
	"depends_on":   "depends_on",
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ParseList reads a JSON list column.
func ParseList(s string) []string {
	var out []string
	if s == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ---------------------------------------------------- generation and loops

// ItemProposed records one proposed item and the control plane's answer.
// Admitted proposals are followed by an ItemCreated; refused ones stand alone,
// so the record holds the work that was declined as well as the work that
// happened.
type ItemProposed struct {
	RunID      string `json:"run_id"`
	Worker     string `json:"worker"`
	SegmentID  string `json:"segment_id"`
	ProposedID string `json:"proposed_id"`
	Title      string `json:"title"`
	Admitted   bool   `json:"admitted"`
	Reason     string `json:"reason,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// LoopTicked records one firing of a scheduled dispatch loop.
//
// Every firing is recorded, including the ones that found nothing to do.
// Liveness has to be measured from outside the loop's own opinion of itself.
type LoopTicked struct {
	Loop       string `json:"loop"`
	Scope      string `json:"scope"`
	Dispatched bool   `json:"dispatched"`
	RunID      string `json:"run_id,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// LoopHealth is a derived view of one scheduled loop.
type LoopHealth struct {
	Loop        string
	Scope       string
	Ticks       int
	Dispatches  int
	LastTickMS  int64
	LastRunID   string
	LastDetail  string
	Enabled     bool
	EverySecond int
}

// Fresh reports whether the loop has ticked within a tolerance of its own
// cadence. A loop that has never ticked is not fresh and is not silent — it
// is reported as never having run.
func (h LoopHealth) Fresh(now time.Time, grace float64) bool {
	if h.LastTickMS == 0 || h.EverySecond <= 0 {
		return false
	}
	limit := time.Duration(float64(h.EverySecond)*grace) * time.Second
	return now.Sub(time.UnixMilli(h.LastTickMS)) <= limit
}

// LoopHealthAll derives per-loop liveness from the tick record.
func (l *Ledger) LoopHealthAll() (map[string]LoopHealth, error) {
	rows, err := l.db.Query(`SELECT loop, scope, COUNT(*), SUM(dispatched), MAX(ts_ms) FROM adlc_loop_tick GROUP BY loop, scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LoopHealth{}
	for rows.Next() {
		var h LoopHealth
		var disp sql.NullInt64
		if err := rows.Scan(&h.Loop, &h.Scope, &h.Ticks, &disp, &h.LastTickMS); err != nil {
			return nil, err
		}
		h.Dispatches = int(disp.Int64)
		out[h.Loop] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for name, h := range out {
		var runID, detail string
		err := l.db.QueryRow(`SELECT run_id, detail FROM adlc_loop_tick WHERE loop=? ORDER BY seq DESC LIMIT 1`, name).
			Scan(&runID, &detail)
		if err == nil {
			h.LastRunID, h.LastDetail = runID, detail
			out[name] = h
		}
	}
	return out, nil
}
