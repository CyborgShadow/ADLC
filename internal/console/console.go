// Package console is the operator console: a way to drive this tool by talking
// to it.
//
// It changes nothing about how the system works. A console turn is a run like
// any other — it is dispatched as a declared worker, it writes an envelope, and
// everything it wants to happen goes through the same transition authority as
// everything a lane produces. What it adds is a way to say what you want in
// your own words instead of assembling the flags yourself.
//
// The one thing that needed deciding is how much of what it proposes it may
// execute, and that is declared in config rather than settled here. See
// config.ConsoleAuthority.
package console

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// ActionKind is something the console can propose doing.
//
// A closed vocabulary, for the reason every other vocabulary here is closed: an
// action the tool cannot classify is one it cannot decide about, and the only
// safe thing to do with an unclassifiable action is refuse it. That refusal has
// to be legible, which means the set has to be enumerable.
type ActionKind string

const (
	// --- things the control plane could do on its own

	// ActCreateDeliverable registers a deliverable at the top of the planning
	// pipeline. It still needs a person to sign it off before anything is spent.
	ActCreateDeliverable ActionKind = "create_deliverable"
	// ActRaiseItem proposes one work item, admitted or refused by the same rules
	// a planner's proposals face.
	ActRaiseItem ActionKind = "raise_item"
	// ActReworkItem routes a rejected item back for another attempt.
	ActReworkItem ActionKind = "rework_item"
	// ActCancelItem withdraws an item.
	ActCancelItem ActionKind = "cancel_item"
	// ActSetLane pauses a lane or changes its cadence.
	ActSetLane ActionKind = "set_lane"
	// ActNote records something on the ledger without changing any state.
	ActNote ActionKind = "note"

	// --- the three gates that exist because a person owns the decision

	// ActSignOff moves a deliverable through the planning steps a person owns.
	ActSignOff ActionKind = "sign_off"
	// ActAnswerQuestion answers a question an agent stopped on.
	ActAnswerQuestion ActionKind = "answer_question"
	// ActDecideApproval approves or rejects something irreversible.
	ActDecideApproval ActionKind = "decide_approval"
)

// AllActions is the vocabulary, in the order the console prompt lists it.
func AllActions() []ActionKind {
	return []ActionKind{
		ActCreateDeliverable, ActRaiseItem, ActReworkItem, ActCancelItem, ActSetLane, ActNote,
		ActSignOff, ActAnswerQuestion, ActDecideApproval,
	}
}

// Known reports whether this build understands the action.
func (k ActionKind) Known() bool {
	for _, x := range AllActions() {
		if x == k {
			return true
		}
	}
	return false
}

// Gated reports whether the action clears a decision a person owns.
//
// These three are not gated because they are dangerous — cancelling an item is
// arguably worse than answering a question. They are gated because each one IS
// the human checkpoint: signing off is what stops the fleet spending on an idea
// nobody agreed to, answering a blocking question is the analysis an agent
// declined to make for itself, and an approval is the last thing standing
// between an agent and an outage. An agent that clears its own checkpoint has
// removed it.
func (k ActionKind) Gated() bool {
	switch k {
	case ActSignOff, ActAnswerQuestion, ActDecideApproval:
		return true
	}
	return false
}

// Args names the arguments each action takes, so a malformed one is refused
// with the missing name rather than failing somewhere downstream.
func (k ActionKind) Args() (required, optional []string) {
	switch k {
	case ActCreateDeliverable:
		return []string{"id", "title", "brief"}, []string{"why", "target"}
	case ActRaiseItem:
		return []string{"segment", "id", "title", "area", "criteria"}, []string{"radius", "why", "resources"}
	case ActReworkItem:
		return []string{"item", "reason"}, nil
	case ActCancelItem:
		return []string{"item", "reason"}, nil
	case ActSetLane:
		return []string{"lane"}, []string{"enabled", "every_seconds", "max_per_tick"}
	case ActNote:
		return []string{"text"}, []string{"subject"}
	case ActSignOff:
		return []string{"segment", "to"}, []string{"why"}
	case ActAnswerQuestion:
		return []string{"question", "answer"}, nil
	case ActDecideApproval:
		return []string{"approval", "verdict"}, []string{"note"}
	}
	return nil, nil
}

// consoleOutputs is the shape a console turn writes into its envelope.
//
// It rides in outputs.notes_md as JSON rather than in a typed field, so the
// envelope contract does not grow a branch for a role that advances no item.
type consoleOutputs struct {
	Reply   string      `json:"reply"`
	Actions []rawAction `json:"actions,omitempty"`
}

type rawAction struct {
	Kind    string            `json:"kind"`
	Summary string            `json:"summary"`
	Args    map[string]string `json:"args,omitempty"`
}

// Turn is one parsed reply.
type Turn struct {
	Reply   string
	Actions []ledger.ConsoleAction
	// Rejected are actions this build refused before anything was executed,
	// each with the reason. They are carried rather than dropped: an action the
	// agent proposed and the tool discarded is exactly the sort of thing whose
	// absence makes a transcript misleading.
	Rejected []ledger.ConsoleAction
}

// Parse reads a console turn's envelope.
//
// Anything malformed is a refusal with a reason, never a silent drop. The
// console is the one surface where an agent's output is shown to a person as
// prose, and prose is easy to believe — so the actions behind it are checked
// harder, not less.
func Parse(env *envelope.Envelope, turnID string, auth config.ConsoleAuthority) (Turn, error) {
	if env == nil {
		return Turn{}, fmt.Errorf("the turn produced no envelope")
	}
	var out consoleOutputs
	body := strings.TrimSpace(env.Outputs.Notes)
	if body == "" {
		// A turn that said something in its summary and nothing in its outputs
		// is still an answer. Refusing it would spend a run to punish shape.
		if s := strings.TrimSpace(env.Summary); s != "" {
			return Turn{Reply: s}, nil
		}
		return Turn{}, fmt.Errorf("the turn wrote no reply")
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		// Same reasoning: plain prose where JSON was asked for is an answer with
		// no actions, not a failed turn.
		return Turn{Reply: body}, nil
	}
	t := Turn{Reply: strings.TrimSpace(out.Reply)}
	if t.Reply == "" {
		t.Reply = strings.TrimSpace(env.Summary)
	}
	for i, ra := range out.Actions {
		a := ledger.ConsoleAction{
			ID:      fmt.Sprintf("%s-a%d", turnID, i+1),
			Kind:    ra.Kind,
			Summary: strings.TrimSpace(ra.Summary),
			Args:    ra.Args,
		}
		kind := ActionKind(ra.Kind)
		a.Gated = kind.Gated()
		switch {
		case !kind.Known():
			a.Outcome, a.Detail = ledger.ActionRefused,
				fmt.Sprintf("%q is not an action this build knows; the dashboard\x27s Console page lists the ones that exist", ra.Kind)
		case a.Summary == "":
			a.Outcome, a.Detail = ledger.ActionRefused,
				"an action with no summary cannot be labelled on a control somebody presses"
		default:
			if miss := missingArgs(kind, ra.Args); len(miss) > 0 {
				a.Outcome, a.Detail = ledger.ActionRefused,
					"missing "+strings.Join(miss, ", ")
			} else if auth.MayExecute(a.Gated) {
				a.Outcome = ledger.ActionPending // executed by the caller, then rewritten
			} else {
				a.Outcome = ledger.ActionPending
			}
		}
		if a.Outcome == ledger.ActionRefused {
			t.Rejected = append(t.Rejected, a)
			continue
		}
		t.Actions = append(t.Actions, a)
	}
	return t, nil
}

func missingArgs(k ActionKind, args map[string]string) []string {
	required, _ := k.Args()
	var miss []string
	for _, name := range required {
		if strings.TrimSpace(args[name]) == "" {
			miss = append(miss, name)
		}
	}
	return miss
}

// Arg reads one argument, trimmed.
func Arg(a ledger.ConsoleAction, name string) string {
	return strings.TrimSpace(a.Args[name])
}

// ArgInt reads a numeric argument. A value that is not a number is reported as
// such rather than read as zero — zero is a meaningful cadence and a meaningful
// target, so the two cannot be allowed to look alike.
func ArgInt(a ledger.ConsoleAction, name string) (int, bool, error) {
	s := Arg(a, name)
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false, fmt.Errorf("%s=%q is not a number", name, s)
	}
	return n, true, nil
}

// ArgBool reads a boolean argument, accepting the spellings an agent actually
// writes.
func ArgBool(a ledger.ConsoleAction, name string) (bool, bool) {
	switch strings.ToLower(Arg(a, name)) {
	case "true", "yes", "on", "1":
		return true, true
	case "false", "no", "off", "0":
		return false, true
	}
	return false, false
}

// Lines splits a multi-line argument, dropping blanks. Acceptance criteria
// arrive this way because an argument map is flat.
func Lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// SessionID is the conversation a dashboard is talking in.
//
// One per project rather than one per browser tab. The console steers a shared
// fleet; two operators holding separate conversations about the same fleet
// would each be missing half of why it is in the state it is in.
func SessionID(project string) string {
	p := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		}
		return '-'
	}, project)
	if p == "" {
		p = "project"
	}
	return "console:" + p
}

// Vocabulary renders the action list for the prompt, so the agent is told what
// it may propose by the same table this package decides with. A prompt that
// lists actions separately drifts from the code, and the drift shows up as
// refusals nobody can explain.
func Vocabulary(auth config.ConsoleAuthority) string {
	var b strings.Builder
	for _, k := range AllActions() {
		required, optional := k.Args()
		fmt.Fprintf(&b, "- `%s` — args: %s", k, strings.Join(required, ", "))
		if len(optional) > 0 {
			sort.Strings(optional)
			fmt.Fprintf(&b, " (optional: %s)", strings.Join(optional, ", "))
		}
		if k.Gated() {
			if auth == config.ConsoleFull {
				b.WriteString("  · a gate a person owns; this project has configured you to press it")
			} else {
				b.WriteString("  · a gate a person owns; you may draft it, they press it")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
