package main

import (
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// `adlc ledger events` is where a person asks which build wrote a row. These
// pin that it can answer, and that it does not answer an absence with silence:
// a blank column reads as "nothing to say here", and this one has something to
// say.

func TestAnEventLineNamesTheBuildThatAppendedIt(t *testing.T) {
	line := eventLine(ledger.Event{
		Seq: 12, Kind: ledger.KindNoteRecorded, Subject: "S1-001", Actor: "pm",
		BuildRev: "2f6a1c0d4e8b9a7c5d3f1e0b2a4c6d8e0f1a2b3c",
		Hash:     "aaaabbbbccccddddeeee",
	})
	if !strings.Contains(line, "2f6a1c0d4e8b") {
		t.Fatalf("the build should be on the line: %q", line)
	}
	if strings.Contains(line, ledger.RevisionUnknown) {
		t.Fatalf("a stamped row must not render as unknown: %q", line)
	}
}

func TestAnEventLineFromBeforeRevisionsSaysUnknown(t *testing.T) {
	line := eventLine(ledger.Event{
		Seq: 1, Kind: ledger.KindNoteRecorded, Actor: "pm",
		Hash: "aaaabbbbccccddddeeee",
	})
	if !strings.Contains(line, ledger.RevisionUnknown) {
		t.Fatalf("a row nobody stamped must say so rather than print blank: %q", line)
	}
}
