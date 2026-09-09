package ledger

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// The console conversation is on the ledger for the same reason everything else
// is: a conversation that steers the fleet but leaves no record turns "why did
// this happen" into a question nobody can answer. The transcript is also the
// agent's only memory — it has none between invocations — so putting it
// anywhere else would mean two places to lose it from.

// ConsoleAsked is one thing a person said to the console.
type ConsoleAsked struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Text      string `json:"text"`
	AskedBy   string `json:"asked_by"`
}

// ConsoleReplied is what came back, including the case where nothing did.
type ConsoleReplied struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	RunID     string `json:"run_id,omitempty"`
	Reply     string `json:"reply"`
	// Actions are what the turn proposed, each already carrying whether it was
	// executed, is waiting for a person, or was refused.
	Actions []ConsoleAction `json:"actions,omitempty"`
	// Failure is set when the turn did not produce an answer. A turn that failed
	// is recorded rather than dropped: an ask with no reply and no explanation
	// is indistinguishable from a console that silently stopped working.
	Failure string `json:"failure,omitempty"`
	Usage   Usage  `json:"usage,omitempty"`
}

// ConsoleAction is one thing the console proposed doing.
type ConsoleAction struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Summary is the action in a person's words, written by the agent. It is
	// what the confirm control is labelled with, so it has to be readable on its
	// own — the person pressing it may not have read the reply above it.
	Summary string            `json:"summary"`
	Args    map[string]string `json:"args,omitempty"`
	// Gated marks an action that exists because a person owns the decision.
	Gated bool `json:"gated"`
	// Outcome is executed, pending, refused or declined.
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

// Action outcomes.
const (
	ActionExecuted = "executed"
	ActionPending  = "pending"
	ActionRefused  = "refused"
	ActionDeclined = "declined"
)

// ConsoleActed records what happened when a pending action was later pressed.
//
// The action's real effect is already its own event — an item.created, a
// transition.admitted — so this does not restate it. It records that the
// pending thing stopped being pending, and who caused that.
type ConsoleActed struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	ActionID  string `json:"action_id"`
	Kind      string `json:"kind"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail,omitempty"`
	By        string `json:"by"`
}

// EventsOfKind returns the most recent events of the given kinds, oldest first.
//
// The filter is pushed into the query rather than applied after reading the
// chain. The console is re-read on every dashboard refresh, and a full scan per
// refresh is a cost that grows with the age of the project for no reason.
func (l *Ledger) EventsOfKind(kinds []Kind, subject string, limit int) ([]Event, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	ph := make([]string, len(kinds))
	args := make([]any, 0, len(kinds)+2)
	for i, k := range kinds {
		ph[i] = "?"
		args = append(args, string(k))
	}
	q := `SELECT ` + eventColumns + ` FROM adlc_event
	      WHERE kind IN (` + strings.Join(ph, ",") + `)`
	if subject != "" {
		q += ` AND subject=?`
		args = append(args, subject)
	}
	// Newest first with a limit, then reversed: the tail of a conversation is
	// what anybody wants, and taking it from the front would need the whole
	// thing read first.
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
	out, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var out []Event
	for rows.Next() {
		var e Event
		var kind, payload string
		if err := rows.Scan(&e.Seq, &e.TsMS, &kind, &e.Actor, &e.Subject,
			&payload, &e.PrevHash, &e.Hash, &e.BuildRev); err != nil {
			return nil, err
		}
		e.Kind = Kind(kind)
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ConsoleTurn is one exchange, assembled from the events that make it up.
type ConsoleTurn struct {
	TurnID  string
	AskedBy string
	Asked   string
	AskedMS int64

	Replied   bool
	Reply     string
	Failure   string
	RunID     string
	RepliedMS int64
	Actions   []ConsoleAction
}

// Pending reports whether this turn is still waiting on the agent.
func (t ConsoleTurn) Pending() bool { return !t.Replied }

// ConsoleHistory reassembles a session's transcript, oldest turn first.
//
// A turn with an ask and no reply is returned as pending rather than skipped.
// That is the state an interrupted turn leaves behind — the server stopped
// between the ask and the answer — and rendering it as absent would let a
// console that stopped answering look like one nobody had asked anything.
func (l *Ledger) ConsoleHistory(sessionID string, turns int) ([]ConsoleTurn, error) {
	// Three events per turn at the outside, plus room for actions pressed later.
	limit := 0
	if turns > 0 {
		limit = turns * 6
	}
	evs, err := l.EventsOfKind(
		[]Kind{KindConsoleAsked, KindConsoleReplied, KindConsoleActed}, sessionID, limit)
	if err != nil {
		return nil, err
	}
	byID := map[string]*ConsoleTurn{}
	var order []string
	for _, e := range evs {
		switch e.Kind {
		case KindConsoleAsked:
			var p ConsoleAsked
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, err
			}
			if byID[p.TurnID] == nil {
				byID[p.TurnID] = &ConsoleTurn{TurnID: p.TurnID}
				order = append(order, p.TurnID)
			}
			t := byID[p.TurnID]
			t.Asked, t.AskedBy, t.AskedMS = p.Text, p.AskedBy, e.TsMS

		case KindConsoleReplied:
			var p ConsoleReplied
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, err
			}
			t := byID[p.TurnID]
			if t == nil {
				continue
			}
			t.Replied, t.Reply, t.Failure = true, p.Reply, p.Failure
			t.RunID, t.RepliedMS, t.Actions = p.RunID, e.TsMS, p.Actions

		case KindConsoleActed:
			var p ConsoleActed
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, err
			}
			t := byID[p.TurnID]
			if t == nil {
				continue
			}
			for i := range t.Actions {
				if t.Actions[i].ID == p.ActionID {
					t.Actions[i].Outcome = p.Outcome
					t.Actions[i].Detail = p.Detail
				}
			}
		}
	}
	out := make([]ConsoleTurn, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	if turns > 0 && len(out) > turns {
		out = out[len(out)-turns:]
	}
	return out, nil
}

// ConsoleAction finds one proposed action in a session, with the turn it came
// from. Pressing a control is a request naming an id, and an id that names
// nothing has to be an error rather than a silent no-op.
func (l *Ledger) ConsoleAction(sessionID, actionID string) (ConsoleTurn, ConsoleAction, error) {
	turns, err := l.ConsoleHistory(sessionID, 0)
	if err != nil {
		return ConsoleTurn{}, ConsoleAction{}, err
	}
	for _, t := range turns {
		for _, a := range t.Actions {
			if a.ID == actionID {
				return t, a, nil
			}
		}
	}
	return ConsoleTurn{}, ConsoleAction{}, fmt.Errorf("action %s: %w", actionID, ErrNotFound)
}
