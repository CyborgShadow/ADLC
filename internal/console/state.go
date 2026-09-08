package console

import (
	"fmt"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Brief is the state of the fleet, rendered for the console agent.
//
// The agent has no memory and no tools of its own here. Everything it knows
// about the project comes from this text, which means the text has to carry the
// same facts the dashboard shows a person — including the unflattering ones. A
// briefing that omitted blocked items and stalled lanes would produce a console
// that cheerfully reports progress while the fleet is stopped.
func Brief(
	cfg *config.Config,
	segs []ledger.Segment,
	items []ledger.Item,
	questions []ledger.Question,
	approvals []ledger.Approval,
	lanes []ledger.LoopHealth,
	now time.Time,
) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## The fleet right now — %s\n\n", now.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "Project `%s`. Console authority: **%s** — %s.\n\n",
		cfg.Project, cfg.Console.Authority, cfg.Console.Authority.Describe())

	// --- deliverables
	b.WriteString("### Deliverables\n\n")
	if len(segs) == 0 {
		b.WriteString("None. The roadmap is empty.\n\n")
	} else {
		byID := map[string][]ledger.Item{}
		for _, it := range items {
			byID[it.SegmentID] = append(byID[it.SegmentID], it)
		}
		for _, s := range segs {
			st := authority.SegmentState(s.State)
			p := authority.Progress(byID[s.ID])
			fmt.Fprintf(&b, "- `%s` **%s** — state `%s`", s.ID, s.Title, s.State)
			if st.NeedsPerson() {
				b.WriteString(" · **waiting for a person to sign it off**")
			}
			fmt.Fprintf(&b, " · %d/%d items done", p.Done, p.Total)
			if s.TargetOpen > 0 {
				fmt.Fprintf(&b, " · target %d open", s.TargetOpen)
			}
			b.WriteString("\n")
			if s.Brief != "" {
				fmt.Fprintf(&b, "  - intent: %s\n", oneLine(s.Brief))
			}
			if s.Rationale != "" {
				fmt.Fprintf(&b, "  - why it matters: %s\n", oneLine(s.Rationale))
			}
		}
		b.WriteString("\n")
	}

	// --- items, grouped by the phase a person reads
	b.WriteString("### Work items by stage\n\n")
	if len(items) == 0 {
		b.WriteString("None.\n\n")
	} else {
		byStage := map[string][]ledger.Item{}
		for _, it := range items {
			k := authority.StageOf(authority.State(it.State)).Key
			byStage[k] = append(byStage[k], it)
		}
		for _, st := range authority.Stages() {
			group := byStage[st.Key]
			if len(group) == 0 {
				continue
			}
			fmt.Fprintf(&b, "**%s** (%d)\n", st.Label, len(group))
			for _, it := range group {
				fmt.Fprintf(&b, "- `%s` %s — `%s`, area `%s`, radius `%s`, %d attempt(s)\n",
					it.ID, oneLine(it.Title), it.State, it.Area, it.Radius, it.Attempts)
			}
			b.WriteString("\n")
		}
	}

	// --- the things that stop work
	b.WriteString("### Open questions\n\n")
	if len(questions) == 0 {
		b.WriteString("None.\n\n")
	} else {
		for _, q := range questions {
			fmt.Fprintf(&b, "- `%s` on `%s`%s: %s\n", q.ID, q.ItemID,
				blockingNote(q.Blocking), oneLine(q.Text))
			if q.Lean != "" {
				fmt.Fprintf(&b, "  - the agent's own recommendation: %s\n", oneLine(q.Lean))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("### Waiting for approval\n\n")
	if len(approvals) == 0 {
		b.WriteString("None.\n\n")
	} else {
		for _, a := range approvals {
			fmt.Fprintf(&b, "- `%s` on `%s` — radius `%s`, plan digest `%s`: %s\n",
				a.ID, a.ItemID, a.Radius, short(a.PlanDigest), oneLine(a.Summary))
		}
		b.WriteString("\n")
	}

	// --- lanes, including the ones that stopped
	b.WriteString("### Lanes\n\n")
	if len(lanes) == 0 {
		b.WriteString("None declared.\n\n")
	} else {
		for _, l := range lanes {
			status := "live"
			switch {
			case !l.Enabled:
				status = "paused"
			case l.LastTickMS == 0:
				status = "NEVER RUN"
			case !l.Fresh(now, 3):
				status = "STALE — it has stopped firing"
			}
			fmt.Fprintf(&b, "- `%s` (%s) every %ds — %s, %d tick(s), %d dispatch(es)\n",
				l.Loop, l.Scope, l.EverySecond, status, l.Ticks, l.Dispatches)
		}
		b.WriteString("\n")
	}

	// --- the roster and the areas, so a raised item can be filed somewhere real
	b.WriteString("### Areas an item may be filed under\n\n")
	if len(cfg.Routing) == 0 {
		b.WriteString("None declared, so no new item can be filed.\n\n")
	} else {
		var areas []string
		for a := range cfg.Routing {
			areas = append(areas, fmt.Sprintf("`%s` (owned by `%s`)", a, cfg.Routing[a]))
		}
		b.WriteString(strings.Join(areas, ", "))
		b.WriteString("\n\nAn area not on that list is refused: an item filed under an area nobody owns is unreachable, and an unreachable item looks exactly like one nobody has got round to.\n\n")
	}

	fmt.Fprintf(&b, "Blast radius policy: anything above `%s` stops for a named person.\n\n",
		cfg.Blast.AutoApplyMax)
	return b.String()
}

// Transcript renders the conversation so far, which is the whole of the agent's
// memory. Turns that failed are included: a console that hides its own failed
// turns will confidently repeat whatever caused them.
func Transcript(turns []ledger.ConsoleTurn) string {
	if len(turns) == 0 {
		return "This is the first thing anyone has said in this conversation.\n"
	}
	var b strings.Builder
	for _, t := range turns {
		fmt.Fprintf(&b, "**%s asked:** %s\n\n", orDash(t.AskedBy), t.Asked)
		switch {
		case !t.Replied:
			b.WriteString("**you replied:** _(this turn was interrupted and never answered)_\n\n")
		case t.Failure != "":
			fmt.Fprintf(&b, "**you replied:** _(the turn failed: %s)_\n\n", t.Failure)
		default:
			fmt.Fprintf(&b, "**you replied:** %s\n\n", t.Reply)
			for _, a := range t.Actions {
				fmt.Fprintf(&b, "  - action `%s` — %s → **%s**", a.Kind, a.Summary, a.Outcome)
				if a.Detail != "" {
					fmt.Fprintf(&b, " (%s)", oneLine(a.Detail))
				}
				b.WriteString("\n")
			}
			if len(t.Actions) > 0 {
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func blockingNote(b bool) string {
	if b {
		return " **(blocking — the item cannot finish until it is answered)**"
	}
	return ""
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "someone"
	}
	return s
}
