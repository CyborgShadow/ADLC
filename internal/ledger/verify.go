package ledger

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Verdict is the outcome of a verification pass.
//
// There are three of them, and the third is the whole point of this file.
//
// When a newer build appended an event kind the older binary did not know,
// `ledger verify` reported LEDGER FAILED VERIFICATION over a chain whose
// hashes, anchor, replay and append-only guards were all perfect. A tamper
// detector that cries tamper at its own obsolescence gets ignored, and a
// tamper detector that is ignored is not a control.
//
// So integrity and knowledge are answered separately. Broken hashes are
// TAMPERED. An event kind, item state or schema version this build does not
// recognise is UNKNOWN — the honest recovery for which is to upgrade the
// binary, not to distrust the record.
type Verdict string

const (
	// VerdictIntact means every integrity check passed and this build understood
	// everything it read.
	VerdictIntact Verdict = "INTACT"
	// VerdictUnknown means integrity holds but this build is not current enough
	// to interpret the whole record.
	VerdictUnknown Verdict = "UNKNOWN"
	// VerdictTampered means the record does not describe itself. This is the only
	// verdict that accuses anyone.
	VerdictTampered Verdict = "TAMPERED"
	// VerdictStale means the chain is intact and the derived tables do not match
	// a replay of it.
	//
	// Two things produce that, and from outside they are indistinguishable: an
	// upgrade changed how a row is derived, or something wrote to a projection
	// without going through the chain. So this verdict deliberately does not
	// claim to know which — it reports what was observed and names both causes.
	//
	// It is separate from TAMPERED for two reasons. The first is that calling it
	// tampering asserts a cause the check cannot establish, and it would fire on
	// every release that changes a projection; an alarm that goes off on routine
	// upgrades is one people learn to ignore, which costs exactly the one time
	// it was real. The second is that the consequence is genuinely different: a
	// broken chain means history is unaccounted for, while a wrong projection
	// means a cache is wrong and `adlc ledger rebuild` restores it from the
	// record that was never in doubt.
	VerdictStale Verdict = "STALE PROJECTION"
)

// Tier separates the two questions.
type Tier string

const (
	TierIntegrity Tier = "integrity"
	// TierDerived covers the tables rebuilt from the chain. A disagreement here
	// says the deriving code changed, not that the record did.
	TierDerived   Tier = "derived"
	TierKnowledge Tier = "knowledge"
)

// Check is one verification result.
type Check struct {
	Name   string
	Tier   Tier
	Pass   bool
	Detail string
}

// Report is a whole verification pass.
type Report struct {
	Verdict  Verdict
	HeadSeq  int64
	HeadHash string
	Events   int64
	Checks   []Check
	Findings []string
}

// Failed reports whether the caller should treat this as a hard stop.
//
// A stale projection is not one. The chain is the record and it is intact, so
// the work can carry on; what is wrong is a derived table, and it is fixable
// with one command.
func (r *Report) Failed() bool { return r.Verdict == VerdictTampered }

// Verify walks the chain and answers both questions.
func (l *Ledger) Verify() (*Report, error) {
	rep := &Report{Verdict: VerdictIntact}
	add := func(name string, tier Tier, pass bool, detail string) {
		rep.Checks = append(rep.Checks, Check{Name: name, Tier: tier, Pass: pass, Detail: detail})
		if pass {
			return
		}
		switch tier {
		case TierIntegrity:
			rep.Verdict = VerdictTampered
		case TierDerived:
			// Never downgrades a real integrity failure.
			if rep.Verdict == VerdictIntact || rep.Verdict == VerdictUnknown {
				rep.Verdict = VerdictStale
			}
		case TierKnowledge:
			if rep.Verdict == VerdictIntact {
				rep.Verdict = VerdictUnknown
			}
		}
	}

	// --- schema version, first: an older build reading a newer file should say
	// so before it says anything else.
	stored, err := l.StoredSchemaVersion()
	if err != nil {
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	add("schema_version", TierKnowledge, stored <= SchemaVersion,
		fmt.Sprintf("ledger schema v%d, this build writes v%d", stored, SchemaVersion))

	events, err := l.Events(1, 0)
	if err != nil {
		return nil, err
	}
	rep.Events = int64(len(events))

	// --- chain walk: dense sequence, correct linkage, recomputed hashes.
	prev := GenesisHash
	var linkageBad, hashBad, gapBad int
	for i, ev := range events {
		want := int64(i + 1)
		if ev.Seq != want {
			gapBad++
			rep.Findings = append(rep.Findings,
				fmt.Sprintf("seq %d: sequence is not dense — expected %d", ev.Seq, want))
		}
		if ev.PrevHash != prev {
			linkageBad++
			rep.Findings = append(rep.Findings,
				fmt.Sprintf("seq %d: prev_hash does not match the previous event's hash", ev.Seq))
		}
		got := HashEvent(ev.PrevHash, ev.Seq, ev.TsMS, ev.Kind, ev.Actor, ev.Subject, ev.Payload)
		if got != ev.Hash {
			hashBad++
			rep.Findings = append(rep.Findings,
				fmt.Sprintf("seq %d: recorded hash %s does not describe the row", ev.Seq, short(ev.Hash)))
		}
		prev = ev.Hash
	}
	add("seq_dense", TierIntegrity, gapBad == 0, fmt.Sprintf("%d event(s), %d gap(s)", len(events), gapBad))
	add("chain_linkage", TierIntegrity, linkageBad == 0, fmt.Sprintf("%d broken link(s)", linkageBad))
	add("event_hashes", TierIntegrity, hashBad == 0, fmt.Sprintf("%d mismatched hash(es)", hashBad))

	// --- head anchor: a second copy of the tip, kept outside the event table
	// precisely so that truncating the chain cannot also truncate the evidence
	// that it was truncated.
	hseq, hhash, err := l.Head()
	if err != nil {
		return nil, err
	}
	rep.HeadSeq, rep.HeadHash = hseq, hhash
	anchorOK := (len(events) == 0 && hseq == 0) ||
		(len(events) > 0 && hseq == events[len(events)-1].Seq && hhash == events[len(events)-1].Hash)
	detail := fmt.Sprintf("anchor seq %d %s", hseq, short(hhash))
	if !anchorOK {
		detail += " — does not match the last event in the chain; events have been removed from the tip"
	}
	add("head_anchor", TierIntegrity, anchorOK, detail)

	// --- append-only guards: present, and proven to still fire. A guard that has
	// gone quiet reads exactly like a clean tree, so presence in sqlite_master is
	// not enough on its own.
	missing, err := l.missingTriggers()
	if err != nil {
		return nil, err
	}
	add("guards_present", TierIntegrity, len(missing) == 0,
		fmt.Sprintf("%d/%d append-only guards present%s", len(requiredTriggers)-len(missing), len(requiredTriggers), listSuffix(missing)))

	fires, fireDetail, err := l.guardsFire()
	if err != nil {
		return nil, err
	}
	add("guards_fire", TierIntegrity, fires, fireDetail)

	// --- projection replay: rebuild every derived table from the chain in a
	// fresh database and diff. This is what makes "the reports agree with the
	// record" a checked property instead of a hope.
	diffs, err := l.replayDiff(events)
	if err != nil {
		return nil, err
	}
	badBlobs, totalBlobs, berr := l.blobIntegrity()
	if berr != nil {
		return nil, berr
	}
	add("blob_integrity", TierIntegrity, badBlobs == 0,
		fmt.Sprintf("%d retained prompt/envelope blob(s), %d whose contents no longer hash to their own address", totalBlobs, badBlobs))

	add("projection_replay", TierDerived, len(diffs) == 0,
		fmt.Sprintf("%d projection table(s) disagree with a replay of the chain%s", len(diffs), listSuffix(diffs)))
	for _, d := range diffs {
		rep.Findings = append(rep.Findings, fmt.Sprintf(
			"projection %s does not match a replay of the chain. The chain itself passed every integrity check, so the record is not in doubt — but this table is not what the record says it should be. Either an upgrade changed how these rows are derived, or something wrote to this table without going through the chain. `adlc ledger rebuild` re-derives it from the chain; if you were not expecting this, find out which of the two it was first.", d))
	}

	// --- knowledge: kinds this build cannot interpret.
	unknown := map[string]int{}
	for _, ev := range events {
		if !KnownKinds[ev.Kind] {
			unknown[string(ev.Kind)]++
		}
	}
	if len(unknown) == 0 {
		add("known_kinds", TierKnowledge, true, fmt.Sprintf("all %d event kind(s) understood", countKinds(events)))
	} else {
		var parts []string
		for k, n := range unknown {
			parts = append(parts, fmt.Sprintf("%s (%d)", k, n))
		}
		sort.Strings(parts)
		add("known_kinds", TierKnowledge, false,
			"written by a newer build: "+strings.Join(parts, ", ")+" — upgrade this binary; the chain itself is fine")
	}
	return rep, nil
}

func (l *Ledger) missingTriggers() ([]string, error) {
	have := map[string]bool{}
	rows, err := l.db.Query(`SELECT name FROM sqlite_master WHERE type='trigger'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		have[n] = true
	}
	var missing []string
	for _, t := range requiredTriggers {
		if !have[t] {
			missing = append(missing, t)
		}
	}
	return missing, rows.Err()
}

// guardsFire proves the append-only triggers still refuse, by attempting the
// writes they exist to stop and requiring an error.
//
// Every attempt happens inside a transaction that is rolled back, so the probe
// cannot itself modify anything. A guard is only a guard if it can be observed
// refusing; the alternative is a check that asserts a trigger's name.
func (l *Ledger) guardsFire() (bool, string, error) {
	var n int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM adlc_event`).Scan(&n); err != nil {
		return false, "", err
	}
	if n == 0 {
		return true, "no events yet; nothing to probe", nil
	}
	tx, err := l.db.Begin()
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()

	_, updErr := tx.Exec(`UPDATE adlc_event SET actor='probe' WHERE seq=1`)
	if updErr == nil {
		return false, "UPDATE on adlc_event was ACCEPTED — the append-only guard is vacuous", nil
	}
	// The failed statement leaves this transaction usable in SQLite, but be
	// explicit rather than assume it: run the second probe in its own.
	tx.Rollback()

	tx2, err := l.db.Begin()
	if err != nil {
		return false, "", err
	}
	defer tx2.Rollback()
	_, delErr := tx2.Exec(`DELETE FROM adlc_event WHERE seq=1`)
	if delErr == nil {
		return false, "DELETE on adlc_event was ACCEPTED — the append-only guard is vacuous", nil
	}
	return true, "UPDATE and DELETE on the chain were both refused by the database", nil
}

// replayDiff rebuilds the projections from the chain in a scratch database and
// names every table that disagrees.
func (l *Ledger) replayDiff(events []Event) ([]string, error) {
	scratch, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer scratch.Close()
	scratch.SetMaxOpenConns(1)
	if _, err := scratch.Exec(schemaSQL); err != nil {
		return nil, err
	}
	tx, err := scratch.Begin()
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if err := apply(tx, ev); err != nil {
			tx.Rollback()
			// A replay that cannot be performed is not evidence of tampering, and it is
			// reported as a disagreement rather than swallowed.
			return []string{fmt.Sprintf("replay stopped at seq %d: %v", ev.Seq, err)}, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var diffs []string
	for _, t := range projectionTables {
		a, err := fingerprint(l.db, t)
		if err != nil {
			return nil, err
		}
		b, err := fingerprint(scratch, t)
		if err != nil {
			return nil, err
		}
		if a != b {
			diffs = append(diffs, t)
		}
	}
	return diffs, nil
}

// fingerprint hashes a table's contents independently of row order AND of
// physical column order.
//
// Both independences are load-bearing, and the second one was learned the hard
// way. A table created by an early build and then migrated carries its new
// columns appended at the end; the same table created fresh from the current
// schema carries them in the middle. The data is identical and the layout is
// not — so a fingerprint that walked columns in returned order made every
// migrated ledger report TAMPERED, which is the single worst thing this
// verifier can say when nothing is wrong.
//
// So each row is reduced to a sorted set of name=value pairs. A projection is
// compared on what it holds, never on how the database happens to store it.
func fingerprint(db *sql.DB, table string) (string, error) {
	rows, err := db.Query(`SELECT * FROM ` + table)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		pairs := make([]string, len(cols))
		for i, v := range vals {
			pairs[i] = fmt.Sprintf("%s=%v", cols[i], v)
		}
		sort.Strings(pairs)
		lines = append(lines, strings.Join(pairs, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{0x1e})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func countKinds(events []Event) int {
	set := map[Kind]bool{}
	for _, e := range events {
		set[e.Kind] = true
	}
	return len(set)
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func listSuffix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return ": " + strings.Join(items, ", ")
}
