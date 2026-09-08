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
