package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
)

// The About page exists because everything else in this dashboard assumes you
// already know how the system works. It is the one page written for somebody
// who does not — and it is built from the same tables the control plane runs
// on, so it cannot drift into describing a lifecycle this build does not have.

// aboutStage is one phase of the lifecycle, as a person reads it.
type aboutStage struct {
	Key    string
	Label  string
	Plain  string   // what is happening, in one line, no jargon
	Who    string   // the roles that move it, from the live config
	States []string // the machine states inside this phase
	Gate   string   // what has to be true to leave, empty when nothing gates it
}

// aboutRoadmapStage is one phase of the planning pipeline.
type aboutRoadmapStage struct {
	State  string
	Plain  string
	Who    string
	Person bool
}

func (s *Server) about(*http.Request) (string, any, error) {
	who := func(capability string) string {
		ws := s.Cfg.WorkersWith(capability)
		if len(ws) == 0 {
			return "nobody — this stage is unreachable"
		}
		return strings.Join(ws, ", ")
	}

	// The plain-language line for each phase. Everything else on this page is
	// derived, so this map is the only prose that has to be kept honest, and
	// a phase added to the authority without one shows up as blank rather than
	// silently missing from the diagram.
	plain := map[string]struct{ Text, Cap, Gate string }{
		"planning": {"An idea becomes a list of work items somebody has checked.",
			config.CapPlan, "the plan has been validated against the intent that produced it"},
		"in_progress": {"Somebody builds the thing and writes tests for it.",
			config.CapImplement, "the control plane ran the project's checks itself and they were green"},
		"testing": {"The tests are executed by someone who did not write them.",
			config.CapTest, "the suite ran, and it ran a non-zero number of tests"},
		"review": {"The work is checked against its acceptance criteria, then attacked.",
			config.CapJudge, "every criterion passed, each citing a command that was executed"},
		"reviewed": {"Tidied up, then judged against the system rather than against itself.",
			config.CapArbitrate, "it is coherent with what is already here"},
		"approval": {"Anything that touches something real stops here for a person.",
			config.CapOperate, "a named person approved this exact plan, and it has not changed since"},
		"merge": {"The work lands on the trunk, one branch at a time.",
			"", "it rebases, re-gates green on the rebased tree, and changes only files it touched"},
		"improve": {"What this item taught gets written down, and fixes get raised as work.",
			config.CapImprove, "the lessons pass finished"},
		"done":    {"Finished.", "", ""},
		"stopped": {"Blocked on a question, rejected, cancelled, or replaced.", "", ""},
	}

	byStage := map[string][]string{}
	for _, st := range authority.AllStates() {
		k := authority.StageOf(st).Key
		byStage[k] = append(byStage[k], string(st))
	}

	var stages []aboutStage
	for _, st := range authority.Stages() {
		p := plain[st.Key]
		a := aboutStage{Key: st.Key, Label: st.Label, Plain: p.Text,
			States: byStage[st.Key], Gate: p.Gate}
		switch {
		case p.Cap != "":
			a.Who = who(p.Cap)
		case st.Key == "merge":
			a.Who = "the control plane itself"
		default:
			a.Who = "—"
		}
		stages = append(stages, a)
	}

	roadmap := []aboutRoadmapStage{
		{"theory", "An idea, written down. Nothing is committed to it.", "you", true},
		{"roadmap", "Accepted onto the roadmap and waiting for a person to agree it is worth doing.", "you", true},
		{"signed_off", "Agreed. This is the one planning gate no machine passes on its own.", "you", true},
		{"researching", "Turning the intent into an approach: what exists, the options, which one and why.", who(config.CapResearch), false},
		{"researched", "The approach is written down and ready to break up.", who(config.CapResearch), false},
		{"planning", "Breaking the approach into work items with criteria a command can check.", who(config.CapPlan), false},
		{"planned", "Work items exist. Nobody has checked yet that they add up to the intent.", who(config.CapPlan), false},
		{"validating", "Checking the plan against the original intent — would these items, done, deliver it?", who(config.CapValidate), false},
		{"ready", "The plan was accepted. Only now may any of the work be dispatched.", "—", false},
		{"building", "At least one item has moved.", "—", false},
		{"delivered", "Every item reached a terminal state and at least one is done.", "—", false},
	}

	// The guards, each stated as the failure it prevents rather than as a
	// feature. A guard whose purpose is not obvious gets removed by somebody
	// tidying up.
	type guard struct{ Name, Prevents string }
	guards := []guard{
		{"Agents propose, the tool decides",
			"An agent that could name its own next state can route around every rule on this page. It reports pass, fail, reject or blocked; where the item goes is arithmetic."},
		{"The control plane runs the checks itself",
			"Evidence an agent produced about its own work is a claim. The checks are re-run here, and a claim that disagrees with what was observed refuses the whole transition."},
		{"Three verdicts, not two",
			"GREEN, RED and UNKNOWN. A tool that was missing, a host that did not answer, a suite that matched no tests — none of those are a pass. Absence never renders as green."},
		{"Whoever wrote it does not check it",
			"The run that did the work cannot be the run that judges it, and a criterion citing a command nobody executed is refused."},
		{"Nothing irreversible happens unattended",
			"Every item declares how far it reaches. Above the configured threshold it stops for a named person, and that approval names one exact plan — change the plan and the approval no longer applies."},
		{"One branch lands at a time",
			"The merge queue rebases, re-runs the checks on the rebased tree, and refuses any branch that would change a file its own commits never touched. That last one silently reverts a colleague's work, and every check stays green while it happens."},
		{"Refusals are recorded too",
			"A record that keeps only what it accepted cannot answer the question it will actually be asked, which is why something did not happen."},
		{"Every lane firing leaves a mark",
			"A scheduled lane writes a tick whether or not it found work. A lane that quietly stopped is then a stale timestamp rather than an absence nobody notices."},
	}

	// The roster, grouped by layer, so the page shows this project's actual
	// roles rather than a generic list.
	type roleLine struct{ Type, Does, Can string }
	byLayer := map[string][]roleLine{}
	var layers []string
	for _, w := range s.Cfg.Workers {
		l := w.Layer
		if l == "" {
			l = "worker"
		}
		if _, seen := byLayer[l]; !seen {
			layers = append(layers, l)
		}
		byLayer[l] = append(byLayer[l], roleLine{w.Type, w.Description, strings.Join(w.Capabilities, ", ")})
	}
	// Read in pipeline order, not alphabetically: the roster is a walk through
	// the lifecycle, and sorting it by name puts the platform layer in the middle
	// of the verification one.
	order := map[string]int{"planning": 0, "worker": 1, "verification": 2, "stewardship": 3, "platform": 4}
	rank := func(l string) int {
		if n, ok := order[l]; ok {
			return n
		}
		return len(order)
	}
	sort.SliceStable(layers, func(i, j int) bool {
		if rank(layers[i]) != rank(layers[j]) {
			return rank(layers[i]) < rank(layers[j])
		}
		return layers[i] < layers[j]
	})
	type layerBlock struct {
		Layer string
		Roles []roleLine
	}
	var roster []layerBlock
	for _, l := range layers {
		roster = append(roster, layerBlock{l, byLayer[l]})
	}

	return "About the ADLC", struct {
		Stages     []aboutStage
		Roadmap    []aboutRoadmapStage
		Guards     []guard
		Roster     []layerBlock
		Project    string
		Checks     int
		Workers    int
		Lanes      int
		StateCount int
	}{stages, roadmap, guards, roster, s.Cfg.Project,
		len(s.Cfg.Checks), len(s.Cfg.Workers), len(s.Cfg.Loops), len(authority.AllStates())}, nil
}
