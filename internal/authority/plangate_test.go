package authority

import (
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// An unreviewed breakdown holds work that reaches something real, and advises
// on work that reaches a file.
//
// One contested plan for a static page kept twelve items unstartable for hours
// across four rejections, while the review went on running and finding nothing
// anybody acted on. Blocking is right where it is right and pure cost where it
// is not.
func TestThePlanGateHoldsByBlastRadius(t *testing.T) {
	p := config.BlastPolicy{PlanGateMin: config.RadiusHost}

	if PlanGateHolds(string(config.RadiusNone), p) {
		t.Error("source-only work was held by an unreviewed plan; it reaches nothing")
	}
	// The firing case, and the one that matters: everything at or above the
	// threshold is still held.
	for _, r := range []config.Radius{config.RadiusHost, config.RadiusFleet,
		config.RadiusRegion, config.RadiusGlobal} {
		if !PlanGateHolds(string(r), p) {
			t.Errorf("%s work started on a breakdown nobody had reviewed", r)
		}
	}
}

// An unrecognised radius holds, on either side of the comparison. Radius
// comparison fails closed everywhere in this package, and a typo must not be
// the thing that lets unreviewed work reach something.
func TestThePlanGateFailsClosed(t *testing.T) {
	if !PlanGateHolds("typo", config.BlastPolicy{PlanGateMin: config.RadiusHost}) {
		t.Error("an unrecognised item radius was let through an unreviewed plan")
	}
	if !PlanGateHolds(string(config.RadiusNone), config.BlastPolicy{PlanGateMin: "typo"}) {
		t.Error("an unrecognised threshold let work through; a typo must not disable the gate")
	}
	if !PlanGateHolds(string(config.RadiusNone), config.BlastPolicy{}) {
		t.Error("an unset threshold let work through rather than holding")
	}
}
