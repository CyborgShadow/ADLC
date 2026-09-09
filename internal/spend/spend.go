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
	"math"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Micros is a cost in millionths of a currency unit.
type Micros int64

// Unknown is a cost nobody can state: a run nobody measured, or a figure whose
// parts include one.
//
// It is a distinct value rather than zero because $0.00 is exactly what an
// unwired cost column prints, so the absent quantity and the free one are
// indistinguishable the moment they share a representation. A cap compared
// against a silent zero is a cap that is never reached; a total that quietly
// absorbs one is a number somebody will quote.
const Unknown Micros = math.MinInt64

// Known reports whether this figure is a cost at all rather than the absence
// of one.
func (m Micros) Known() bool { return m != Unknown }

// String renders micros as money, always with cents, so a small non-zero cost
// never renders as "$0" and gets mistaken for the absent quantity above.
func (m Micros) String() string {
	if m == Unknown {
		return "UNKNOWN"
	}
	neg := ""
	v := int64(m)
	if v < 0 {
		neg, v = "-", -v
	}
	return fmt.Sprintf("%s$%d.%02d", neg, v/1_000_000, (v%1_000_000)/10_000)
}

// Cost prices one run's usage.
//
// The cost is UNKNOWN — ok false — two ways, and neither of them is zero. The
// model may have no price entry. Or nobody counted the tokens: an envelope
// that omits its `usage` block parses to four zeros, and pricing those at the
// going rate produces a confident $0.00 for a run that may have cost anything.
// Both are how a spend cap comes to be never reached.
//
// The value returned alongside a false ok is zero rather than Unknown, because
// callers record it in the ledger's cost column and a sentinel written there
// would be read back as money. The flag is what carries the meaning; a caller
// that ignores it turns an unknown cost into a free one.
func Cost(b config.Budget, model string, u ledger.Usage) (Micros, bool) {
	return CostOf(b, model, u, u.Measured())
}

// CostOf prices usage whose measurement the caller can vouch for.
//
// Cost has to infer measurement from the counters, because an omitted usage
// block and a genuine zero arrive as the same four zeros. A caller that
// watched the block itself — a harness reporting a real zero, or a test
// pinning that case — says so here and gets a priced zero rather than an
// UNKNOWN. Passing false is how a caller that knows the figures were never
// filled in refuses to have them priced at all.
func CostOf(b config.Budget, model string, u ledger.Usage, measured bool) (Micros, bool) {
	if !measured {
		return 0, false
	}
	c, src := CostFrom(b, model, u)
	return c, src != config.PriceUnpriced
}

// CostFrom prices one run's usage and says which table did it.
//
// It is the same arithmetic as Cost with the provenance kept. Callers that
// print money to a person want it: a total built partly on rates that shipped
// in the binary is still worth showing, but only if it is labelled, because the
// figure will otherwise be quoted as if somebody had checked the rates.
//
// A model in neither the operator's table nor the defaults returns zero micros
// and PriceUnpriced. The zero is not a price; the source is what carries the
// meaning, and a caller that ignores it turns an unknown cost into a free one.
func CostFrom(b config.Budget, model string, u ledger.Usage) (Micros, config.PriceSource) {
	table, src := b.PriceFor(model)
	if src == config.PriceUnpriced {
		return 0, src
	}
	per := func(class string, tokens int64) int64 {
		return table[class] * tokens / 1_000_000
	}
	total := per(config.PriceInput, u.InputTokens) +
		per(config.PriceOutput, u.OutputTokens) +
		per(config.PriceCacheRead, u.CacheReadTokens) +
		per(config.PriceCacheWrite, u.CacheWriteTokens)
	return Micros(total), src
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
	// A total built partly on runs nobody measured is a lower bound, and a lower
	// bound compared against a cap answers a question nobody asked: it says the
	// part we can see is affordable. UNKNOWN satisfies nothing, so it refuses
	// here rather than passing on the strength of the runs that did report.
	if !spentToday.Known() || !spentSegment.Known() {
		return Verdict{Reason: "spend_unknown", Detail: fmt.Sprintf(
			"spend so far cannot be totalled — runs finished without reporting any token usage, "+
				"so what has been spent against the daily cap of %s is unknown, not zero",
			Micros(b.PerDayMicros))}
	}
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
	// An unmeasured run is not a cheap run. Comparing zero against the cap here
	// would clear precisely the runs whose cost nobody can bound, which is the
	// opposite of what the cap is for.
	if !cost.Known() {
		return Verdict{Reason: "spend_unknown", Detail: fmt.Sprintf(
			"this run reported no token usage, so its cost is unknown rather than zero "+
				"and cannot be shown to be within the per-run cap of %s", Micros(b.PerRunMicros))}
	}
	if b.PerRunMicros > 0 && int64(cost) > b.PerRunMicros {
		return Verdict{Reason: "budget_exhausted", Detail: fmt.Sprintf(
			"this run cost %s against a per-run cap of %s", cost, Micros(b.PerRunMicros))}
	}
	return Verdict{OK: true}
}

// Report is a spend summary for the fleet report.
//
// Today and AllTime are sums of what the record could price, so they are lower
// bounds whenever Unmeasured or Unpriced is non-zero. The counts travel beside
// the figures rather than being folded into them: a surface that prints the
// number without the count prints a total that is not one.
type Report struct {
	Today       Micros
	AllTime     Micros
	DayCap      Micros
	Unpriced    int
	UnpricedRun string
	// Unmeasured is how many finished runs reported no token usage at all, and
	// UnmeasuredToday how many of those started inside the 24-hour window Today
	// covers. Their cost is unknown, and each of them contributes zero to the
	// sums above.
	Unmeasured      int
	UnmeasuredToday int
	UnmeasuredRun   string
}

// Complete reports whether the totals are the whole story. A false here is the
// difference between "the fleet has spent this" and "the fleet has spent at
// least this".
func (r Report) Complete() bool { return r.Unmeasured == 0 && r.Unpriced == 0 }

// Summarise totals recorded spend and counts the runs nobody could price or
// nobody measured.
//
// Both counts are reported rather than folded into the total. A run whose
// model has no price is not a free run, and neither is one that finished
// without reporting any usage; each is a run of unknown cost, and a total that
// quietly includes it as zero is a number that will be trusted and should not
// be.
func Summarise(l *ledger.Ledger, b config.Budget, now time.Time) (Report, error) {
	var r Report
	r.DayCap = Micros(b.PerDayMicros)
	since := now.Add(-24 * time.Hour)
	today, err := l.SpendMicros(since, "")
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
		if !run.Usage.Measured() {
			// Counted separately from unpriced: nobody knows the rate there, and
			// here nobody knows the quantity. The window matches SpendMicros's, so
			// the 24-hour figure is not labelled incomplete over a run from last
			// week that it never included.
			r.Unmeasured++
			if run.StartedMS >= since.UnixMilli() {
				r.UnmeasuredToday++
			}
			if r.UnmeasuredRun == "" {
				r.UnmeasuredRun = run.RunID
			}
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
