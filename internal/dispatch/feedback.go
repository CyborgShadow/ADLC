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
	facts := authority.GenerationFacts{
		SegmentID: seg.ID, SegmentBrief: seg.Brief,
		// The id is MEANT to exist this time, so it is not offered as a clash.
		ExistingIDs: map[string]bool{},
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
