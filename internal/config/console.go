package config

import "fmt"

// ConsoleAuthority is how much of what the console agent proposes it may
// actually execute.
//
// It is declared rather than decided in code because the answer is a property
// of the project, not of this tool. A fleet building infrastructure that
// reaches production wants a different answer from one building a prototype
// nobody depends on, and the difference is exactly the sort of thing a config
// exists to carry.
type ConsoleAuthority string

const (
	// ConsolePropose executes nothing. Everything the agent suggests renders as
	// a control a person presses. Safe, and inert: a conversation that can only
	// hand back a list of buttons is a smart help page.
	ConsolePropose ConsoleAuthority = "propose"
	// ConsoleAct executes anything the control plane could do on its own, each
	// through the same transition authority as everything else. It never
	// executes the three gates that exist because a person owns them: signing
	// off an idea, answering a blocking question, and approving something
	// irreversible. Those it drafts.
	ConsoleAct ConsoleAuthority = "act"
	// ConsoleFull executes the human gates too.
	//
	// This is a real choice with a real cost, and it is written down here rather
	// than hidden: an approval an agent granted itself is not an approval, and a
	// sign-off that nobody read is not a sign-off. A fleet running like this has
	// no human checkpoint in it. The record still says, on every one of them,
	// that the agent pressed it.
	ConsoleFull ConsoleAuthority = "full"
)

// Known reports whether this build understands the level.
func (a ConsoleAuthority) Known() bool {
	switch a {
	case ConsolePropose, ConsoleAct, ConsoleFull:
		return true
	}
	return false
}

// Rank orders the levels from least to most authority. An unrecognised level
// ranks BELOW the safest one rather than above the widest, so a typo produces
// a console that does nothing rather than one that does everything.
func (a ConsoleAuthority) Rank() int {
	switch a {
	case ConsolePropose:
		return 1
	case ConsoleAct:
		return 2
	case ConsoleFull:
		return 3
	}
	return 0
}

// MayExecute reports whether an action of the given sensitivity runs on its own.
func (a ConsoleAuthority) MayExecute(gated bool) bool {
	if !a.Known() {
		return false
	}
	if gated {
		return a == ConsoleFull
	}
	return a == ConsoleAct || a == ConsoleFull
}

// Describe renders the level for a person, in the terms of what it gives up.
func (a ConsoleAuthority) Describe() string {
	switch a {
	case ConsolePropose:
		return "drafts everything and executes nothing; you press every action"
	case ConsoleAct:
		return "executes what the control plane could do on its own; the gates a person owns it only drafts"
	case ConsoleFull:
		return "executes everything, including the gates a person owns — there is no human checkpoint left"
	}
	return "unrecognised, so it executes nothing"
}

// ConsolePolicy governs the operator console.
//
// The console is a way to drive this tool by talking to it. It is off by
// default: it invokes an agent, an agent costs money, and a surface that starts
// spending without being asked is a surface nobody trusts.
type ConsolePolicy struct {
	Comment string `json:"_comment,omitempty"`
	Enabled bool   `json:"enabled"`
	// Authority is propose, act or full. Empty means propose.
	Authority ConsoleAuthority `json:"authority,omitempty"`
	// Worker is the declared worker the console runs as. It must hold the
	// converse capability, so that a console turn is attributable to a role in
	// the roster like every other run.
	Worker string `json:"worker,omitempty"`
	// TimeoutSeconds bounds one turn. A turn nobody can wait for is a turn the
	// operator abandons, and an abandoned turn that later writes to the ledger
	// is worse than one that failed.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// HistoryTurns is how much of the conversation is replayed into each turn.
	// The agent has no memory between invocations; this is the memory.
	HistoryTurns int `json:"history_turns,omitempty"`
	// Command is argv for a conversational agent. Unlike a lane's command it may
	// contain {{session_args}}, which expands to the flags that start or resume
	// the thread — so the agent keeps the conversation and each turn sends only
	// what changed.
	//
	// Empty falls back to dispatch.command, which is a one-shot: it works, and it
	// pays for the whole transcript again on every turn.
	Command []string `json:"command,omitempty"`
}

// validateConsole fills the defaults and refuses what cannot work.
func (c *Config) validateConsole() error {
	p := &c.Console
	if p.Authority == "" {
		p.Authority = ConsolePropose
	}
	if !p.Authority.Known() {
		return fmt.Errorf(
			"console.authority %q is not one this build knows (propose, act, full). An unrecognised level fails closed and would execute nothing, which is a console that silently does less than you configured — so it is refused instead", p.Authority)
	}
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 180
	}
	if p.HistoryTurns <= 0 {
		p.HistoryTurns = 20
	}
	if len(p.Command) > 0 {
		hasSession := false
		for _, a := range p.Command {
			if a == "{{session_args}}" {
				hasSession = true
			}
		}
		if !hasSession {
			// A console command with no session placeholder starts a new thread
			// every turn — the one-shot behaviour wearing a session-shaped
			// config. Said out loud rather than discovered in a bill.
			return fmt.Errorf(
				"console.command has no {{session_args}}, so every turn would start a fresh conversation and re-send the whole transcript. Add it, or leave console.command empty to use dispatch.command deliberately as a one-shot")
		}
	}
	if !p.Enabled {
		return nil
	}
	if p.Worker == "" {
		ws := c.WorkersWith(CapConverse)
		if len(ws) == 0 {
			return fmt.Errorf(
				"console.enabled is true and no declared worker holds the %q capability, so there is nobody to run a turn as. Declare one, or set console.enabled to false", CapConverse)
		}
		p.Worker = ws[0]
		return nil
	}
	w := c.Worker(p.Worker)
	if w == nil {
		return fmt.Errorf("console.worker %q is not a declared worker", p.Worker)
	}
	if !w.Can(CapConverse) {
		return fmt.Errorf(
			"console.worker %q does not hold the %q capability. A console turn is a run like any other and is attributed to a role in the roster; a worker that cannot converse would appear in the record having done something it is not declared to do", p.Worker, CapConverse)
	}
	return nil
}
