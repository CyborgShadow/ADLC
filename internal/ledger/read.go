package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a subject does not exist. It is distinct from a
// zero value on purpose: "no such item" and "an item with nothing in it" are
// different answers, and conflating them is how a run comes to work against a
// row that was never created.
var ErrNotFound = errors.New("not found")

// Segment is a build segment.
type Segment struct {
	ID         string
	Title      string
	Brief      string
	Rationale  string
	State      string
	Rank       int
	TargetOpen int
	DependsOn  []string
}

// Item is one unit of work.
type Item struct {
	ID         string
	SegmentID  string
	Title      string
	Area       string
	State      string
	Radius     string
	Resources  []string
	FileScope  []string
	DependsOn  []string
	Criteria   []string
	Rationale  string
	Attempts   int
	PlanDigest string
	BlockedWhy string
	UpdatedSeq int64
}

// Run is one dispatched unit of agent work.
type Run struct {
	RunID       string
	WorkerType  string
	Actor       string
	ItemID      string
	SegmentID   string
	PromptID    string
	PromptSHA   string
	BaseSHA     string
	WorkDir     string
	Model       string
	StartedMS   int64
	FinishedMS  int64
	Verdict     string
	EnvelopeSHA string
	HeadSHA     string
	Artifact    string
	Usage       Usage
	CostMicros  int64
}

// Finished reports whether the run recorded an end. A started run with no end
// is not a failure and not a success; it is a run nobody knows the outcome of,
// and it renders that way.
func (r Run) Finished() bool { return r.FinishedMS > 0 }

// Proposal is one transition proposal and the authority's answer.
type Proposal struct {
	Seq      int64
	RunID    string
	ItemID   string
	Worker   string
	From     string
	To       string
	Admitted bool
	Reason   string
	Detail   string
	TsMS     int64
}

// Question is a decision an agent could not make for itself.
type Question struct {
	ID         string
	ItemID     string
	Blocking   bool
	Text       string
	Lean       string
	Evidence   string
	RaisedBy   string
	Answer     string
	AnsweredBy string
	Answered   bool
}

// Approval is a request to perform something whose blast radius exceeds what
// may run unattended.
type Approval struct {
	ID          string
	ItemID      string
	Radius      string
	PlanDigest  string
	Summary     string
	RequestedMS int64
	DecidedMS   int64
	Verdict     string
	Approver    string
	Note        string
}

// Decided reports whether a human has answered.
func (a Approval) Decided() bool { return a.DecidedMS > 0 }

// WorkerRow is a registered worker type.
type WorkerRow struct {
	Type        string
	Layer       string
	Description string
	PromptID    string
	LowCadence  bool
}

// WorkerStat is measured activity for one worker type. Every field is derived
// from the chain. Nothing here is self-reported, which is the difference
// between a liveness board that can raise an alarm and one that cannot: a
// worker that never runs never files a row saying it never ran, so its silence
// has to be computed from the outside.
type WorkerStat struct {
	Type       string
	Layer      string
	Runs       int
	Unfinished int
	Pass       int
	Fail       int
	Reject     int
	Blocked    int
	Refusals   int
	CostMicros int64
	LastMS     int64
	LowCadence bool
}

const itemCols = `id,segment_id,title,area,state,blast_radius,resources,file_scope,depends_on,criteria,rationale,attempts,plan_digest,blocked_why,updated_seq`

func scanItem(s interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var res, fs, dep, cr string
	err := s.Scan(&it.ID, &it.SegmentID, &it.Title, &it.Area, &it.State, &it.Radius,
		&res, &fs, &dep, &cr, &it.Rationale, &it.Attempts, &it.PlanDigest, &it.BlockedWhy, &it.UpdatedSeq)
	if err != nil {
		return Item{}, err
	}
	it.Resources, it.FileScope, it.DependsOn, it.Criteria = ParseList(res), ParseList(fs), ParseList(dep), ParseList(cr)
	return it, nil
}

// Item reads one work item.
func (l *Ledger) Item(id string) (Item, error) {
	row := l.db.QueryRow(`SELECT `+itemCols+` FROM adlc_item WHERE id=?`, id)
	it, err := scanItem(row)
	if err == sql.ErrNoRows {
		return Item{}, fmt.Errorf("item %s: %w", id, ErrNotFound)
	}
	return it, err
}

// Items lists work items, optionally filtered to one segment.
func (l *Ledger) Items(segmentID string) ([]Item, error) {
	q := `SELECT ` + itemCols + ` FROM adlc_item`
	var args []any
	if segmentID != "" {
		q += ` WHERE segment_id=?`
		args = append(args, segmentID)
	}
	q += ` ORDER BY id`
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Segment reads one segment.
func (l *Ledger) Segment(id string) (Segment, error) {
	var s Segment
	var dep string
	err := l.db.QueryRow(`SELECT id,title,brief,rationale,state,rank,target_open,depends_on FROM adlc_segment WHERE id=?`, id).
		Scan(&s.ID, &s.Title, &s.Brief, &s.Rationale, &s.State, &s.Rank, &s.TargetOpen, &dep)
	if err == sql.ErrNoRows {
		return Segment{}, fmt.Errorf("segment %s: %w", id, ErrNotFound)
	}
	s.DependsOn = ParseList(dep)
	return s, err
}

// Segments lists every segment.
func (l *Ledger) Segments() ([]Segment, error) {
	rows, err := l.db.Query(`SELECT id,title,brief,rationale,state,rank,target_open,depends_on FROM adlc_segment ORDER BY rank, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Segment
	for rows.Next() {
		var s Segment
		var dep string
		if err := rows.Scan(&s.ID, &s.Title, &s.Brief, &s.Rationale, &s.State, &s.Rank, &s.TargetOpen, &dep); err != nil {
			return nil, err
		}
		s.DependsOn = ParseList(dep)
		out = append(out, s)
	}
	return out, rows.Err()
}

const runCols = `run_id,worker_type,actor,item_id,segment_id,prompt_id,prompt_sha,base_sha,workdir,model,
	started_ms,finished_ms,verdict,envelope_sha,head_sha,artifact,tok_in,tok_out,tok_cache_r,tok_cache_w,cost_micros`

func scanRun(s interface{ Scan(...any) error }) (Run, error) {
	var r Run
	err := s.Scan(&r.RunID, &r.WorkerType, &r.Actor, &r.ItemID, &r.SegmentID, &r.PromptID, &r.PromptSHA,
		&r.BaseSHA, &r.WorkDir, &r.Model, &r.StartedMS, &r.FinishedMS, &r.Verdict, &r.EnvelopeSHA,
		&r.HeadSHA, &r.Artifact, &r.Usage.InputTokens, &r.Usage.OutputTokens,
		&r.Usage.CacheReadTokens, &r.Usage.CacheWriteTokens, &r.CostMicros)
	return r, err
}

// Run reads one run.
func (l *Ledger) Run(id string) (Run, error) {
	row := l.db.QueryRow(`SELECT `+runCols+` FROM adlc_run WHERE run_id=?`, id)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return Run{}, fmt.Errorf("run %s: %w", id, ErrNotFound)
	}
	return r, err
}

// RunExists reports whether a run id is taken.
//
// Checked when the id is minted and again when its work lands.
func (l *Ledger) RunExists(id string) (bool, error) {
	var n int
	err := l.db.QueryRow(`SELECT COUNT(*) FROM adlc_run WHERE run_id=?`, id).Scan(&n)
	return n > 0, err
}

// Runs lists runs, newest first, optionally filtered by item.
func (l *Ledger) Runs(itemID string, limit int) ([]Run, error) {
	q := `SELECT ` + runCols + ` FROM adlc_run`
	var args []any
	if itemID != "" {
		q += ` WHERE item_id=?`
		args = append(args, itemID)
	}
	q += ` ORDER BY started_ms DESC, run_id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Proposals lists transition proposals, newest first. Refused proposals are
// included and are the point: the record has to hold what was declined.
func (l *Ledger) Proposals(itemID string, onlyRefused bool, limit int) ([]Proposal, error) {
	q := `SELECT seq,run_id,item_id,worker,from_state,to_state,admitted,reason,detail,ts_ms FROM adlc_proposal WHERE 1=1`
	var args []any
	if itemID != "" {
		q += ` AND item_id=?`
		args = append(args, itemID)
	}
	if onlyRefused {
		q += ` AND admitted=0`
	}
	q += ` ORDER BY seq DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var p Proposal
		var adm int
		if err := rows.Scan(&p.Seq, &p.RunID, &p.ItemID, &p.Worker, &p.From, &p.To, &adm, &p.Reason, &p.Detail, &p.TsMS); err != nil {
			return nil, err
		}
		p.Admitted = adm == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// Questions lists questions. openOnly restricts to unanswered ones.
func (l *Ledger) Questions(itemID string, openOnly bool) ([]Question, error) {
	q := `SELECT id,item_id,blocking,text,lean,evidence,raised_by,answer,answered_by,answered_seq FROM adlc_question WHERE 1=1`
	var args []any
	if itemID != "" {
		q += ` AND item_id=?`
		args = append(args, itemID)
	}
	if openOnly {
		q += ` AND answered_seq=0`
	}
	q += ` ORDER BY raised_seq`
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Question
	for rows.Next() {
		var x Question
		var blocking int
		var ansSeq int64
		if err := rows.Scan(&x.ID, &x.ItemID, &blocking, &x.Text, &x.Lean, &x.Evidence, &x.RaisedBy,
			&x.Answer, &x.AnsweredBy, &ansSeq); err != nil {
			return nil, err
		}
		x.Blocking, x.Answered = blocking == 1, ansSeq > 0
		out = append(out, x)
	}
	return out, rows.Err()
}

// Approvals lists approval requests for an item, newest first.
func (l *Ledger) Approvals(itemID string) ([]Approval, error) {
	q := `SELECT id,item_id,radius,plan_digest,summary,requested_ms,decided_ms,verdict,approver,note FROM adlc_approval`
	var args []any
	if itemID != "" {
		q += ` WHERE item_id=?`
		args = append(args, itemID)
	}
	q += ` ORDER BY requested_seq DESC`
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.ItemID, &a.Radius, &a.PlanDigest, &a.Summary,
			&a.RequestedMS, &a.DecidedMS, &a.Verdict, &a.Approver, &a.Note); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Workers lists every registered worker type, including those with no runs.
func (l *Ledger) Workers() ([]WorkerRow, error) {
	rows, err := l.db.Query(`SELECT type,layer,description,prompt_id,low_cadence FROM adlc_worker ORDER BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkerRow
	for rows.Next() {
		var w WorkerRow
		var low int
		if err := rows.Scan(&w.Type, &w.Layer, &w.Description, &w.PromptID, &low); err != nil {
			return nil, err
		}
		w.LowCadence = low == 1
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkerStats measures every registered worker from the chain.
//
// It starts from the registry, not from the runs, so a worker with zero runs
// appears as a row of zeroes rather than not appearing. That inversion is the
// whole mechanism: reading activity from the runs alone renders a dark worker
// as silence, and silence reads as fine.
func (l *Ledger) WorkerStats(segmentID string) ([]WorkerStat, error) {
	workers, err := l.Workers()
	if err != nil {
		return nil, err
	}
	byType := map[string]*WorkerStat{}
	out := make([]WorkerStat, 0, len(workers))
	for _, w := range workers {
		out = append(out, WorkerStat{Type: w.Type, Layer: w.Layer, LowCadence: w.LowCadence})
	}
	for i := range out {
		byType[out[i].Type] = &out[i]
	}

	q := `SELECT worker_type, verdict, finished_ms, cost_micros, started_ms FROM adlc_run`
	var args []any
	if segmentID != "" {
		q += ` WHERE segment_id=?`
		args = append(args, segmentID)
	}
	rows, err := l.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var wt, verdict string
		var fin, cost, started int64
		if err := rows.Scan(&wt, &verdict, &fin, &cost, &started); err != nil {
			return nil, err
		}
		st := byType[wt]
		if st == nil {
			// A run by a type no longer registered still counts. Dropping it would
			// understate the record to make the roster look tidy.
			out = append(out, WorkerStat{Type: wt, Layer: "unregistered"})
			byType[wt] = &out[len(out)-1]
			st = byType[wt]
		}
		st.Runs++
		st.CostMicros += cost
		if started > st.LastMS {
			st.LastMS = started
		}
		if fin == 0 {
			st.Unfinished++
			continue
		}
		switch verdict {
		case "pass":
			st.Pass++
		case "fail":
			st.Fail++
		case "reject":
			st.Reject++
		case "blocked":
			st.Blocked++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rq := `SELECT worker, COUNT(*) FROM adlc_proposal WHERE admitted=0`
	var rargs []any
	if segmentID != "" {
		rq += ` AND item_id IN (SELECT id FROM adlc_item WHERE segment_id=?)`
		rargs = append(rargs, segmentID)
	}
	rq += ` GROUP BY worker`
	rrows, err := l.db.Query(rq, rargs...)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var w string
		var n int
		if err := rrows.Scan(&w, &n); err != nil {
			return nil, err
		}
		if st := byType[w]; st != nil {
			st.Refusals = n
		}
	}
	return out, rrows.Err()
}

// SpendMicros totals recorded cost. A zero window means all time.
func (l *Ledger) SpendMicros(since time.Time, segmentID string) (int64, error) {
	q := `SELECT COALESCE(SUM(cost_micros),0) FROM adlc_run WHERE 1=1`
	var args []any
	if !since.IsZero() {
		q += ` AND started_ms>=?`
		args = append(args, since.UnixMilli())
	}
	if segmentID != "" {
		q += ` AND segment_id=?`
		args = append(args, segmentID)
	}
	var total int64
	err := l.db.QueryRow(q, args...).Scan(&total)
	return total, err
}

// PromptPin resolves a pinned prompt version.
func (l *Ledger) PromptPin(promptID, version string) (sha, path string, err error) {
	err = l.db.QueryRow(`SELECT sha,path FROM adlc_prompt_pin WHERE prompt_id=? AND version=?`,
		promptID, version).Scan(&sha, &path)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("prompt %s@%s: %w", promptID, version, ErrNotFound)
	}
	return sha, path, err
}

// GateRun is a recorded gate observation, read back.
type GateRun struct {
	Seq      int64
	RunID    string
	ItemID   string
	Edge     string
	TreeSHA  string
	Dirty    bool
	Artifact string
	Status   string
	Checks   string
	TsMS     int64
}

// GatesForRun returns what the control plane observed during one run.
func (l *Ledger) GatesForRun(runID string) ([]GateRun, error) {
	rows, err := l.db.Query(
		`SELECT seq,run_id,item_id,edge,tree_sha,dirty,artifact,status,checks,ts_ms
		 FROM adlc_gate WHERE run_id=? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GateRun
	for rows.Next() {
		var g GateRun
		var dirty int
		if err := rows.Scan(&g.Seq, &g.RunID, &g.ItemID, &g.Edge, &g.TreeSHA, &dirty,
			&g.Artifact, &g.Status, &g.Checks, &g.TsMS); err != nil {
			return nil, err
		}
		g.Dirty = dirty == 1
		out = append(out, g)
	}
	return out, rows.Err()
}

// ActiveRuns are runs that started and have not recorded an end.
//
// They are what the overview page shows as "running now" — with the honest
// caveat that a run in this list may also be one that died without recording
// anything, which is why the page shows how long each has been open.
func (l *Ledger) ActiveRuns() ([]Run, error) {
	rows, err := l.db.Query(`SELECT ` + runCols + ` FROM adlc_run WHERE finished_ms=0 ORDER BY started_ms DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AttemptsFor counts how many runs have been dispatched against one item, and
// how they ended. A person looking at a stuck item wants this before anything
// else: how many times has this been tried, and what happened each time.
func (l *Ledger) AttemptsFor(itemID string) (tries, passed, failed int, err error) {
	rows, err := l.db.Query(`SELECT verdict, finished_ms FROM adlc_run WHERE item_id=?`, itemID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		var fin int64
		if err := rows.Scan(&v, &fin); err != nil {
			return 0, 0, 0, err
		}
		tries++
		switch {
		case fin == 0:
		case v == "pass":
			passed++
		default:
			failed++
		}
	}
	return tries, passed, failed, rows.Err()
}

// SegmentHistory returns the roadmap moves for one deliverable, oldest first.
func (l *Ledger) SegmentHistory(segmentID string) ([]Event, error) {
	evs, err := l.Events(1, 0)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == KindSegmentAdvanced && e.Subject == segmentID {
			out = append(out, e)
		}
	}
	return out, nil
}
