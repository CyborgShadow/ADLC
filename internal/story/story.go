// Package story turns the ledger into an account a person can read, and
// re-derives past decisions to prove they still hold.
//
// The record already answers "what happened" in the sense a machine needs.
// This package answers it in the sense a person asks it three weeks later:
// what was this run given, what did it do, what did the control plane observe,
// what did it decide, and — the question that is usually the point — why
// did any of it matter to the product.
//
// That last one is a chain, not a field: a run advanced an item, the item
// exists to serve a deliverable, and the deliverable has a rationale somebody
// wrote in plain language. Every link is recorded, so the answer is assembled
// rather than remembered.
package story

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
	"github.com/CyborgShadow/ADLC/internal/gate"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// Step is one recorded moment in a run, in the order it happened.
type Step struct {
	At     time.Time
	Kind   string
	Title  string
	Detail string
	// Verdict colours the step in a UI: ok, bad, warn, mute.
	Verdict string
}

// Run is the whole account of one run.
type Run struct {
	Run     ledger.Run
	Item    ledger.Item
	Segment ledger.Segment
	Worker  *config.WorkerDecl

	// Purpose is the chain from this run up to something a person cares about.
	Purpose []string
	// Headline is one sentence: what this run did.
	Headline string
	Steps    []Step

	Gates     []ledger.GateRun
	Proposals []ledger.Proposal
	Questions []ledger.Question

	Cost     spend.Micros
	Duration time.Duration
	// Timeout is the dispatch budget this run was given, so an unfinished run
	// can be told apart from one nobody will ever finish.
	Timeout time.Duration
	// Abandoned is why this run was closed by something other than itself,
	// when it was. Present means the evidence is on the record rather than in
	// somebody's log file.
	Abandoned *ledger.RunAbandoned
	// Reported is what the agent said it did, in its own words.
	//
	// A run's product was retained and readable from the command line and shown
	// on no page at all — so a fifteen-minute research run finished and the only
	// way to find out what it had concluded was to know that
	// `adlc run envelope` existed. The account is a CLAIM, not a verdict, and it
	// is labelled as one wherever it appears.
	Reported      string
	ReportedNotes []string
	// ReportedMD is the longer written account a run may leave — an approach, a
	// set of findings — which is the actual product of a research run.
	ReportedMD     string
	PromptRetained bool
	EnvRetained    bool
	// Reproducible reports whether this run's decision can be re-derived. It
	// needs the envelope bytes and the commit the evidence describes; without
	// both, the run is described but not checkable.
	Reproducible bool
	WhyNot       string
}

// OfRun assembles the account of one run.
func OfRun(l *ledger.Ledger, cfg *config.Config, runID string) (*Run, error) {
	r, err := l.Run(runID)
	if err != nil {
		return nil, err
	}
	s := &Run{Run: r, Worker: cfg.Worker(r.WorkerType),
		Timeout: time.Duration(cfg.Dispatch.TimeoutSeconds) * time.Second}
	s.Cost = spend.Micros(r.CostMicros)
	if r.Finished() {
		s.Duration = time.UnixMilli(r.FinishedMS).Sub(time.UnixMilli(r.StartedMS))
	}
	if ab, ok, aerr := l.Abandonment(runID); aerr == nil && ok {
		s.Abandoned = &ab
	}
	if l.HasBlob(r.EnvelopeSHA) {
		if raw, berr := l.Blob(r.EnvelopeSHA); berr == nil {
			if env, perr := envelope.Parse(raw); perr == nil {
				s.Reported = strings.TrimSpace(env.Summary)
				s.ReportedNotes = env.Outputs.Deferred
				s.ReportedMD = strings.TrimSpace(env.Outputs.Notes)
			}
		}
	}
	s.PromptRetained = l.HasBlob(r.PromptSHA)
	s.EnvRetained = l.HasBlob(r.EnvelopeSHA)

	if r.ItemID != "" {
		if it, err := l.Item(r.ItemID); err == nil {
			s.Item = it
		}
	}
	if r.SegmentID != "" {
		if sg, err := l.Segment(r.SegmentID); err == nil {
			s.Segment = sg
		}
	}
	s.Purpose = purpose(s.Item, s.Segment)
	s.Gates, _ = gatesFor(l, runID)
	s.Proposals, _ = proposalsFor(l, runID)
	s.Questions, _ = questionsFor(l, runID)
	s.Steps = steps(s)
	s.Headline = headline(s)

	switch {
	case !s.EnvRetained:
		s.WhyNot = "the envelope this run produced was not retained, so its decision cannot be re-derived"
	case r.HeadSHA == "" && r.ItemID != "":
		s.WhyNot = "this run named no commit, so there is no tree to re-run the checks against"
	default:
		s.Reproducible = true
	}
	return s, nil
}

// purpose walks up from the run to something a person cares about.
func purpose(it ledger.Item, sg ledger.Segment) []string {
	var out []string
	if it.ID != "" {
		line := fmt.Sprintf("Work item %s — %s", it.ID, it.Title)
		if it.Rationale != "" {
			line += ". " + it.Rationale
		}
		out = append(out, line)
	}
	if sg.ID != "" {
		line := fmt.Sprintf("Deliverable %s — %s", sg.ID, sg.Title)
		if sg.Rationale != "" {
			line += ". " + sg.Rationale
		} else if sg.Brief != "" {
			line += ". " + sg.Brief
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		out = append(out, "This run was not tied to a work item, so the record cannot say what it was for. That is a gap in how it was dispatched, not in the record.")
	}
	return out
}

func headline(s *Run) string {
	r := s.Run
	who := r.WorkerType
	switch {
	// An agent inside its timeout is working. Saying it "never recorded an end"
	// about a run three minutes into a thirty-minute budget describes a failure
	// that has not happened — and reads as one.
	//
	// Only when a timeout is declared. With nothing to measure against there is
	// no evidence either way, and UNKNOWN is the honest answer rather than a
	// cheerful guess that it is fine.
	// Closed by the fleet rather than by itself, and the record says why. The
	// verdict stays UNKNOWN — nobody can say what the agent did — but WHY nobody
	// can say is known, and printing a bare "unknown" threw that away.
	case s.Abandoned != nil:
		return s.Abandoned.Why
	case s.Timeout > 0 && r.StandingAt(time.Now(), s.Timeout) == ledger.StandingWorking:
		return fmt.Sprintf("%s is running. It started %s ago and has not reported yet — an absent verdict here means not finished, not failed.",
			who, time.Since(time.UnixMilli(r.StartedMS)).Round(time.Second))
	case !r.Finished():
		return fmt.Sprintf("%s started and never recorded an end. That is an UNKNOWN, not a failure and not a pass — nobody knows what it did.", who)
	case len(s.Proposals) > 0 && s.Proposals[0].Admitted:
		return fmt.Sprintf("%s moved %s from %s to %s.", who, r.ItemID, s.Proposals[0].From, s.Proposals[0].To)
	case len(s.Proposals) > 0:
		return fmt.Sprintf("%s was refused: %s.", who, s.Proposals[0].Reason)
	case r.Verdict == "pass":
		return fmt.Sprintf("%s finished and reported success without moving anything.", who)
	}
	return fmt.Sprintf("%s finished with verdict %q.", who, r.Verdict)
}

func steps(s *Run) []Step {
	r := s.Run
	var out []Step
	at := func(ms int64) time.Time { return time.UnixMilli(ms) }

	out = append(out, Step{
		At: at(r.StartedMS), Kind: "dispatched", Verdict: "mute",
		Title: fmt.Sprintf("Dispatched as %s", r.WorkerType),
		Detail: fmt.Sprintf("prompt %s (%s), base commit %s, workspace %s",
			r.PromptID, shortSHA(r.PromptSHA), shortSHA(r.BaseSHA), r.WorkDir),
	})
	for _, g := range s.Gates {
		v := "ok"
		if g.Status == "RED" {
			v = "bad"
		} else if g.Status != "GREEN" {
			v = "warn"
		}
		detail := fmt.Sprintf("the control plane ran the checks itself over %s", shortSHA(g.TreeSHA))
		if g.Dirty {
			detail += " — and the tree carried uncommitted changes"
		}
		out = append(out, Step{At: at(g.TsMS), Kind: "gate", Verdict: v,
			Title: fmt.Sprintf("Gate %s on %s", g.Status, g.Edge), Detail: detail})
	}
	if r.Finished() {
		v := "ok"
		if r.Verdict != "pass" {
			v = "warn"
		}
		out = append(out, Step{At: at(r.FinishedMS), Kind: "reported", Verdict: v,
			Title: fmt.Sprintf("Agent reported %q", r.Verdict),
			Detail: fmt.Sprintf("%d in / %d out tokens, %s",
				r.Usage.InputTokens, r.Usage.OutputTokens, spend.Micros(r.CostMicros))})
	}
	for _, q := range s.Questions {
		out = append(out, Step{At: at(r.FinishedMS), Kind: "question", Verdict: "warn",
			Title: "Raised a question", Detail: q.Text})
	}
	for _, p := range s.Proposals {
		v, title := "ok", fmt.Sprintf("Advanced %s → %s", p.From, p.To)
		if !p.Admitted {
			v = "bad"
			title = fmt.Sprintf("Refused: %s", p.Reason)
		}
		out = append(out, Step{At: at(p.TsMS), Kind: "decision", Verdict: v,
			Title: title, Detail: p.Detail})
	}
	return out
}

// ---------------------------------------------------------------- replay

// Replay is the result of re-deriving a past decision.
type Replay struct {
	RunID string
	// Recorded is what the authority decided at the time.
	Recorded string
	// Rederived is what it decides now, from the same recorded inputs.
	Rederived string
	Agrees    bool
	Detail    string
	// GateNow is what the checks say against the recorded commit today. It is
	// reported separately from the decision because a tree can legitimately
	// change; a decision cannot.
	GateNow string
	Notes   []string
}

// Rederive re-runs a past decision from the record and compares.
//
// It replays the DECISION, not the agent. Re-invoking a language model does
// not reproduce anything and claiming otherwise would be dishonest — but
// everything the control plane did is a pure function of recorded inputs, and
// that part reproduces exactly. So this re-parses the retained envelope,
// re-runs the declared checks against the commit the run named, and puts the
// result back through the same authority. If the answer differs, either the
// rules changed or the record did, and both are worth knowing.
//
// A disagreement is not automatically a defect. Rules are allowed to get
// stricter, and a run admitted under an older policy may be refused under a
// newer one. What matters is that the difference is visible and explicable
// rather than silent.
func Rederive(ctx context.Context, l *ledger.Ledger, cfg *config.Config, repo, runID string, now time.Time) (*Replay, error) {
	s, err := OfRun(l, cfg, runID)
	if err != nil {
		return nil, err
	}
	rp := &Replay{RunID: runID, Recorded: "(no decision recorded)"}
	if len(s.Proposals) > 0 {
		p := s.Proposals[0]
		if p.Admitted {
			rp.Recorded = fmt.Sprintf("admitted %s -> %s", p.From, p.To)
		} else {
			rp.Recorded = fmt.Sprintf("refused [%s]", p.Reason)
		}
	}
	if !s.EnvRetained {
		rp.Detail = s.WhyNot
		return rp, nil
	}
	raw, err := l.Blob(s.Run.EnvelopeSHA)
	if err != nil {
		rp.Detail = err.Error()
		return rp, nil
	}
	env, err := envelope.Parse(raw)
	if err != nil {
		rp.Rederived = "malformed_envelope"
		rp.Detail = err.Error()
		return rp, nil
	}

	// A generation run made a different kind of decision: it proposed work items,
	// and each one was admitted or refused on its own. Re-deriving that means
	// re-running the admission rules over the same proposals.
	if len(env.Outputs.WorkItems) > 0 {
		return rederiveGeneration(l, cfg, s, env, rp)
	}

	from := authority.State(s.Item.State)
	if len(s.Proposals) > 0 {
		from = authority.State(s.Proposals[0].From)
	}
	capability := ""
	if s.Worker != nil && len(s.Worker.Capabilities) > 0 {
		capability = s.Worker.Capabilities[0]
	}
	adv := authority.NextState(from, capability, env.Verdict,
		config.Radius(s.Item.Radius), cfg.Blast)
	if !adv.Inferred {
		rp.Rederived = "no move"
		rp.Detail = adv.Stall
		rp.Agrees = strings.HasPrefix(rp.Recorded, "(no decision")
		return rp, nil
	}

	dir := repo
	r := &gate.Runner{Cfg: cfg, Dir: dir, Artifact: env.Artifact,
		Vars: map[string]string{"workdir": dir, "artifact": env.Artifact}}
	gres, err := r.RunEdge(ctx, string(from), string(adv.To))
	if err != nil {
		return nil, err
	}
	rp.GateNow = string(gres.Status)
	if s.Run.BaseSHA != "" && gres.TreeSHA != s.Run.HeadSHA {
		rp.Notes = append(rp.Notes, fmt.Sprintf(
			"the checks ran against %s, not the %s this run described — the tree has moved since, so a gate difference is expected and a decision difference is not",
			shortSHA(gres.TreeSHA), shortSHA(s.Run.HeadSHA)))
	}

	facts, err := authority.Gather(l, s.Item.ID, runID, func(sha string) bool {
		return gate.CommitExists(repo, sha)
	}, env.HeadSHA, now)
	if err != nil {
		return nil, err
	}
	// Re-derive against the state the item was in AT THE TIME, not the state it
	// is in now. Replaying against today's state would answer a different
	// question and always disagree.
	facts.Item.State = string(from)
	facts.RunStarted = true

	dec := authority.New(cfg).Decide(authority.Request{
		Actor: "replay", Worker: s.Run.WorkerType, RunID: runID,
		From: from, To: adv.To, Env: env, Gate: gres, Reason: adv.Why, Now: now,
	}, facts)
	if dec.Admitted {
		rp.Rederived = fmt.Sprintf("admitted %s -> %s", from, adv.To)
	} else {
		rp.Rederived = fmt.Sprintf("refused [%s]", dec.Reason)
		rp.Detail = dec.Detail
	}
	rp.Agrees = rp.Rederived == rp.Recorded
	return rp, nil
}

// ---------------------------------------------------------------- helpers

func gatesFor(l *ledger.Ledger, runID string) ([]ledger.GateRun, error) {
	return l.GatesForRun(runID)
}

func proposalsFor(l *ledger.Ledger, runID string) ([]ledger.Proposal, error) {
	all, err := l.Proposals("", false, 0)
	if err != nil {
		return nil, err
	}
	var out []ledger.Proposal
	for _, p := range all {
		if p.RunID == runID {
			out = append(out, p)
		}
	}
	return out, nil
}

func questionsFor(l *ledger.Ledger, runID string) ([]ledger.Question, error) {
	all, err := l.Questions("", false)
	if err != nil {
		return nil, err
	}
	var out []ledger.Question
	for _, q := range all {
		if q.RaisedBy == runID {
			out = append(out, q)
		}
	}
	return out, nil
}

func shortSHA(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// rederiveGeneration re-runs the item-admission rules over the same proposals.
//
// A planning run's decision is not a transition, it is a set of admit/refuse
// calls — one per proposed work item — and those are just as much a pure
// function of recorded inputs as anything else here. Re-deriving them answers
// a question worth asking: would the same breakdown be accepted by today's
// rules?
//
// The comparison deliberately excludes duplicate-id refusals. An item admitted
// the first time round exists now, so replaying the same proposals must find
// it already taken; treating that as a disagreement would make every
// generation run fail replay for the exact reason it succeeded.
func rederiveGeneration(l *ledger.Ledger, cfg *config.Config, s *Run, env *envelope.Envelope, rp *Replay) (*Replay, error) {
	all, err := l.Items("")
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	created := map[string]bool{}
	for _, it := range all {
		existing[it.ID] = true
	}
	for _, p := range s.Proposals {
		if p.To == "created" && p.Admitted {
			created[p.ItemID] = true
		}
	}
	facts := GenerationFactsFor(s.Segment)
	var agree, differ int
	for _, p := range env.Outputs.WorkItems {
		f := facts
		f.ExistingIDs = map[string]bool{}
		for id := range existing {
			// Items this very run created are excluded, so the replay sees the world as
			// it was when the decision was made.
			if !created[id] {
				f.ExistingIDs[id] = true
			}
		}
		dec := authority.AdmitItem(cfg, p, f)
		wasAdmitted := created[p.ID]
		if dec.Admitted == wasAdmitted {
			agree++
			continue
		}
		differ++
		rp.Notes = append(rp.Notes, fmt.Sprintf(
			"%s was %s then and is %s now%s", p.ID,
			admittedWord(wasAdmitted), admittedWord(dec.Admitted), detailSuffix(dec.Detail)))
	}
	rp.Recorded = fmt.Sprintf("%d item(s) admitted", len(created))
	rp.Rederived = fmt.Sprintf("%d of %d proposal(s) decided the same way", agree, agree+differ)
	rp.Agrees = differ == 0
	if rp.Agrees {
		rp.Detail = "every proposal this planning run made is judged the same way by today's rules"
	}
	return rp, nil
}

// GenerationFactsFor builds the admission context for a segment.
func GenerationFactsFor(sg ledger.Segment) authority.GenerationFacts {
	return authority.GenerationFacts{SegmentID: sg.ID, SegmentBrief: sg.Brief}
}

func admittedWord(b bool) string {
	if b {
		return "admitted"
	}
	return "refused"
}

func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	return " — " + d
}
