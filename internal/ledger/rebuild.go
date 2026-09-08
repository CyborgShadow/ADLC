package ledger

import "fmt"

// Rebuild re-derives every projection table from the chain.
//
// This is safe in a way it is worth being explicit about: the projections are
// derived data, the chain is the record, and nothing here touches the chain.
// The append-only guards on adlc_event stay in force throughout — a rebuild
// that could rewrite the chain would defeat the whole point of having one.
//
// It exists because a projection can legitimately fall out of step with the
// chain: an upgrade changes how a row is derived, and the stored rows were
// written by the previous build. That is not tampering and must not be reported
// as it, but it does need fixing, and "restore from a backup" is a bad answer
// to a problem the tool can solve deterministically in a second.
func (l *Ledger) Rebuild() (tables int, events int, err error) {
	evs, err := l.Events(1, 0)
	if err != nil {
		return 0, 0, err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	for _, t := range projectionTables {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return 0, 0, fmt.Errorf("clear %s: %w", t, err)
		}
	}
	for _, ev := range evs {
		if err := apply(tx, ev); err != nil {
			return 0, 0, fmt.Errorf(
				"replay stopped at seq %d (%s): %w — the chain is intact; this build cannot derive a projection from it",
				ev.Seq, ev.Kind, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(projectionTables), len(evs), nil
}
