// Package spend turns recorded token usage into money, and money into a
// refusal.
//
// The lesson is not "add a cost column". It is that an autonomous dispatcher
// with no spend accounting has no throttle, and that accounting nothing acts
// on is decoration. So this package's public surface is a budget CHECK, and
// the cost figure is a by-product of it.
package spend

import (
	"fmt"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Micros is a cost in millionths of a currency unit.
type Micros int64

// String renders micros as money, always with cents, so a small non-zero cost
// never renders as "$0" and gets mistaken for the absent quantity above.
func (m Micros) String() string {
	neg := ""
	v := int64(m)
	if v < 0 {
		neg, v = "-", -v
	}
	return fmt.Sprintf("%s$%d.%02d", neg, v/1_000_000, (v%1_000_000)/10_000)
}

// Cost prices one run's usage.
//
// A model with no price entry costs UNKNOWN, not zero. That distinction is the
// whole point: an unpriced model silently costing zero is how a spend cap
// comes to be never reached.
func Cost(b config.Budget, model string, u ledger.Usage) (Micros, bool) {
	if model == "" {
		model = b.DefaultModel
	}
	table, ok := b.PriceMicrosPerMTok[model]
	if !ok {
		return 0, false
	}
	per := func(class string, tokens int64) int64 {
		return table[class] * tokens / 1_000_000
	}
	total := per("input", u.InputTokens) +
		per("output", u.OutputTokens) +
		per("cache_read", u.CacheReadTokens) +
		per("cache_write", u.CacheWriteTokens)
	return Micros(total), true
}

// Verdict is the answer to "may this run be dispatched, or its result kept".
type Verdict struct {
	OK     bool
	Reason string
	Detail string
}

// CheckDispatch answers whether there is budget left to start a run.
//
// A zero cap means unlimited and reports as unlimited. It must never be read
// as a cap of zero already exhausted: "no budget configured" and "budget
// spent" are opposite facts, and collapsing them stops the fleet on its first
// run.
func CheckDispatch(b config.Budget, spentToday, spentSegment Micros, segmentID string) Verdict {
	if b.PerDayMicros > 0 && int64(spentToday) >= b.PerDayMicros {
		return Verdict{Reason: "budget_exhausted", Detail: fmt.Sprintf(
			"the last 24 hours have cost %s against a daily cap of %s",
			spentToday, Micros(b.PerDayMicros))}
	}
	if b.PerSegmentMicros > 0 && int64(spentSegment) >= b.PerSegmentMicros {
		return Verdict{Reason: "budget_exhausted", Detail: fmt.Sprintf(
			"segment %s has cost %s against a cap of %s",
			segmentID, spentSegment, Micros(b.PerSegmentMicros))}
	}
	return Verdict{OK: true}
}

// CheckRun answers whether one finished run's cost is within the per-run cap.
// It runs after the fact, because a run's cost is not known before it. What it
// buys is a loud record of the overrun rather than a silent one.
func CheckRun(b config.Budget, cost Micros) Verdict {
	if b.PerRunMicros > 0 && int64(cost) > b.PerRunMicros {
		return Verdict{Reason: "budget_exhausted", Detail: fmt.Sprintf(
			"this run cost %s against a per-run cap of %s", cost, Micros(b.PerRunMicros))}
	}
	return Verdict{OK: true}
}

// Report is a spend summary for the fleet report.
type Report struct {
	Today       Micros
	AllTime     Micros
	DayCap      Micros
	Unpriced    int
	UnpricedRun string
}

// Summarise totals recorded spend and counts the runs nobody could price.
//
// The unpriced count is reported rather than folded into the total. A run
// whose model has no price is not a free run; it is a run of unknown cost, and
// a total that quietly includes it as zero is a number that will be trusted
// and should not be.
func Summarise(l *ledger.Ledger, b config.Budget, now time.Time) (Report, error) {
	var r Report
	r.DayCap = Micros(b.PerDayMicros)
	today, err := l.SpendMicros(now.Add(-24*time.Hour), "")
	if err != nil {
		return r, err
	}
	all, err := l.SpendMicros(time.Time{}, "")
	if err != nil {
		return r, err
	}
	r.Today, r.AllTime = Micros(today), Micros(all)

	runs, err := l.Runs("", 0)
	if err != nil {
		return r, err
	}
	for _, run := range runs {
		if !run.Finished() {
			continue
		}
		if run.CostMicros == 0 && (run.Usage.InputTokens > 0 || run.Usage.OutputTokens > 0) {
			r.Unpriced++
			if r.UnpricedRun == "" {
				r.UnpricedRun = run.RunID
			}
		}
	}
	return r, nil
}
