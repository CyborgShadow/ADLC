package spend

import (
	"testing"

	"github.com/CyborgShadow/adlc/internal/config"
	"github.com/CyborgShadow/adlc/internal/ledger"
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
