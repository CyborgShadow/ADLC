package spend

import (
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// TestAnUnmeasuredRunReachesASurfaceAsUnknown pins the step between what
// CostOf returns and what a person reads, which nothing in this package
// asserted.
//
// CostOf deliberately returns micros of zero alongside a false, so a caller can
// still record something in the ledger's cost column without a sentinel being
// read back as money. That is a sound choice and it has a cost: the value on
// its own is now indistinguishable from a measured zero, and every surface is
// one dropped `known` away from printing $0.00 for a run nobody counted —
// which is the exact defect this item exists to remove, reintroduced at the
// render step rather than the pricing one.
//
// So the rule a surface must follow is stated here as an executable one: the
// figure shown is Unknown whenever the cost is not known, and the money
// otherwise. All three record shapes the criteria name go through it, because
// asserting only the unmeasured case passes just as well for a surface that
// prints UNKNOWN for everything.
func TestAnUnmeasuredRunReachesASurfaceAsUnknown(t *testing.T) {
	// shown is what any surface holding CostOf's pair must render. A caller that
	// keeps the micros and drops the flag is the regression.
	shown := func(c Micros, known bool) string {
		if !known {
			return Unknown.String()
		}
		return c.String()
	}

	for _, tc := range []struct {
		name     string
		usage    ledger.Usage
		measured bool
		want     string
	}{
		// An envelope with no usage block: a priced model, and still nothing to
		// price. Not $0.00, which is what an unwired cost column prints.
		{"absent usage block", ledger.Usage{}, false, "UNKNOWN"},
		// Somebody counted and the count was nothing. A real zero, and it must
		// survive as one — otherwise the guard flags every run and stops meaning
		// anything.
		{"counted, and the count was zero", ledger.Usage{}, true, "$0.00"},
		// The ordinary case, so a surface that renders UNKNOWN for everything
		// fails here rather than passing on the strength of the case above.
		{"real tokens", ledger.Usage{InputTokens: 1_000_000}, true, "$3.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, known := CostOf(budget(), "model-a", tc.usage, tc.measured)
			if got := shown(c, known); got != tc.want {
				t.Errorf("a surface shows %s for %s; want %s", got, tc.name, tc.want)
			}
		})
	}

	// And the flag is reachable from the record alone: a caller holding only the
	// counters gets the same answer as one that watched the block arrive, so a
	// surface reading the ledger is not obliged to guess.
	c, known := CostOf(budget(), "model-a", ledger.Usage{}, ledger.Usage{}.Measured())
	if shown(c, known) != "UNKNOWN" {
		t.Errorf("a run read back from the record must reach a surface as UNKNOWN, got %s", shown(c, known))
	}

	// An unpriced model is the other unknown, and must not be quietly rendered
	// as money either — the two arrive by different routes and both end here.
	if c, priced := Cost(config.Budget{DefaultModel: "model-a"}, "model-nobody-priced",
		ledger.Usage{InputTokens: 1_000_000}); shown(c, priced) != "UNKNOWN" {
		t.Errorf("a model nobody priced must reach a surface as UNKNOWN, got %s", shown(c, priced))
	}
}
