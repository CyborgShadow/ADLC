package config

import "sort"

// Plain-language descriptions of the declared surface.
//
// They live beside the definitions rather than in the dashboard because a
// value and the sentence explaining it drift apart the moment they are edited
// in two files. A stale explanation of a safety setting is worse than no
// explanation at all: an operator who reads nothing goes and looks, and an
// operator who reads something wrong acts on it.
//
// Each one is written to name what goes wrong at the wrong value, not to
// restate the field name.

// Explain says what a radius actually reaches if the change is wrong.
func (r Radius) Explain() string {
	switch r {
	case RadiusNone:
		return "Source only. There is no apply phase, so the worst case is a commit somebody reverts."
	case RadiusHost:
		return "One machine. It goes down; nothing else notices."
	case RadiusFleet:
		return "A group of machines that fail together — a whole tier can go at once."
	case RadiusRegion:
		return "A region or an entire cloud account. Recovery takes hours and needs somebody awake."
	case RadiusGlobal:
		return "Everything, everywhere, with no untouched copy left to recover from."
	}
	return "Not a radius this build knows. It fails closed, so nothing at this radius is ever applied unattended."
}

// Explain says which channel a verdict is read from, and what reading the
// other one would let through.
func (r VerdictRule) Explain() string {
	switch r {
	case VerdictExitZero:
		return "The exit code is the verdict: zero passes. The output is kept for you to read but is not judged."
	case VerdictExitIn:
		return "Passes on any of the listed exit codes. A planner that returns 2 for \"there are changes\" is succeeding, not failing."
	case VerdictOutputEmpty:
		return "The OUTPUT is the verdict: it passes only if the command printed nothing. gofmt -l exits 0 whether or not it named files it would rewrite, so its exit code carries no information at all."
	case VerdictOutputNonEmpty:
		return "The output is the verdict, inverted: printing nothing fails. For a command whose silence means it never looked."
	case VerdictOutputMatches:
		return "Passes only if the output matches the declared pattern; the exit code is ignored."
	case VerdictOutputNotMatch:
		return "Fails if the output matches the declared pattern — a forbidden-string check."
	case VerdictGoTestJSON:
		return "Parses go test -json and requires that some tests actually reported. A filter matching zero tests would otherwise turn \"I ran nothing\" into \"everything passed\"."
	case VerdictCountMin:
		return "Pulls a count out of the output and requires at least min_count of them. Zero discovered units means the scanner never ran, which must not read as clean."
	}
	return "Not a rule this build knows, so the check cannot be read at all."
}

// Explain says what depth of verification a kind names. It is descriptive: a
// kind never changes how a verdict is read, only what the evidence has to
// carry alongside it.
func (k Kind) Explain() string {
	switch k {
	case KindSource:
		return "Judges the source tree — format, lint, unit tests, build."
	case KindDryRun:
		return "Judges a proposed change without making it. The digest of its output is what an approval approves."
	case KindBehavioural:
		return "Judges the assembled, running artifact, and must record the digest of what it ran against."
	}
	return "Unknown kind."
}

// ConsoleAuthorities lists the levels weakest first, so a comparison can be
// rendered from the declared set rather than from a hand-written copy of it
// that a fourth level would silently fall out of.
func ConsoleAuthorities() []ConsoleAuthority {
	return []ConsoleAuthority{ConsolePropose, ConsoleAct, ConsoleFull}
}

// Advice says when a console level is the right choice, which is the half of
// the trade-off Describe deliberately leaves out.
func (a ConsoleAuthority) Advice() string {
	switch a {
	case ConsolePropose:
		return "Start here. Every turn still costs a run, but nothing it decides reaches the ledger without you pressing it."
	case ConsoleAct:
		return "For a fleet you already let run unattended: the console gets no authority the scheduler did not already have."
	case ConsoleFull:
		return "Only for a project nothing depends on. An approval an agent granted itself is not an approval."
	}
	return "Unrecognised levels execute nothing, so the console is silently inert."
}

// PricedModels lists the models with a price entry, sorted, so a table of them
// renders in the same order twice running.
func (b Budget) PricedModels() []string {
	out := make([]string, 0, len(b.PriceMicrosPerMTok))
	for m := range b.PriceMicrosPerMTok {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
