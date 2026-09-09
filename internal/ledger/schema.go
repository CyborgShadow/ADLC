package ledger

// SchemaVersion is the ledger layout this build writes. It is recorded in the
// database and compared on open: a build older than the ledger it is handed
// reports UNKNOWN rather than guessing.
//
// v2 added adlc_event.build_rev. The number is bumped rather than the meaning of
// v1 changed, because a v1 ledger is still a real thing an older binary is
// entitled to read correctly, and a version whose meaning moves underneath it
// makes every past ledger unreadable in a way nothing can detect.
const SchemaVersion = 2

// schemaSQL is applied on init and is forward-only.
//
// Two structural decisions carry most of the weight here.
//
// First, the chain table is append-only and the triggers say so in SQL rather
// than in a code path, because the code path is the thing under audit. UPDATE
// and DELETE on adlc_event abort at the database, so a control plane with a
// bug cannot quietly rewrite its own record, and neither can anything else
// holding a connection to the file.
//
// Second, the head anchor lives OUTSIDE the event table. If the tip's hash
// were stored only as the last row's hash column, a truncation would remove
// the evidence of itself: dropping the final N events leaves a chain that
// still walks cleanly. The anchor is a second place the tip is written, so a
// truncated chain disagrees with it.
//
// Third, adlc_event.build_rev records which build appended the row, and it sits
// OUTSIDE the hash preimage. That is deliberate: fold it in and every ledger
// this build writes reads as TAMPERED to an older binary, whose HashEvent
// cannot know about a field that did not exist — and a tamper detector that
// accuses at somebody else's obsolescence is the one thing this verifier must
// never be. The column is protected the same way every other column on this
// table is, by the append-only triggers below.
//
// Everything else is a projection. Projections are derived, are written only
// by the replay path, and are checked by rebuilding them from the chain and
// diffing. That is why they carry no append-only triggers and why every one of
// them cites the event sequence it came from.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS adlc_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS adlc_event (
  seq        INTEGER PRIMARY KEY,
  ts_ms      INTEGER NOT NULL,
  kind       TEXT    NOT NULL,
  actor      TEXT    NOT NULL,
  subject    TEXT    NOT NULL DEFAULT '',
  payload    TEXT    NOT NULL,
  prev_hash  TEXT    NOT NULL,
  hash       TEXT    NOT NULL,
  build_rev  TEXT    NOT NULL DEFAULT ''
);

CREATE TRIGGER IF NOT EXISTS adlc_event_no_update
BEFORE UPDATE ON adlc_event
BEGIN SELECT RAISE(ABORT, 'adlc_event is append-only'); END;

CREATE TRIGGER IF NOT EXISTS adlc_event_no_delete
BEFORE DELETE ON adlc_event
BEGIN SELECT RAISE(ABORT, 'adlc_event is append-only'); END;

CREATE TABLE IF NOT EXISTS adlc_head (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  seq        INTEGER NOT NULL,
  hash       TEXT    NOT NULL,
  updated_ms INTEGER NOT NULL
);

CREATE TRIGGER IF NOT EXISTS adlc_head_monotonic
BEFORE UPDATE ON adlc_head
WHEN NEW.seq <= OLD.seq
BEGIN SELECT RAISE(ABORT, 'the head anchor only moves forward'); END;

CREATE TRIGGER IF NOT EXISTS adlc_head_no_delete
BEFORE DELETE ON adlc_head
BEGIN SELECT RAISE(ABORT, 'the head anchor is not deletable'); END;

CREATE TABLE IF NOT EXISTS adlc_segment (
  id          TEXT PRIMARY KEY,
  title       TEXT NOT NULL,
  brief       TEXT NOT NULL DEFAULT '',
  rationale   TEXT NOT NULL DEFAULT '',
  state       TEXT NOT NULL DEFAULT 'drafted',
  rank        INTEGER NOT NULL DEFAULT 0,
  target_open INTEGER NOT NULL DEFAULT 0,
  depends_on  TEXT NOT NULL DEFAULT '[]',
  created_seq INTEGER NOT NULL,
  updated_seq INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS adlc_item (
  id           TEXT PRIMARY KEY,
  segment_id   TEXT NOT NULL,
  title        TEXT NOT NULL,
  area         TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL,
  blast_radius TEXT NOT NULL,
  resources    TEXT NOT NULL DEFAULT '[]',
  file_scope   TEXT NOT NULL DEFAULT '[]',
  depends_on   TEXT NOT NULL DEFAULT '[]',
  criteria     TEXT NOT NULL DEFAULT '[]',
  attempts     INTEGER NOT NULL DEFAULT 0,
  plan_digest  TEXT NOT NULL DEFAULT '',
  rationale    TEXT NOT NULL DEFAULT '',
  blocked_why  TEXT NOT NULL DEFAULT '',
  created_seq  INTEGER NOT NULL,
  updated_seq  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS adlc_run (
  run_id       TEXT PRIMARY KEY,
  worker_type  TEXT NOT NULL,
  actor        TEXT NOT NULL,
  item_id      TEXT NOT NULL DEFAULT '',
  segment_id   TEXT NOT NULL DEFAULT '',
  prompt_id    TEXT NOT NULL DEFAULT '',
  prompt_sha   TEXT NOT NULL DEFAULT '',
  base_sha     TEXT NOT NULL DEFAULT '',
  workdir      TEXT NOT NULL DEFAULT '',
  branch       TEXT NOT NULL DEFAULT '',
  started_ms   INTEGER NOT NULL,
  finished_ms  INTEGER NOT NULL DEFAULT 0,
  verdict      TEXT NOT NULL DEFAULT '',
  envelope_sha TEXT NOT NULL DEFAULT '',
  head_sha     TEXT NOT NULL DEFAULT '',
  artifact     TEXT NOT NULL DEFAULT '',
  model        TEXT NOT NULL DEFAULT '',
  tok_in       INTEGER NOT NULL DEFAULT 0,
  tok_out      INTEGER NOT NULL DEFAULT 0,
  tok_cache_r  INTEGER NOT NULL DEFAULT 0,
  tok_cache_w  INTEGER NOT NULL DEFAULT 0,
  cost_micros  INTEGER NOT NULL DEFAULT 0,
  started_seq  INTEGER NOT NULL,
  finished_seq INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS adlc_proposal (
  seq        INTEGER PRIMARY KEY,
  run_id     TEXT NOT NULL,
  item_id    TEXT NOT NULL,
  worker     TEXT NOT NULL,
  from_state TEXT NOT NULL,
  to_state   TEXT NOT NULL,
  admitted   INTEGER NOT NULL,
  reason     TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '',
  ts_ms      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS adlc_question (
  id           TEXT PRIMARY KEY,
  item_id      TEXT NOT NULL DEFAULT '',
  blocking     INTEGER NOT NULL DEFAULT 0,
  text         TEXT NOT NULL,
  lean         TEXT NOT NULL DEFAULT '',
  evidence     TEXT NOT NULL DEFAULT '',
  raised_by    TEXT NOT NULL DEFAULT '',
  raised_seq   INTEGER NOT NULL,
  answered_seq INTEGER NOT NULL DEFAULT 0,
  answer       TEXT NOT NULL DEFAULT '',
  answered_by  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS adlc_approval (
  id            TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL,
  radius        TEXT NOT NULL,
  plan_digest   TEXT NOT NULL,
  summary       TEXT NOT NULL DEFAULT '',
  requested_seq INTEGER NOT NULL,
  requested_ms  INTEGER NOT NULL,
  decided_seq   INTEGER NOT NULL DEFAULT 0,
  decided_ms    INTEGER NOT NULL DEFAULT 0,
  verdict       TEXT NOT NULL DEFAULT '',
  approver      TEXT NOT NULL DEFAULT '',
  note          TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS adlc_gate (
  seq       INTEGER PRIMARY KEY,
  run_id    TEXT NOT NULL DEFAULT '',
  item_id   TEXT NOT NULL DEFAULT '',
  edge      TEXT NOT NULL DEFAULT '',
  tree_sha  TEXT NOT NULL DEFAULT '',
  dirty     INTEGER NOT NULL DEFAULT 0,
  artifact  TEXT NOT NULL DEFAULT '',
  status    TEXT NOT NULL,
  checks    TEXT NOT NULL DEFAULT '[]',
  ts_ms     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS adlc_worker (
  type        TEXT PRIMARY KEY,
  layer       TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  prompt_id   TEXT NOT NULL DEFAULT '',
  low_cadence INTEGER NOT NULL DEFAULT 0,
  seq         INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS adlc_prompt_pin (
  prompt_id TEXT NOT NULL,
  version   TEXT NOT NULL,
  sha       TEXT NOT NULL,
  path      TEXT NOT NULL,
  seq       INTEGER NOT NULL,
  PRIMARY KEY (prompt_id, version)
);

CREATE TABLE IF NOT EXISTS adlc_blob (
  sha   TEXT PRIMARY KEY,
  kind  TEXT NOT NULL,
  bytes TEXT NOT NULL,
  ts_ms INTEGER NOT NULL
);

CREATE TRIGGER IF NOT EXISTS adlc_blob_no_update
BEFORE UPDATE ON adlc_blob
BEGIN SELECT RAISE(ABORT, 'a blob is addressed by the hash of its contents and never changes'); END;

CREATE TABLE IF NOT EXISTS adlc_note (
  seq    INTEGER PRIMARY KEY,
  actor  TEXT NOT NULL,
  about  TEXT NOT NULL DEFAULT '',
  text   TEXT NOT NULL,
  ts_ms  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS adlc_loop_tick (
  seq        INTEGER PRIMARY KEY,
  loop       TEXT NOT NULL,
  scope      TEXT NOT NULL DEFAULT '',
  dispatched INTEGER NOT NULL DEFAULT 0,
  run_id     TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '',
  ts_ms      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS adlc_loop_last ON adlc_loop_tick(loop, seq);
CREATE INDEX IF NOT EXISTS adlc_run_item   ON adlc_run(item_id);
CREATE INDEX IF NOT EXISTS adlc_run_worker ON adlc_run(worker_type);
CREATE INDEX IF NOT EXISTS adlc_prop_item  ON adlc_proposal(item_id);
CREATE INDEX IF NOT EXISTS adlc_item_seg   ON adlc_item(segment_id);
CREATE INDEX IF NOT EXISTS adlc_event_kind ON adlc_event(kind, subject, seq);
`

// projectionTables are rebuilt from the chain by Replay and are the tables
// Verify diffs. Anything not on this list is either the chain itself, the
// anchor, or metadata.
var projectionTables = []string{
	"adlc_segment", "adlc_item", "adlc_run", "adlc_proposal",
	"adlc_question", "adlc_approval", "adlc_gate", "adlc_worker",
	"adlc_prompt_pin", "adlc_note", "adlc_loop_tick",
}

// requiredTriggers are the append-only guards.
var requiredTriggers = []string{
	"adlc_blob_no_update",
	"adlc_event_no_update",
	"adlc_event_no_delete",
	"adlc_head_monotonic",
	"adlc_head_no_delete",
}

// migrations are additive, forward-only and idempotent.
//
// `CREATE TABLE IF NOT EXISTS` does nothing to a table that already exists, so
// a column added to schemaSQL never reaches a ledger created by an earlier
// build. That is not a hypothetical: it broke the first ledger this build met.
//
// Each statement is applied on every open and its "duplicate column" error is
// the success case. They are additive by rule — nothing here drops, renames
// or retypes anything, because the chain is the record and a migration that
// can lose data is a migration that eventually does.
var migrations = []string{
	`ALTER TABLE adlc_segment ADD COLUMN brief TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE adlc_segment ADD COLUMN target_open INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE adlc_segment ADD COLUMN rationale TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE adlc_segment ADD COLUMN state TEXT NOT NULL DEFAULT 'drafted'`,
	`ALTER TABLE adlc_segment ADD COLUMN rank INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE adlc_item ADD COLUMN rationale TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE adlc_run ADD COLUMN branch TEXT NOT NULL DEFAULT ''`,
	// Rows written before v2 keep the empty default, which reads as UNKNOWN
	// rather than as a build anybody can name.
	`ALTER TABLE adlc_event ADD COLUMN build_rev TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS adlc_event_kind ON adlc_event(kind, subject, seq)`,
}
