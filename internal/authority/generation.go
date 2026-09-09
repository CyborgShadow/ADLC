package authority

import (
	"fmt"
	"regexp"
	"sort"
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
	ReasonNoExecutableAC  Reason = "no_executable_criterion"
	ReasonUnrunnableAC    Reason = "criterion_not_invocable"
	ReasonUnownedArea     Reason = "area_has_no_owner"
	ReasonUnnamedResource Reason = "resource_not_named"
	ReasonScopeInvented   Reason = "scope_not_in_brief"
	ReasonScopeCollision  Reason = "file_scope_collision"
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
	// SegmentScopes is every file path this segment's own items have declared,
	// finished ones included. With the paths the brief itself names, it is the
	// whole of what "the work this deliverable touches" means in shape terms,
	// and it is what an invented scope is measured against.
	//
	// Finished items count. A segment whose first five items all landed still
	// touched what it touched, and dropping them the moment they merge would
	// turn the relevance rule off exactly when the backlog starts growing.
	SegmentScopes []string
	// OpenScopes is the file scope declared by every item that is still open,
	// keyed by item id, so a refusal can name the item already holding a file.
	// Open only: a done item's files serialise nobody.
	OpenScopes map[string][]string
}

// AdmitItem decides whether one proposed work item may be created.
//
// Generation is the one place an agent gets to decide what work exists, which
// makes it the one place an agent can quietly widen its own scope. So the
// admission rules are deliberately about *shape* rather than about taste: an
// id nothing else has taken, an area that routes to somebody who will actually
// drain it, at least one criterion a command can check, a blast radius the
// policy understands, a file scope that lands where this deliverable's work
// lands and that no open item is already holding, and — for anything that
// reaches a machine — the machine named, so the lease can protect it.
//
// None of that judges whether the item is a good idea. That is what the human
// brief and the review queue are for. What it prevents is the failure mode
// that costs the most: an item that looks fine, is filed under an area nobody
// drains, and is therefore never done and never noticed.
//
// The order is identity, routing, blast radius, specification, scope, and it is
// ordered by how early in the lifecycle each defect stops the item. An item
// nobody can pick up, or whose machine the lease cannot protect, never reaches
// verification at all — so answering it first with a complaint about its
// acceptance criteria sends the run to fix the thing that was not blocking it.
// A proposal with several defects is answered with the earliest of them, once,
// and comes back for the rest.
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

	// At least one criterion the control plane can settle for itself.
	//
	// Measured over one segment: 221 acceptance criteria, 11 of them executable.
	// The other 210 were prose, and prose is settled the only way prose can be —
	// by dispatching a judge, handing it the sentence, and recording what it
	// said it saw. Ten to fifteen minutes of agent time per item to read text
	// that was already written down, and the answer comes back as a claim where
	// every other verification in this system is an observation.
	//
	// Prose goes stale, too, and silently. Two real ones: a criterion whose
	// command named a role that had never existed, and another requiring a
	// status the trunk had since renamed. Nothing executes a sentence, so
	// nothing notices when it stops being satisfiable, and the item sits there
	// specified against a world that has moved.
	//
	// One executable criterion is the floor, not the ceiling: prose alongside it
	// stays admissible, because criterion.go is right that some things are not
	// mechanically checkable and pretending otherwise would be worse.
	parsed := ParseCriteria(p.Criteria)
	executable := 0
	for i, c := range parsed {
		if fault := criterionFault(c.Text); fault != "" {
			return ItemDecision{Reason: ReasonUnrunnableAC, Detail: fmt.Sprintf(
				"%s criterion %d %s. A criterion that FAILS is expected at this point — the work does not exist yet, so it exits non-zero and says so, and that is the whole value of filing it. One that cannot be started says nothing about the work now or ever, and its silence is indistinguishable from work not done. The criterion reads: %q",
				id, i+1, fault, c.Text)}
		}
		if c.Executable() {
			executable++
		}
	}
	if executable == 0 {
		return ItemDecision{Reason: ReasonNoExecutableAC, Detail: fmt.Sprintf(
			"%s declares %d acceptance criteria and not one of them carries a check the control plane can run, so every one of them costs a dispatched judge and comes back as a claim rather than an observation. Write at least one in the form `AC-1 [exit_zero] go test ./internal/foo/...`; the rules are %s. Prose criteria remain admissible alongside it, for what genuinely needs somebody's judgement",
			id, len(parsed), ruleList())}
	}

	if d := inTheBrief(id, p, f); d.Reason != "" {
		return d
	}
	if d := scopeIsFree(id, p, f); d.Reason != "" {
		return d
	}
	return ItemDecision{Admitted: true}
}

// inTheBrief refuses an item whose declared file scope lies entirely outside
// anything this deliverable touches.
//
// The failure it prevents was measured: a reviewed five-item plan became
// forty-four items, thirty-nine of which arrived after the last thing anybody
// had looked at, and 77% of the segment's spend went to the fleet working on
// itself rather than on the deliverable it had been given. Nothing was wrong
// with any individual item. What was wrong is that no rule anywhere asked
// whether the item was what the user asked for, so generation — the one place
// an agent decides what work EXISTS — had no floor under it at all.
//
// The signal is the file scope, and it is chosen because it is the one part of
// a proposal that cannot be written in the language of the brief without
// actually being about the brief. A title can echo any words you like; a path
// either lands where this deliverable's work lands or it does not. An item for
// a cats page under site/cats and an item rewriting internal/dispatch are
// distinguishable at a glance by their paths and by almost nothing else in the
// envelope.
//
// It judges shape, not merit. It cannot tell a good item from a bad one and
// does not try — that is the brief, the review queue and the plan gate. It
// answers one question: is this the same body of work.
//
// Two limits, stated rather than hidden. An item that declares NO file scope is
// not judged here, because there is no shape to judge; and the FIRST item of a
// segment whose brief names no path has nothing to be measured against, so it
// sets the root that the rest are then held to. Both are holes an agent could
// walk through, and both are narrower than the hole of having no rule at all.
func inTheBrief(id string, p envelope.ProposedItem, f GenerationFacts) ItemDecision {
	scope := cleanPaths(p.FileScope)
	if len(scope) == 0 {
		return ItemDecision{}
	}
	roots := knownRoots(f)
	if len(roots) == 0 {
		return ItemDecision{}
	}
	for _, path := range scope {
		if underAny(path, roots) {
			return ItemDecision{}
		}
	}
	return ItemDecision{Reason: ReasonScopeInvented, Detail: fmt.Sprintf(
		"%s declares file scope %s, and not one of those paths is under anything %s touches (%s). An item is admitted for the work the brief asked for; work on something else is a different deliverable, and filing it here is how a reviewed five-item plan became forty-four with nobody having agreed to thirty-nine of them. Either declare a path under one of those roots, or leave it out of this plan entirely and say in the run's notes that it wants a deliverable of its own — creating that is a person's decision, not a planner's",
		id, strings.Join(scope, ", "), segmentName(f), strings.Join(roots, ", "))}
}

// scopeIsFree refuses an item that claims a file an open item already claims.
//
// Two items that name one file are not two items; they are one queue with two
// agents in it. S1-004 and S1-011 both declared site/cats/index.html, and the
// second could only start once the first had landed — so the plan's DEPTH set
// the wall clock and its breadth did nothing. That segment ended up eight
// lifecycles deep in a chain of eight items, and running more agents at once
// could not have made it finish any sooner.
//
// agents/planner.md already asks for disjoint scopes. Asking was not enough:
// nothing measured the answer, so nothing noticed when it was wrong.
func scopeIsFree(id string, p envelope.ProposedItem, f GenerationFacts) ItemDecision {
	scope := cleanPaths(p.FileScope)
	if len(scope) == 0 || len(f.OpenScopes) == 0 {
		return ItemDecision{}
	}
	// Sorted so that two runs over one proposal name the same colliding item.
	others := make([]string, 0, len(f.OpenScopes))
	for other := range f.OpenScopes {
		others = append(others, other)
	}
	sort.Strings(others)
	for _, other := range others {
		if other == id {
			continue
		}
		for _, mine := range scope {
			for _, theirs := range cleanPaths(f.OpenScopes[other]) {
				if overlaps(mine, theirs) {
					return ItemDecision{Reason: ReasonScopeCollision, Detail: fmt.Sprintf(
						"%s declares %s, which open item %s already declares as %s. Two builders on one file are serialised however wide the fleet is — the second waits for the first to land, or is rebased onto it and rebuilt — so depth in the plan is wall clock and breadth is not. Either give this item files nothing open claims, or fold it into %s",
						id, mine, other, theirs, other)}
				}
			}
		}
	}
	return ItemDecision{}
}

// knownRoots is where this deliverable's work lives: the paths its brief names,
// plus the roots its own items already declare.
func knownRoots(f GenerationFacts) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if r := pathRoot(p); r != "" && !seen[r] {
			seen[r], out = true, append(out, r)
		}
	}
	for _, tok := range strings.FieldsFunc(f.SegmentBrief, func(r rune) bool {
		// A path in prose ends at whitespace or at the punctuation a sentence
		// puts after it. The slash is what makes it a path at all.
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' ||
			r == ',' || r == ';' || r == '(' || r == ')' || r == '"' || r == '`' || r == '\''
	}) {
		tok = strings.Trim(tok, ".")
		if strings.Contains(tok, "/") {
			add(tok)
		}
	}
	for _, p := range f.SegmentScopes {
		add(p)
	}
	sort.Strings(out)
	return out
}

// pathRoot is the first two path segments, which is the coarsest thing that
// still separates one body of work from another: site/cats and internal/gate
// are different work, while site/cats/index.html and site/cats/style.css are
// the same work. One segment would call every directory in a repo the same
// place; three would call a page and its stylesheet different places.
func pathRoot(p string) string {
	parts := splitPath(p)
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return parts[0] + "/" + parts[1]
	}
}

func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+"/") {
			return true
		}
	}
	return false
}

// overlaps reports whether two declared paths reach the same file. A scope
// entry may name a directory, so a directory containing the other counts: an
// item holding site/cats/ holds site/cats/index.html with it.
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// cleanPaths canonicalises declared paths so that "./site/cats/index.html",
// "site\\cats\\index.html" and "site/cats/index.html" are one path rather than
// three. Two agents naming one file in two spellings is exactly the collision
// this has to see.
func cleanPaths(in []string) []string {
	var out []string
	for _, p := range in {
		if parts := splitPath(p); len(parts) > 0 {
			out = append(out, strings.Join(parts, "/"))
		}
	}
	sort.Strings(out)
	return out
}

func splitPath(p string) []string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	var parts []string
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			continue
		}
		parts = append(parts, seg)
	}
	return parts
}

func segmentName(f GenerationFacts) string {
	if id := strings.TrimSpace(f.SegmentID); id != "" {
		return id
	}
	return "this deliverable"
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
