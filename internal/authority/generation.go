package authority

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/envelope"
)

// Refusal reasons specific to work generation.
const (
	ReasonNoSuchSegment   Reason = "no_such_segment"
	ReasonDuplicateItem   Reason = "duplicate_item_id"
	ReasonMalformedItemID Reason = "malformed_item_id"
	ReasonNoCriteria      Reason = "no_acceptance_criteria"
	ReasonUnownedArea     Reason = "area_has_no_owner"
	ReasonUnnamedResource Reason = "resource_not_named"
	ReasonScopeInvented   Reason = "scope_not_in_brief"
)

var itemIDRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]{3,}$`)

// ItemDecision is the answer to one proposed work item.
type ItemDecision struct {
	Admitted bool
	Reason   Reason
	Detail   string
}

// GenerationFacts are what admitting a proposed item needs beyond the config.
type GenerationFacts struct {
	SegmentID string
	// SegmentBrief is the direction the planner was given. It is carried here
	// so that a refusal can say what the item was supposed to be about.
	SegmentBrief string
	// ExistingIDs are every item id already in the ledger, plus the ids admitted
	// earlier in this same run.
	ExistingIDs map[string]bool
}

// AdmitItem decides whether one proposed work item may be created.
//
// Generation is the one place an agent gets to decide what work exists, which
// makes it the one place an agent can quietly widen its own scope. So the
// admission rules are deliberately about *shape* rather than about taste: an
// id nothing else has taken, an area that routes to somebody who will actually
// drain it, at least one criterion a command can check, a blast radius the
// policy understands, and — for anything that reaches a machine — the
// machine named, so the lease can protect it.
//
// None of that judges whether the item is a good idea. That is what the human
// brief and the review queue are for. What it prevents is the failure mode
// that costs the most: an item that looks fine, is filed under an area nobody
// drains, and is therefore never done and never noticed.
func AdmitItem(cfg *config.Config, p envelope.ProposedItem, f GenerationFacts) ItemDecision {
	id := strings.TrimSpace(p.ID)
	if id == "" || !itemIDRe.MatchString(id) {
		return ItemDecision{Reason: ReasonMalformedItemID, Detail: fmt.Sprintf(
			"%q is not a usable work item id (want something like %s-001). The id is also the lease key, so it has to be stable and mechanically comparable",
			p.ID, f.SegmentID)}
	}
	if f.ExistingIDs[id] {
		return ItemDecision{Reason: ReasonDuplicateItem, Detail: fmt.Sprintf(
			"%s already exists; a second item under one id gives two runs one lease key and the collision check goes silent", id)}
	}
	if strings.TrimSpace(p.Title) == "" {
		return ItemDecision{Reason: ReasonMalformedItemID, Detail: id + " has no title"}
	}

	var checkable int
	for _, c := range p.Criteria {
		if len(strings.TrimSpace(c)) >= 12 {
			checkable++
		}
	}
	if checkable == 0 {
		return ItemDecision{Reason: ReasonNoCriteria, Detail: fmt.Sprintf(
			"%s declares no usable acceptance criteria. An item nobody can verify is an item nobody can finish, and it will sit in the backlog looking like work", id)}
	}

	if strings.TrimSpace(p.Area) == "" {
		return ItemDecision{Reason: ReasonUnownedArea, Detail: fmt.Sprintf(
			"%s has no area. Areas are how work reaches the right worker; an untagged item is routed by accident", id)}
	}
	if !cfg.KnownArea(p.Area) {
		return ItemDecision{Reason: ReasonUnownedArea, Detail: fmt.Sprintf(
			"%s is filed under area %q, which this project has not declared. Generation is the one place an agent decides what work EXISTS, so it may only file under a taxonomy somebody already owns — inventing an area is how a backlog fills with work nobody drains",
			id, p.Area)}
	}
	if _, ok := cfg.OwnerFor(p.Area, config.CapImplement); !ok {
		return ItemDecision{Reason: ReasonUnownedArea, Detail: fmt.Sprintf(
			"%s is filed under area %q and no declared worker can implement it", id, p.Area)}
	}

	radius := config.Radius(p.Radius)
	if p.Radius == "" {
		radius = config.RadiusNone
	}
	if !radius.Known() {
		return ItemDecision{Reason: ReasonRadiusUndeclared, Detail: fmt.Sprintf(
			"%s declares blast radius %q, which is not one of %v", id, p.Radius, config.Radii())}
	}
	if radius != config.RadiusNone && len(p.Resources) == 0 {
		return ItemDecision{Reason: ReasonUnnamedResource, Detail: fmt.Sprintf(
			"%s reaches %q but names no resource. The lease cannot protect a machine nobody named, and two runs changing one host is the failure this exists to prevent",
			id, radius)}
	}
	if radius.Rank() > cfg.Blast.AutoApplyMax.Rank() && cfg.Blast.AutoApplyMax == config.RadiusNone {
		// Not a refusal: an item above the auto-apply threshold is legitimate, it
		// simply stops for an approval later.
		_ = radius
	}
	return ItemDecision{Admitted: true}
}

// SegmentNeedsWork reports how many more items a segment's brief calls for.
//
// Generation is demand-driven rather than continuous. A planner that runs on
// a timer regardless of backlog depth invents work to justify its own cadence,
// and the backlog stops being a statement of what is left to do.
func SegmentNeedsWork(targetOpen, openItems int) int {
	if targetOpen <= 0 {
		return 0
	}
	if openItems >= targetOpen {
		return 0
	}
	return targetOpen - openItems
}
