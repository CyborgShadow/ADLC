package server

import (
	"strings"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Cross-references.
//
// Nearly every row in this dashboard names something else — a run, an item, a
// deliverable, a lane, a prompt — and a name you cannot follow is a name you
// have to go and look up somewhere else, which in practice means nobody does.
// The record is a graph; it should be navigable as one.
//
// One resolver rather than a link expression inlined into each template: the
// set of things that have a page is a fact about the server, and having it in
// one place is what stops a new page shipping with half the references to it
// still rendering as plain text.

// linkFor returns the path that explains an identifier, or "" when nothing
// does. The kind disambiguates: a subject is only meaningful next to the event
// kind that produced it.
func linkFor(kind ledger.Kind, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	switch kind {
	case ledger.KindItemCreated, ledger.KindItemProposed, ledger.KindItemTransitioned,
		ledger.KindItemAmended, ledger.KindTransitionAdmitted, ledger.KindTransitionRefused:
		return "/item/" + id
	case ledger.KindSegmentCreated, ledger.KindSegmentAdvanced:
		return "/segment/" + id
	case ledger.KindRunStarted, ledger.KindRunFinished, ledger.KindRunActorCorrected,
		ledger.KindGateObserved:
		return "/run/" + id
	case ledger.KindPromptPinned:
		return "/roles/" + id
	case ledger.KindQuestionRaised, ledger.KindQuestionAnswered:
		return "/questions"
	case ledger.KindApprovalRequested, ledger.KindApprovalDecided:
		return "/approvals"
	case ledger.KindLoopTicked:
		return "/config"
	case ledger.KindWorkerRegistered:
		return "/roles/" + id
	case ledger.KindConsoleAsked, ledger.KindConsoleReplied, ledger.KindConsoleActed:
		return "/console"
	}
	return ""
}

// fieldLinks maps a payload field name to the page that explains its value.
//
// These are the references that appear *inside* a payload rather than as its
// subject — the item a run worked on, the run that raised a question, the
// deliverable an item belongs to.
var fieldLinks = map[string]string{
	"item_id":      "/item/",
	"work_item_id": "/item/",
	"segment_id":   "/segment/",
	"run_id":       "/run/",
	"raised_by":    "/run/",
	"prompt_id":    "/roles/",
	"worker_type":  "/roles/",
	"worker":       "/roles/",
}

// linkForField returns the path a payload field's value points at.
func linkForField(key, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "—" || value == "none" {
		return ""
	}
	prefix, ok := fieldLinks[key]
	if !ok {
		return ""
	}
	// A value with a space in it is prose that happens to sit under a linkable
	// key, not an identifier. Linking it would produce a dead page.
	if strings.ContainsAny(value, " \t\n/") {
		return ""
	}
	return prefix + value
}
