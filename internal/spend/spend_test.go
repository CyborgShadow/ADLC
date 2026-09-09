package spend

import (
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

func budget() config.Budget {
	return config.Budget{
		DefaultModel: "model-a",
		PriceMicrosPerMTok: map[string]map[string]int64{
			"model-a": {"input": 3_000_000, "output": 15_000_000, "cache_read": 300_000, "cache_write": 3_750_000},
		},
	}
}

func TestCostIsComputedFromRecordedUsage(t *testing.T) {
	c, ok := Cost(budget(), "model-a", ledger.Usage{InputTokens: 1_000_000, OutputTokens: 100_000})
	if !ok {
		t.Fatal("a priced model should price")
	}
	// 1M input at $3/Mtok + 100k output at $15/Mtok = 3.00 + 1.50
	if c.String() != "$4.50" {
		t.Fatalf("want $4.50, got %s", c)
	}
}

// TestAnUnmeasuredRunCostsUnknownNotNothing is the whole item. An envelope
// that omits its `usage` block parses to four zeros, and pricing those at the
// going rate produces a confident $0.00 for a run that may have cost anything.
//
// The firing case and the clean case differ only in whether anybody counted:
// the same four zeros price as UNKNOWN when the counters are all the evidence
// there is, and as a real zero when the caller can vouch for the measurement.
// Both go through CostOf, because that is where measurement is answered — see
// TestTheCostFlagIsAboutThePriceTableOnly for why it is not answered in Cost.
func TestAnUnmeasuredRunCostsUnknownNotNothing(t *testing.T) {
	// Absent usage block: a priced model, and still nothing to price. The
	// counters are all the evidence there is, so Measured() is the whole answer.
	absent := ledger.Usage{}
	c, known := CostOf(budget(), "model-a", absent, absent.Measured())
	if known {
		t.Fatalf("a run nobody measured must not price as %s", c)
	}
	if c != 0 {
		t.Errorf("the flag carries the meaning; the value stays zero so callers can record it, got %s", c)
	}

	// Explicit zero: somebody counted, and the count was nothing.
	c, known = CostOf(budget(), "model-a", ledger.Usage{}, true)
	if !known {
		t.Fatal("a measured zero is a known cost, not an absent one")
	}
	if c != 0 {
		t.Errorf("a measured zero costs zero, got %s", c)
	}

	// And the record is what the two are told apart by.
	if absent.Measured() {
		t.Error("an all-zero usage block is the shape of one nobody filled in")
	}
	if !(ledger.Usage{CacheReadTokens: 1}).Measured() {
		t.Error("a non-zero counter is evidence somebody counted, whichever counter it is")
	}
}

// TestTheCostFlagIsAboutThePriceTableOnly pins the meaning two callers outside
// this package already read off Cost's second value. internal/dispatch and
// cmd/adlc turn a false into "this model has no price entry", so an earlier
// attempt at this item, which also returned false for a run nobody measured,
// made the shipped binary print `model "claude-opus-5" has no price entry` for
// an envelope with no usage block — about a model priced by the defaults. That
// sends an operator to add a price already there and buries the fact that held.
//
// Both directions, because a flag quietly given a second meaning is caught only
// by asserting what it does NOT mean as well as what it does.
func TestTheCostFlagIsAboutThePriceTableOnly(t *testing.T) {
	if c, priced := Cost(budget(), "model-a", ledger.Usage{}); !priced {
		t.Errorf("a priced model is priced whether or not anybody counted its tokens, got %s unpriced; "+
			"a false here reaches an operator as \"model-a has no price entry\", which is untrue", c)
	}
	if _, priced := Cost(budget(), "model-nobody-priced", ledger.Usage{InputTokens: 1}); priced {
		t.Error("a model in neither the operator's table nor the defaults must still report unpriced")
	}
}

// TestUnknownDoesNotRenderAsMoney pins the reason Unknown is a distinct value:
// a surface that prints it must not print a price.
func TestUnknownDoesNotRenderAsMoney(t *testing.T) {
	if s := Unknown.String(); s != "UNKNOWN" {
		t.Errorf("want UNKNOWN, got %s", s)
	}
	if Unknown.Known() {
		t.Error("Unknown is the one Micros that is not a cost")
	}
	if !Micros(0).Known() {
		t.Error("a measured zero is a known cost and must not be swept up with it")
	}
	if s := Micros(0).String(); s != "$0.00" {
		t.Errorf("a real zero still renders as money, got %s", s)
	}
}

// A model silently costing zero is how a spend cap comes to be never reached.
func TestAnUnpricedModelCostsUnknownNotZero(t *testing.T) {
	c, ok := Cost(budget(), "model-nobody-priced", ledger.Usage{InputTokens: 5_000_000})
	if ok {
		t.Fatal("an unpriced model must not report as priced")
	}
	if c != 0 {
		t.Fatalf("the caller decides what to do; the value is zero and the flag is what carries the meaning, got %s", c)
	}
}

// TestNoCapMeansUnlimitedNotExhausted separates two opposite facts that a
// single integer collapses. Reading "no budget configured" as "budget spent"
// stops the fleet on its first run.
func TestNoCapMeansUnlimitedNotExhausted(t *testing.T) {
	b := budget() // all caps zero
	if v := CheckDispatch(b, 999_000_000, 0, "S1"); !v.OK {
		t.Fatalf("an unset cap must not refuse: %s", v.Detail)
	}
	b.PerDayMicros = 10_000_000 // $10
	if v := CheckDispatch(b, 10_000_000, 0, "S1"); v.OK {
		t.Fatal("a reached cap must refuse")
	}
	if v := CheckDispatch(b, 9_000_000, 0, "S1"); !v.OK {
		t.Fatalf("under the cap should be fine: %s", v.Detail)
	}
}

func TestASegmentCapIsSeparateFromTheDailyOne(t *testing.T) {
	b := budget()
	b.PerSegmentMicros = 5_000_000
	v := CheckDispatch(b, 0, 6_000_000, "S9")
	if v.OK {
		t.Fatal("a segment over its cap must refuse even when the day is clear")
	}
	if v.Reason != "budget_exhausted" {
		t.Errorf("want budget_exhausted, got %q", v.Reason)
	}
}

func TestSmallCostsDoNotRenderAsZero(t *testing.T) {
	// The reason this matters: $0.00 is exactly what an unwired cost column
	// prints, so a real small number must be distinguishable from an absent one.
	if s := Micros(1_500_000).String(); s != "$1.50" {
		t.Errorf("want $1.50, got %s", s)
	}
	if s := Micros(9_000).String(); s != "$0.00" {
		t.Errorf("want $0.00 for sub-cent, got %s", s)
	}
	if s := Micros(120_000).String(); s != "$0.12" {
		t.Errorf("want $0.12, got %s", s)
	}
}

// TestAnUnknownSpendIsNotAnAffordableOne is the cap half of the same lesson.
// Both checks take a Micros, so a run nobody measured reaches them as Unknown
// and must not clear the cap on the strength of being zero — that would admit
// precisely the runs whose cost nobody can bound.
func TestAnUnknownSpendIsNotAnAffordableOne(t *testing.T) {
	b := budget()
	b.PerDayMicros, b.PerSegmentMicros, b.PerRunMicros = 10_000_000, 10_000_000, 10_000_000

	if v := CheckDispatch(b, Unknown, 0, "S1"); v.OK {
		t.Error("a daily total that cannot be stated must not read as room to spend")
	} else if v.Reason != "spend_unknown" {
		t.Errorf("want spend_unknown, got %q", v.Reason)
	}
	if v := CheckDispatch(b, 0, Unknown, "S1"); v.OK {
		t.Error("a segment total that cannot be stated must not read as room to spend either")
	}
	if v := CheckRun(b, Unknown); v.OK {
		t.Error("a run whose cost is unknown has not been shown to be within the cap")
	} else if v.Reason != "spend_unknown" {
		t.Errorf("want spend_unknown, got %q", v.Reason)
	}

	// The clean case: a run with real token counts, under the cap, is still
	// admitted. Without it this guard passes vacuously the day it starts
	// refusing everything, and a fleet that refuses every dispatch is a worse
	// failure than the one being fixed.
	cost, known := Cost(b, "model-a", ledger.Usage{InputTokens: 1_000_000}) // $3.00
	if !known {
		t.Fatal("a priced model with real counts must price")
	}
	if v := CheckRun(b, cost); !v.OK {
		t.Errorf("a measured run under the per-run cap must be admitted: %s", v.Detail)
	}
	if v := CheckDispatch(b, cost, cost, "S1"); !v.OK {
		t.Errorf("measured spend under both caps must be admitted: %s", v.Detail)
	}
}

func TestAPerRunCapReportsAnOverrun(t *testing.T) {
	b := budget()
	b.PerRunMicros = 1_000_000
	if v := CheckRun(b, Micros(2_000_000)); v.OK {
		t.Fatal("a run over its cap should be reported")
	}
	if v := CheckRun(b, Micros(500_000)); !v.OK {
		t.Fatal("a run under its cap should not be")
	}
}

// The fallback exists because an operator whose config predates DefaultPricing
// carries an empty table, and every run then reports UNPRICED. A cap that is
// never reached is a cap nobody notices is missing.
func TestAModelAbsentFromConfigIsPricedFromTheDefaults(t *testing.T) {
	b := config.Budget{} // the shape of a config written before pricing shipped
	c, src := CostFrom(b, "claude-sonnet-4-5", ledger.Usage{InputTokens: 1_000_000, OutputTokens: 100_000})
	if src != config.PriceDefaulted {
		t.Fatalf("want the default table to price it, got %q", src)
	}
	// $3/Mtok in, $15/Mtok out.
	if c.String() != "$4.50" {
		t.Fatalf("want $4.50 from the default rates, got %s", c)
	}
}

// The operator's own table wins. Someone who priced a model against their real
// invoice must not have that figure quietly replaced by a list price.
func TestTheConfigTableBeatsTheDefaultsForTheSameModel(t *testing.T) {
	b := config.Budget{PriceMicrosPerMTok: map[string]map[string]int64{
		"claude-sonnet-4-5": {"input": 1_000_000, "output": 1_000_000},
	}}
	c, src := CostFrom(b, "claude-sonnet-4-5", ledger.Usage{InputTokens: 1_000_000})
	if src != config.PriceConfigured {
		t.Fatalf("want the configured table, got %q", src)
	}
	if c.String() != "$1.00" {
		t.Fatalf("want the operator's own rate, got %s", c)
	}
}

// The fallback is per model, not per table: an operator who priced two models
// and forgot a third should keep their two figures and not get a free third.
func TestTheFallbackIsPerModelNotPerTable(t *testing.T) {
	b := budget() // prices model-a only
	if _, src := CostFrom(b, "model-a", ledger.Usage{}); src != config.PriceConfigured {
		t.Errorf("model-a is in the config table, got %q", src)
	}
	if _, src := CostFrom(b, "claude-opus-5", ledger.Usage{}); src != config.PriceDefaulted {
		t.Errorf("claude-opus-5 is only in the defaults, got %q", src)
	}
}

// The invariant the fallback must not break: unknown cost is not zero cost.
func TestAModelInNeitherTableIsStillUnpricedNotFree(t *testing.T) {
	b := config.Budget{}
	c, src := CostFrom(b, "some-other-vendors-model", ledger.Usage{InputTokens: 5_000_000})
	if src != config.PriceUnpriced {
		t.Fatalf("a model nobody has priced must stay unpriced, got %q", src)
	}
	if c != 0 {
		t.Fatalf("the source carries the meaning; the value is zero, got %s", c)
	}
	if ok := b.Priced("some-other-vendors-model"); ok {
		t.Error("Priced must agree with CostFrom")
	}
	if _, ok := Cost(b, "some-other-vendors-model", ledger.Usage{InputTokens: 5_000_000}); ok {
		t.Error("Cost must report an unpriced model as unpriced")
	}
}

// A config with no table at all is what the Overview has to warn about, and it
// is distinct from a config whose table simply lacks one model.
func TestAnEmptyTableIsReportedAsRunningOnDefaults(t *testing.T) {
	if !(config.Budget{}).UsesDefaultPricing() {
		t.Error("an empty price table means the figures rest on defaults")
	}
	if budget().UsesDefaultPricing() {
		t.Error("an operator with their own table is not running on defaults")
	}
}
