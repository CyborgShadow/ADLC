package console

import "github.com/CyborgShadow/ADLC/internal/config"

// Everything a person reads about an action, in their words rather than the
// tool's.
//
// The machine names (`create_deliverable`, `answer_question`) exist because a
// closed vocabulary needs stable identifiers, and the agent writes them. Nobody
// operating this should have to. So the identifier stays in the record and in
// the prompt, and the dashboard shows the sentence.

// Human is what this action does, written for somebody who has never read the
// source.
func (k ActionKind) Human() string {
	switch k {
	case ActCreateDeliverable:
		return "Add something to the roadmap"
	case ActRaiseItem:
		return "Raise a piece of work"
	case ActReworkItem:
		return "Send work back for another attempt"
	case ActCancelItem:
		return "Withdraw a piece of work"
	case ActSetLane:
		return "Pause a lane, or change how often it runs"
	case ActNote:
		return "Write something down on the record"
	case ActSignOff:
		return "Agree an idea is worth pursuing"
	case ActAnswerQuestion:
		return "Answer a question that stopped an agent"
	case ActDecideApproval:
		return "Approve or refuse something irreversible"
	}
	return string(k)
}

// Detail is the sentence under the label: what actually happens, and what it
// costs if it is wrong.
func (k ActionKind) Detail() string {
	switch k {
	case ActCreateDeliverable:
		return "Creates a deliverable as a theory. Nothing is spent on it until you sign it off."
	case ActRaiseItem:
		return "Proposes one work item. It faces the same admission rules a planner's proposals do — an area nobody owns, or criteria no command can check, is refused."
	case ActReworkItem:
		return "Returns a rejected item to a builder, with the reason attached. Refused once the item has used up its attempts."
	case ActCancelItem:
		return "Closes an item without doing it. It stays on the record with the stated reason."
	case ActSetLane:
		return "Changes a lane's cadence or parks it. Takes effect on the lane's next tick, not on a restart."
	case ActNote:
		return "Records a note against an item or deliverable. Changes no state."
	case ActSignOff:
		return "Moves an idea onto the roadmap, or signs it off so a researcher picks it up. This is the gate that stops the fleet spending on something nobody agreed to."
	case ActAnswerQuestion:
		return "Unblocks an item by answering what its agent stopped on. The answer is recorded in your words, and everything decomposed from it inherits that reasoning."
	case ActDecideApproval:
		return "Clears, or refuses, a change that reaches something real. An approval names one exact plan; change the plan and it stops applying."
	}
	return ""
}

// WhoDecides says who this belongs to, in one phrase.
func (k ActionKind) WhoDecides(auth config.ConsoleAuthority) string {
	if !k.Gated() {
		if auth.MayExecute(false) {
			return "done for you"
		}
		return "you press it"
	}
	if auth == config.ConsoleFull {
		return "done for you — this project allows it"
	}
	return "yours to press"
}

// HumanAction is one row of the vocabulary, rendered for a person.
type HumanAction struct {
	Kind    string
	Label   string
	Detail  string
	Who     string
	Gated   bool
	Machine string
}

// HumanVocabulary is the whole action list, for the dashboard.
func HumanVocabulary(auth config.ConsoleAuthority) []HumanAction {
	out := make([]HumanAction, 0, len(AllActions()))
	for _, k := range AllActions() {
		out = append(out, HumanAction{
			Kind: string(k), Label: k.Human(), Detail: k.Detail(),
			Who: k.WhoDecides(auth), Gated: k.Gated(), Machine: string(k),
		})
	}
	return out
}
