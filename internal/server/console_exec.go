package server

import (
	"fmt"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/console"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// execute performs one action the console proposed.
//
// Nothing here is a shortcut. Every branch goes through the machinery a lane or
// a form would go through — the item admission rules, the transition authority,
// the config validator — so the console cannot do anything the rest of the
// system would refuse. What the console adds is that a person said it in a
// sentence instead of assembling the flags.
//
// The returned detail is shown in the transcript whether it succeeded or not. A
// refusal an operator cannot see is a refusal they will keep re-triggering.
// pressedBy says who is causing the action to run, which is the only thing that
// decides whether a gate may be cleared.
type pressedBy int

const (
	// byConsole is the agent acting on its own, subject to console.authority.
	byConsole pressedBy = iota
	// byPerson is somebody pressing the control. That IS the checkpoint being
	// cleared properly, so the gate check does not apply to it — and the record
	// carries their name rather than the console's.
	byPerson
)

func (s *Server) execute(a ledger.ConsoleAction, by string, who pressedBy) (outcome, detail string) {
	kind := console.ActionKind(a.Kind)
	if !kind.Known() {
		return ledger.ActionRefused, fmt.Sprintf("%q is not an action this build knows", a.Kind)
	}
	if kind.Gated() && who == byConsole && !s.Cfg.Console.Authority.MayExecute(true) {
		return ledger.ActionRefused, fmt.Sprintf(
			"%s clears a decision a person owns, and console.authority is %q", a.Kind, s.Cfg.Console.Authority)
	}
	switch kind {
	case console.ActCreateDeliverable:
		return s.doCreateDeliverable(a, by)
	case console.ActRaiseItem:
		return s.doRaiseItem(a, by)
	case console.ActReworkItem:
		return s.doMoveItem(a, by, authority.StateInProgress)
	case console.ActCancelItem:
		return s.doMoveItem(a, by, authority.StateCancelled)
	case console.ActSetLane:
		return s.doSetLane(a, by)
	case console.ActNote:
		return s.doNote(a, by)
	case console.ActSignOff:
		return s.doSignOff(a, by)
	case console.ActAnswerQuestion:
		return s.doAnswerQuestion(a, by)
	case console.ActDecideApproval:
		return s.doDecideApproval(a, by)
	}
	return ledger.ActionRefused, "no handler for " + a.Kind
}

func (s *Server) doCreateDeliverable(a ledger.ConsoleAction, by string) (string, string) {
	id := console.Arg(a, "id")
	if _, err := s.Led.Segment(id); err == nil {
		return ledger.ActionRefused, fmt.Sprintf(
			"%s already exists; two deliverables under one id would give two sets of work one name", id)
	}
	target, _, err := console.ArgInt(a, "target")
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	brief := console.Arg(a, "brief")
	if target > 0 && brief == "" {
		return ledger.ActionRefused,
			"a target with no brief asks a planner to keep work in flight with no direction, and it will invent scope"
	}
	if _, err := s.Led.Append(by, ledger.KindSegmentCreated, id, ledger.SegmentCreated{
		ID: id, Title: console.Arg(a, "title"), Brief: brief,
		Rationale: console.Arg(a, "why"), TargetOpen: target,
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	return ledger.ActionExecuted, fmt.Sprintf(
		"%s created as a theory — it needs your sign-off before anything is spent on it", id)
}

func (s *Server) doRaiseItem(a ledger.ConsoleAction, by string) (string, string) {
	segID := console.Arg(a, "segment")
	seg, err := s.Led.Segment(segID)
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	all, err := s.Led.Items("")
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	existing := map[string]bool{}
	for _, it := range all {
		existing[it.ID] = true
	}
	radius := console.Arg(a, "radius")
	if radius == "" {
		radius = string(config.RadiusNone)
	}
	p := envelope.ProposedItem{
		ID: console.Arg(a, "id"), Title: console.Arg(a, "title"), Area: console.Arg(a, "area"),
		Radius: radius, Criteria: console.Lines(console.Arg(a, "criteria")),
		Resources: console.Lines(console.Arg(a, "resources")),
	}
	// The same rules a planner's proposals face. The console is not a way past
	// them: an item raised in conversation and an item raised by a planner are
	// the same object, and one of them being easier to create is how a backlog
	// fills with work nobody can act on.
	segScopes, openScopes := authority.ScopesFor(seg.ID, all)
	dec := authority.AdmitItem(s.Cfg, p, authority.GenerationFacts{
		SegmentID: seg.ID, SegmentBrief: seg.Brief, ExistingIDs: existing,
		SegmentScopes: segScopes, OpenScopes: openScopes,
	})
	if _, err := s.Led.Append(by, ledger.KindItemProposed, p.ID, ledger.ItemProposed{
		SegmentID: seg.ID, ProposedID: p.ID, Title: p.Title, Worker: "console",
		Admitted: dec.Admitted, Reason: string(dec.Reason), Detail: dec.Detail,
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	if !dec.Admitted {
		return ledger.ActionRefused, fmt.Sprintf("[%s] %s", dec.Reason, dec.Detail)
	}
	if _, err := s.Led.Append(by, ledger.KindItemCreated, p.ID, ledger.ItemCreated{
		ID: p.ID, SegmentID: seg.ID, Title: p.Title, Area: p.Area, Radius: p.Radius,
		Resources: p.Resources, Criteria: p.Criteria, Rationale: console.Arg(a, "why"),
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	return ledger.ActionExecuted, fmt.Sprintf("%s created under %s in area %s", p.ID, seg.ID, p.Area)
}

// doMoveItem drives one item across an edge, in the operator's name, through
// the transition authority.
func (s *Server) doMoveItem(a ledger.ConsoleAction, by string, to authority.State) (string, string) {
	id := console.Arg(a, "item")
	it, err := s.Led.Item(id)
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	from := authority.State(it.State)
	reason := console.Arg(a, "reason")
	now := s.now()
	facts, err := authority.Gather(s.Led, id, "", nil, "", now)
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	dec := authority.New(s.Cfg).Decide(authority.Request{
		Actor: by, AsPM: true, From: from, To: to, Reason: reason, Now: now,
	}, facts)
	if !dec.Admitted {
		if _, aerr := s.Led.Append(by, ledger.KindTransitionRefused, id, ledger.TransitionOutcome{
			ItemID: id, From: string(from), To: string(to),
			Reason: string(dec.Reason), Detail: dec.Detail,
		}); aerr != nil {
			return ledger.ActionRefused, aerr.Error()
		}
		return ledger.ActionRefused, fmt.Sprintf("[%s] %s", dec.Reason, dec.Detail)
	}
	if _, err := s.Led.Append(by, ledger.KindTransitionAdmitted, id, ledger.TransitionOutcome{
		ItemID: id, From: string(from), To: string(to),
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	if _, err := s.Led.Append(by, ledger.KindItemTransitioned, id, ledger.ItemTransitioned{
		ItemID: id, From: string(from), To: string(to), Reason: reason,
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	return ledger.ActionExecuted, fmt.Sprintf("%s moved %s → %s", id, from, to)
}

func (s *Server) doSetLane(a ledger.ConsoleAction, _ string) (string, string) {
	name := console.Arg(a, "lane")
	cur := s.Cfg.Loop(name)
	if cur == nil {
		return ledger.ActionRefused, fmt.Sprintf("no lane called %q is declared", name)
	}
	enabled := cur.Enabled
	if v, ok := console.ArgBool(a, "enabled"); ok {
		enabled = v
	}
	every, ok, err := console.ArgInt(a, "every_seconds")
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	if !ok {
		every = cur.EverySeconds
	}
	per, ok, err := console.ArgInt(a, "max_per_tick")
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	if !ok {
		per = cur.MaxPerTick
	}
	// SetLoop validates and persists. A cadence that spends more time starting
	// runs than doing them is refused there, not here.
	if err := s.Cfg.SetLoop(name, enabled, every, per); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	state := "enabled"
	if !enabled {
		state = "paused"
	}
	return ledger.ActionExecuted, fmt.Sprintf("lane %s is %s, every %ds, %d per tick", name, state, every, per)
}

func (s *Server) doNote(a ledger.ConsoleAction, by string) (string, string) {
	subject := console.Arg(a, "subject")
	if _, err := s.Led.Append(by, ledger.KindNoteRecorded, subject, ledger.NoteRecorded{
		About: subject, Text: console.Arg(a, "text"),
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	return ledger.ActionExecuted, "recorded on the ledger"
}

// --- the three a person owns ----------------------------------------------

func (s *Server) doSignOff(a ledger.ConsoleAction, by string) (string, string) {
	id := console.Arg(a, "segment")
	to := authority.SegmentState(console.Arg(a, "to"))
	if to != authority.SegRoadmap && to != authority.SegSignedOff {
		return ledger.ActionRefused,
			"only accepting onto the roadmap and signing off are sign-off steps; everything else on the roadmap is an agent's"
	}
	seg, err := s.Led.Segment(id)
	if err != nil {
		return ledger.ActionRefused, err.Error()
	}
	from := authority.SegmentState(seg.State)
	want := authority.SegTheory
	if to == authority.SegSignedOff {
		want = authority.SegRoadmap
	}
	if from != want {
		return ledger.ActionRefused, fmt.Sprintf("%s is %s, not %s", id, from, want)
	}
	why := console.Arg(a, "why")
	if why == "" {
		why = string(to) + " by " + by
	}
	if _, err := s.Led.Append(by, ledger.KindSegmentAdvanced, id, ledger.SegmentAdvanced{
		SegmentID: id, From: string(from), To: string(to), Why: why,
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	return ledger.ActionExecuted, fmt.Sprintf("%s is %s", id, to)
}

func (s *Server) doAnswerQuestion(a ledger.ConsoleAction, by string) (string, string) {
	id := console.Arg(a, "question")
	answer := console.Arg(a, "answer")
	if _, err := s.Led.Append(by, ledger.KindQuestionAnswered, id, ledger.QuestionAnswered{
		ID: id, Answer: answer, AnsweredBy: by,
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	return ledger.ActionExecuted, "answered " + id
}

func (s *Server) doDecideApproval(a ledger.ConsoleAction, by string) (string, string) {
	id := console.Arg(a, "approval")
	verdict := strings.ToLower(console.Arg(a, "verdict"))
	if verdict != "approve" && verdict != "reject" {
		return ledger.ActionRefused, "verdict must be approve or reject"
	}
	if _, err := s.Led.Append(by, ledger.KindApprovalDecided, id, ledger.ApprovalDecided{
		ID: id, Verdict: verdict, Approver: by, Note: console.Arg(a, "note"),
	}); err != nil {
		return ledger.ActionRefused, err.Error()
	}
	return ledger.ActionExecuted, verdict + "d " + id
}
