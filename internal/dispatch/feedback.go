package dispatch

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Telling a run what already went wrong.
//
// The control plane recorded every refusal, every rejected plan and every
// lesson an improver drew from them — and read none of it back. A builder on
// its third attempt was handed the identical prompt that produced the work
// rejected twice, with nothing in it to say so. So the same mistake was
// available to be made again, at full price, for as long as the attempt limit
// allowed. A record that is only ever written to is an archive, not a feedback
// loop.
//
// Two channels, and the difference between them matters.
//
// What went wrong HERE is history about this exact item or deliverable: the
// reason its last attempt was refused, what a validator said about its plan.
// It is specific, it expires when the work lands, and it is not advice.
//
// What has been LEARNED is a lesson an improver drew that outlived the item it
// came from. It is carried across work, and it is the part that makes the fleet
// better rather than merely persistent.
//
// Neither edits a prompt. The file is still the definition of the role; these
// arrive as context, exactly as the brief and the criteria do. An agent whose
// own output could rewrite the instructions it is given next time is an agent
// that grades its own paper, and no amount of usefulness buys that.

// refusals is how many past refusals a run is shown. Enough for a pattern, few
// enough that the newest is not buried under an item's whole history.
const refusalsShown = 3

// lessonsShown bounds what is carried across work. A prompt that grows without
// limit eventually costs more than the mistakes it prevents.
const lessonsShown = 8

// whatWentWrong is the history of this item's own refusals, newest first.
func (d *Dispatcher) whatWentWrong(itemID string, attempts int) string {
	if itemID == "" {
		return ""
	}
	props, err := d.Led.Proposals(itemID, true, refusalsShown)
	if err != nil || len(props) == 0 {
		if attempts > 0 {
			return fmt.Sprintf(
				"## What already went wrong here\n\nThis is attempt %d. Earlier attempts did not land, and the record does not say why — treat that as a reason to check your own work harder, not as permission to repeat it.", attempts+1)
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("## What already went wrong here\n\n")
	fmt.Fprintf(&b, "This is attempt %d on this item. It has been refused %s before:\n\n",
		attempts+1, plural(len(props), "time", "times"))
	for _, p := range props {
		fmt.Fprintf(&b, "- %s -> %s was refused: **%s**", p.From, p.To, p.Reason)
		if strings.TrimSpace(p.Detail) != "" {
			fmt.Fprintf(&b, " — %s", oneLine(p.Detail, 400))
		}
		fmt.Fprintf(&b, " (run %s)\n", p.RunID)
	}
	b.WriteString("\nDo not simply try the same thing again. If you believe a refusal was wrong, say so in your summary with the evidence; do not work around it.")
	return b.String()
}

// whyThePlanCameBack is what a validator said when it sent a breakdown back.
func (d *Dispatcher) whyThePlanCameBack(segmentID string) string {
	if segmentID == "" {
		return ""
	}
	runs, err := d.Led.Runs("", 200)
	if err != nil {
		return ""
	}
	// Newest first, and only runs about this deliverable that ended in a
	// rejection. A planner replanning without being told what was wrong with
	// the last plan is being asked to guess.
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedMS > runs[j].StartedMS })
	for _, r := range runs {
		if r.SegmentID != segmentID || r.Verdict != "reject" {
			continue
		}
		said := ""
		if d.Led.HasBlob(r.EnvelopeSHA) {
			if raw, berr := d.Led.Blob(r.EnvelopeSHA); berr == nil {
				if env, perr := envelope.Parse(raw); perr == nil {
					said = strings.TrimSpace(env.Summary)
				}
			}
		}
		if said == "" {
			said = "The record does not carry what it said, which is itself worth reporting."
		}
		return fmt.Sprintf(
			"## What already went wrong here\n\nThe last breakdown of this deliverable was REJECTED by %s (run %s). What it said:\n\n%s\n\nThis is the thing to fix. Amend the items that already exist, carrying their same ids, rather than filing new ones; they are listed above.",
			r.WorkerType, r.RunID, said)
	}
	return ""
}

// lessons is what the fleet has learned that applies to this run.
func (d *Dispatcher) lessons(worker, area string) string {
	ls, err := d.Led.Lessons(worker, area, lessonsShown)
	if err != nil || len(ls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## What the fleet has learned\n\nLessons other runs paid for. They are evidence, not orders: if one is wrong here, say which and why in your summary.\n\n")
	for _, l := range ls {
		fmt.Fprintf(&b, "- %s", strings.TrimSpace(l.Lesson))
		if l.FromRun != "" {
			fmt.Fprintf(&b, " *(learned on %s)*", l.FromRun)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// recordLessons turns an improver's notes into lessons the fleet keeps.
//
// Recorded by the control plane from what the agent proposed, like everything
// else: the agent writes an envelope and this decides what it earns. A lesson
// is one line — a rule somebody could follow — because a wall of prose carried
// into every future prompt is a cost with no ceiling.
func (d *Dispatcher) recordLessons(runID string, c Candidate, env *envelope.Envelope) {
	body := strings.TrimSpace(env.Outputs.Notes)
	if body == "" {
		return
	}
	kept := 0
	for _, line := range strings.Split(body, "\n") {
		text := strings.TrimSpace(strings.TrimLeft(line, "-*# \t"))
		if len(text) < 20 || !strings.ContainsAny(text, " ") {
			// A heading, a bullet marker, or a fragment. A lesson is a rule
			// somebody could follow, and one word is not one.
			continue
		}
		if kept >= lessonsShown {
			break
		}
		kept++
		if _, err := d.Led.Append(d.actor(), ledger.KindLessonRecorded,
			fmt.Sprintf("%s-L%d", runID, kept), ledger.LessonRecorded{
				RunID: runID, Worker: c.Worker, Area: c.Item.Area,
				ItemID: c.Item.ID, SegmentID: c.Item.SegmentID, Lesson: text,
			}); err != nil {
			d.log("LESSON from %s could not be recorded: %v", runID, err)
			return
		}
	}
	if kept > 0 {
		d.log("LEARNED %s from %s — carried into future %s runs", plural(kept, "lesson", "lessons"), runID, c.Worker)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// oneLine flattens a detail so one refusal does not become thirty lines.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

// amendProposedItem records a corrected version of an item a validator rejected.
//
// It goes through the same admission rules a new item does — an area nobody
// owns, or a criterion no command can check, is refused here exactly as it
// would be on a new proposal. What it does not do is create anything: the item
// keeps its id, its history and its attempts, and the record shows one item
// that was corrected rather than two that disagree.
//
// One event per field that actually moved, which is the shape item.amended has
// always had. A field whose value is unchanged writes nothing, so the history
// of an item reads as the list of things somebody altered about it.
func (d *Dispatcher) amendProposedItem(runID string, c Candidate, seg ledger.Segment, p envelope.ProposedItem) error {
	before, err := d.Led.Item(p.ID)
	if err != nil {
		return err
	}
	all, err := d.Led.Items("")
	if err != nil {
		return err
	}
	segScopes, openScopes := authority.ScopesFor(seg.ID, all)
	// An amendment is the SAME item corrected, so the scope it already holds is
	// not a collision with itself. Leaving it in would refuse every correction
	// that kept the files it was already scoped to — which is most of them, and
	// would make the repair path unusable for exactly the items it exists for.
	delete(openScopes, p.ID)
	facts := authority.GenerationFacts{
		SegmentID: seg.ID, SegmentBrief: seg.Brief,
		// The id is MEANT to exist this time, so it is not offered as a clash.
		ExistingIDs:   map[string]bool{},
		SegmentScopes: segScopes, OpenScopes: openScopes,
	}
	dec := authority.AdmitItem(d.Cfg, p, facts)
	if _, err := d.Led.Append(d.Actor, ledger.KindItemProposed, p.ID, ledger.ItemProposed{
		RunID: runID, Worker: c.Worker, SegmentID: seg.ID, ProposedID: p.ID,
		Title: p.Title, Admitted: dec.Admitted,
		Reason: string(dec.Reason), Detail: dec.Detail,
	}); err != nil {
		return err
	}
	if !dec.Admitted {
		d.log("REFUSED amendment to %s  [%s] %s", p.ID, dec.Reason, dec.Detail)
		return nil
	}
	radius := p.Radius
	if radius == "" {
		radius = string(config.RadiusNone)
	}
	why := fmt.Sprintf("corrected by %s after the breakdown was rejected (run %s)", c.Worker, runID)
	fields := []struct{ name, was, now string }{
		{"title", before.Title, p.Title},
		{"area", before.Area, p.Area},
		{"blast_radius", before.Radius, radius},
		{"resources", listOf(before.Resources), listOf(p.Resources)},
		{"file_scope", listOf(before.FileScope), listOf(p.FileScope)},
		{"criteria", listOf(before.Criteria), listOf(p.Criteria)},
		{"depends_on", listOf(before.DependsOn), listOf(p.DependsOn)},
	}
	moved := 0
	for _, f := range fields {
		if f.now == "" || f.now == f.was {
			continue
		}
		if _, err := d.Led.Append(d.Actor, ledger.KindItemAmended, p.ID, ledger.ItemAmended{
			ItemID: p.ID, Field: f.name, Value: f.now, Why: why,
		}); err != nil {
			return err
		}
		moved++
	}
	if moved == 0 {
		d.log("AMENDMENT to %s changed nothing — the planner re-filed the item exactly as it was, which does not answer a rejection", p.ID)
		return nil
	}
	d.log("AMENDED %s (%s) — the rejected item is corrected in place rather than duplicated",
		p.ID, plural(moved, "field", "fields"))
	return nil
}

// listOf renders a list the way the projection stores one, so an amendment is
// compared against what is actually on the row.
func listOf(v []string) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// maxPlanRejections is how many times a breakdown may be rejected before the
// fleet stops and asks a person.
//
// There was no limit. An item has max_attempts; a DELIVERABLE had nothing, so
// plan and validate could pass a rejection back and forth for as long as the
// budget held. It did: three planning runs, two rejections, two hours, and not
// one line of code — while every lane reported healthy and the plan grew from
// four items to eight to twelve.
//
// Three is chosen to allow the honest case. The first rejection is normal, the
// second is a planner that misread the objection, and by the third the two
// agents disagree about something neither is going to resolve by trying again.
// That is a question for a person, and asking one costs a fraction of another
// round.
const maxPlanRejections = 3

// loopQuestionID names the question for one threshold of rejections.
//
// The threshold is in the id on purpose. An answer has to unblock the thing it
// was asked about — the first version asked once, was answered, and went on
// refusing to dispatch because the rejection count only ever climbs. A person
// answered the question and the fleet stayed stopped, which is the definition
// of stuck and is worse than never asking.
//
// Encoding the threshold means answering buys another round of attempts, and if
// those also fail a NEW question is asked at the next threshold rather than the
// old answer silently covering it.
func loopQuestionID(segmentID string, threshold int) string {
	return fmt.Sprintf("Q-%s-plan-loop-%d", segmentID, threshold)
}

// planIsLooping reports how many times this deliverable's breakdown has been
// rejected, and whether the fleet should stop and ask about it.
//
// It stops only while the question for the current threshold is unanswered. A
// stop nobody can clear is not a safeguard, it is a stall with a good excuse.
func (d *Dispatcher) planIsLooping(segmentID string) (int, bool) {
	if segmentID == "" {
		return 0, false
	}
	runs, err := d.Led.Runs("", 300)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, r := range runs {
		if r.SegmentID == segmentID && r.Verdict == "reject" {
			n++
		}
	}
	if n < maxPlanRejections {
		return n, false
	}
	threshold := (n / maxPlanRejections) * maxPlanRejections
	q, qerr := d.Led.Question(loopQuestionID(segmentID, threshold))
	if qerr == nil && q.Answered {
		// Asked and answered at this threshold. Carry on until the next one.
		return n, false
	}
	return n, true
}

// stopTheLoop parks a deliverable and asks a person, once per threshold.
//
// A blocking question rather than a silent hold: an item that stops with no
// stated reason is indistinguishable from one nobody has got to yet, and that
// is the failure this whole tool exists to make impossible.
func (d *Dispatcher) stopTheLoop(seg ledger.Segment, rejections int) {
	threshold := (rejections / maxPlanRejections) * maxPlanRejections
	id := loopQuestionID(seg.ID, threshold)
	if q, err := d.Led.Question(id); err == nil && q.ID != "" {
		return // already asked; asking again every tick is not asking harder
	}
	text := fmt.Sprintf(
		"The breakdown of %s (%s) has now been rejected %d times, and each round costs a planning run and a validation run. The two agents are not converging. What should happen: is the plan wrong, is the validator's bar wrong for a deliverable this size, or is the brief asking for something that cannot be decomposed the way this pipeline expects?",
		seg.ID, seg.Title, rejections)

	// The evidence is the last rejection itself, not a note telling somebody to
	// go and read it. A recommendation that makes you open another page before
	// you can weigh it is not a recommendation; it is a chore with an opinion
	// attached, and the first version of this question was exactly that.
	said := d.whyThePlanCameBack(seg.ID)
	if said == "" {
		said = "The record does not carry what the last review said, which is itself worth knowing."
	}
	lean := fmt.Sprintf(
		"Read the objection below before deciding. If it is smaller than the one before it, the two are converging and one more attempt is reasonable — answering this releases the deliverable for another round. If it is the same objection in different words, the plan is not what needs changing: either the brief is asking for something this pipeline cannot decompose, or the validator's bar is wrong for work whose blast radius is %s. Say which, and say it as an instruction the next planner can act on.",
		radiusOrUnknown(d.widestRadius(seg.ID)))
	if _, err := d.Led.Append(d.actor(), ledger.KindQuestionRaised, id, ledger.QuestionRaised{
		ID: id, Blocking: true, Text: text, Lean: lean, RaisedBy: seg.ID,
		Evidence: said,
	}); err != nil {
		d.log("could not raise the plan-loop question for %s: %v", seg.ID, err)
		return
	}
	d.log("STOPPED %s — its breakdown has been rejected %d times and the fleet is not converging. Raised %s. Answering it releases the deliverable for another round.",
		seg.ID, rejections, id)
}

// howMuchRigour is the effort this work is worth, in the run's own terms.
//
// Every role was written for a change that reaches production, and every role
// applied that to everything. A validator spent seventeen minutes reviewing a
// four-item plan for a static page for two children — re-running the gate,
// executing probes, re-deriving claims — and did it three times. Nine runs and
// two hours went into planning a thing a person would have written in twenty
// minutes, and not one of those runs was wrong to be careful. They were never
// told what was at stake.
//
// So the stake is stated. It is derived from the blast radius already declared
// on the work rather than invented here: `none` is work that reaches nothing,
// and the ceremony that protects a production change is pure cost on it.
func (d *Dispatcher) howMuchRigour(c Candidate) string {
	radius := c.Item.Radius
	if c.Kind == KindSegment {
		radius = d.widestRadius(c.Segment.ID)
	}
	switch radius {
	case "", string(config.RadiusNone):
		return "## How much rigour this is worth\n\n" +
			"Blast radius **none**: nothing here reaches a running system, a machine, or anybody's data. " +
			"The worst outcome is a file somebody deletes.\n\n" +
			"Match your effort to that. Read what you need and no more; trust a check you have just " +
			"run rather than re-deriving it a second way; do not propose new tooling, new checks or CI " +
			"changes unless the work cannot be done without them. If this is taking longer than a " +
			"careful person would spend on it, you are over-working it — report what you have.\n\n" +
			"Being thorough where thoroughness buys nothing is not free: it is paid for in the time " +
			"somebody is waiting, and in the rounds of review your own volume then costs."
	case string(config.RadiusHost):
		return "## How much rigour this is worth\n\n" +
			"Blast radius **host**: this reaches one machine. Verify what you claim by running it, " +
			"and say plainly what you could not check."
	}
	return "## How much rigour this is worth\n\n" +
		"Blast radius **" + radius + "**: this reaches something real and hard to take back. " +
		"Re-derive the load-bearing claims by execution rather than by reading, name what you could " +
		"not prove, and prefer refusing to guessing. Here the ceremony is the point."
}

// widestRadius is the largest blast radius among a deliverable's items, which
// is the only stake signal that exists before an approval is requested.
func (d *Dispatcher) widestRadius(segmentID string) string {
	items, err := d.Led.Items(segmentID)
	if err != nil || len(items) == 0 {
		return ""
	}
	widest := ""
	rank := -1
	for _, it := range items {
		if r := config.Radius(it.Radius).Rank(); r > rank {
			rank, widest = r, it.Radius
		}
	}
	return widest
}

// radiusOrUnknown names a deliverable's blast radius for a person, and says so
// when nothing has declared one rather than implying it is safe.
func radiusOrUnknown(r string) string {
	if strings.TrimSpace(r) == "" {
		return "not yet declared"
	}
	return r
}

// learnFromRejection turns a rejected plan into a lesson the next planner gets.
//
// The fleet was recording rejections and learning nothing from them. Lessons
// only ever came from an improver, and an improver runs after an item MERGES —
// so a deliverable that never got past planning could be rejected four times
// and the fifth planner would start with exactly what the first one had. The
// system got no smarter as it got more expensive, which is the opposite of the
// point, and the only escalation available was to stop and ask a person.
//
// A rejection is the cheapest lesson there is: somebody has already written
// down what was wrong, in the envelope, at the moment they refused it. Keeping
// it costs one append and it reaches every future run of that role — on this
// deliverable and on every other one.
//
// Recorded by the control plane from what the run reported, like everything
// else. The validator does not decide that its objection becomes doctrine; it
// reports, and this keeps it.
func (d *Dispatcher) learnFromRejection(runID string, c Candidate, env *envelope.Envelope) {
	said := strings.TrimSpace(env.Summary)
	if said == "" {
		return
	}
	// The planner is the role that has to act on it, not the validator that
	// raised it. A lesson filed against the role that wrote the objection would
	// be read by the one agent that already knows.
	worker, ok := d.Cfg.OwnerFor("", config.CapPlan)
	if !ok {
		return
	}
	lesson := oneLine(said, 600)
	if _, err := d.Led.Append(d.actor(), ledger.KindLessonRecorded,
		fmt.Sprintf("%s-rejection", runID), ledger.LessonRecorded{
			RunID: runID, Worker: worker, SegmentID: c.Segment.ID,
			Lesson: "A breakdown was rejected for this: " + lesson,
		}); err != nil {
		d.log("could not keep the lesson from %s: %v", runID, err)
		return
	}
	d.log("LEARNED from %s — the objection is carried into every future %s run, not just the next one on %s",
		runID, worker, c.Segment.ID)
}

// learnFromMalformed turns a refused envelope into a lesson the next run of
// this role is told.
//
// A malformed envelope is refused before recordLessons is reached, so before
// this existed the same shape was written again by the next run — which had no
// way to see the refusal. Three runs wrote criteria as an object keyed by id
// over one night, each one throwing away the work it had already done. The
// parser now accepts the shapes that cost nothing to accept; this is for the
// ones it must still refuse.
func (d *Dispatcher) learnFromMalformed(runID string, c Candidate, detail string) {
	lesson := fmt.Sprintf(
		"The envelope is a schema, not a suggestion: a %s run was refused for %s and everything it had done was discarded. Write the declared field names and the declared vocabulary.",
		c.Worker, oneLine(strings.TrimPrefix(detail, "malformed envelope: "), 180))
	if _, err := d.Led.Append(d.actor(), ledger.KindLessonRecorded,
		runID+"-LM", ledger.LessonRecorded{
			RunID: runID, Worker: c.Worker,
			// Deliberately no area: a lesson carries to any role working the same
			// area, and this one is about a role's own output format. A tester has
			// no use for how a validator misspelled a verdict.
			ItemID: c.Item.ID, SegmentID: c.Item.SegmentID, Lesson: lesson,
		}); err != nil {
		d.log("LESSON from %s could not be recorded: %v", runID, err)
		return
	}
	d.log("LEARNED from the refusal of %s — carried into future %s runs", runID, c.Worker)
}

// failedTasks names the tasks that did not clear and what each observed, so
// one line in the record and one line in the log say the same thing.
func failedTasks(failed map[string]string) string {
	var parts []string
	for _, c := range authority.VerificationCapabilities() {
		detail, did := failed[c]
		if !did {
			continue
		}
		if strings.TrimSpace(detail) == "" {
			parts = append(parts, c)
			continue
		}
		parts = append(parts, c+": "+oneLine(detail, 200))
	}
	return strings.Join(parts, "; ")
}

// decisionsShown bounds how much settled history a run carries. Enough that it
// does not re-ask, few enough that the prompt stays about the work.
const decisionsShown = 6

// decisionsTaken is what has already been settled on this item.
//
// A question is answered, the item unblocks, and the run that picks it up is a
// fresh agent with no memory: it re-derives the decision or asks it again. Two
// runs raised the same question about the same acceptance criterion in one
// night, neither able to see the other's, and both stopped for a person. The
// answer is a fact about the item and belongs in the brief with the rest of
// them.
func (d *Dispatcher) decisionsTaken(itemID string) string {
	if itemID == "" {
		return ""
	}
	qs, err := d.Led.Questions(itemID, false)
	if err != nil {
		return ""
	}
	var answered []ledger.Question
	for _, q := range qs {
		if q.Answered && strings.TrimSpace(q.Answer) != "" {
			answered = append(answered, q)
		}
	}
	if len(answered) == 0 {
		return ""
	}
	if len(answered) > decisionsShown {
		answered = answered[len(answered)-decisionsShown:]
	}
	var b strings.Builder
	b.WriteString("## Decisions already taken on this item\n\nThese were asked by an earlier run and answered. They are settled: do not ask them again, and do not quietly decide otherwise. If one is wrong in the light of something you have found, say which and why in your summary.\n\n")
	for _, q := range answered {
		fmt.Fprintf(&b, "- **Asked:** %s\n", oneLine(q.Text, 300))
		who := q.AnsweredBy
		if who == "" {
			who = "the operator"
		}
		fmt.Fprintf(&b, "  **Answered by %s:** %s\n", who, oneLine(q.Answer, 500))
	}
	return b.String()
}

// joinSections puts the non-empty ones together, so an absent section leaves no
// dangling heading for a run to reason about.
func joinSections(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, "\n\n")
}

// selfImprovementSegmentID is the one deliverable the fleet may propose into
// without a person having asked for it. One, fixed, so self-improvements
// accumulate somewhere a person can read them all at once rather than being
// scattered through whatever deliverable happened to provoke each.
const selfImprovementSegmentID = "SELF"

// selfImprovementSegment returns the deliverable an improver's proposals are
// filed under, creating it on the roadmap the first time.
//
// It is created at `roadmap` deliberately, which is the state that waits for a
// person. Items can be filed into it, are visible on every surface, and are
// skipped by Candidates for a stated reason until somebody signs it off — the
// same gate every other idea passes, applied to the one source of work nobody
// asked for. The alternative the fleet ran on was that an improver's proposal
// became dispatchable the moment it was written.
func (d *Dispatcher) selfImprovementSegment(provoked ledger.Segment) (ledger.Segment, error) {
	if s, err := d.Led.Segment(selfImprovementSegmentID); err == nil {
		return s, nil
	}
	// TargetOpen 0: this deliverable is never topped up. A planner asked to keep
	// N items in flight here would invent maintenance to fill the quota, which
	// is the failure the improver already has to be argued out of.
	if _, err := d.Led.Append(d.Actor, ledger.KindSegmentCreated, selfImprovementSegmentID, ledger.SegmentCreated{
		ID:    selfImprovementSegmentID,
		Title: "The fleet's own repairs",
		Brief: "Defects the fleet found in itself while doing other work. Each item names the run " +
			"or refusal that provoked it. Nothing here is built until somebody signs this off, " +
			"because work nobody asked for is exactly the work that needs asking about.",
		Rationale:  "Raised by improvers, first while working on " + provoked.ID + ".",
		TargetOpen: 0,
	}); err != nil {
		return ledger.Segment{}, err
	}
	d.log("SELF-IMPROVEMENT %s created on the roadmap; it waits for you before any of it is built", selfImprovementSegmentID)
	return d.Led.Segment(selfImprovementSegmentID)
}
