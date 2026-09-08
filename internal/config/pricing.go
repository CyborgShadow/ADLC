package config

// Default pricing, so a fleet reports what it is spending without an operator
// having to fill in a table before the first run.
//
// The alternative was worse in both directions. With no table at all, every run
// reports UNPRICED, and a number nobody has ever seen is a number nobody
// notices going wrong. With a table that silently defaults unknown models to
// zero, real spend renders as $0.00, which is the same failure the three-valued
// verdicts exist to prevent — an absence reading as a good result.
//
// So: known models are priced, unknown models stay UNPRICED, and the whole
// table is an editable default rather than a fact. **These are list prices as
// of early 2026 and they are the operator's to confirm.** Providers change
// prices, discounts and batch rates exist, and a cost report is only worth
// reading if the numbers behind it were checked once by somebody.

// Token classes a price entry is keyed by.
const (
	PriceInput      = "input"
	PriceOutput     = "output"
	PriceCacheRead  = "cache_read"
	PriceCacheWrite = "cache_write"
)

// price builds one model's entry. Cache reads bill at a tenth of input and
// cache writes at 1.25x, which is the shape of the published rates rather than
// a per-model figure.
func price(inPerMTok, outPerMTok int64) map[string]int64 {
	return map[string]int64{
		PriceInput:      inPerMTok,
		PriceOutput:     outPerMTok,
		PriceCacheRead:  inPerMTok / 10,
		PriceCacheWrite: inPerMTok * 125 / 100,
	}
}

// DefaultPricing is micros per million tokens, by model then token class.
//
// One micro is a millionth of a dollar, so $15 per million input tokens is
// 15_000_000 micros. Integers throughout: money in floats accumulates error
// across thousands of runs, and a spend cap that drifts is a cap nobody trusts.
func DefaultPricing() map[string]map[string]int64 {
	return map[string]map[string]int64{
		// Opus family — the most capable, and the one worth watching the bill on.
		"claude-opus-5":   price(15_000_000, 75_000_000),
		"claude-opus-4-1": price(15_000_000, 75_000_000),
		"claude-opus-4":   price(15_000_000, 75_000_000),
		// Sonnet family.
		"claude-sonnet-5":   price(3_000_000, 15_000_000),
		"claude-sonnet-4-5": price(3_000_000, 15_000_000),
		"claude-sonnet-4":   price(3_000_000, 15_000_000),
		// Haiku.
		"claude-haiku-4-5": price(1_000_000, 5_000_000),
	}
}

// PricingComment is the note that ships beside the table, so the assumption
// travels with the numbers rather than living only here.
const PricingComment = "List prices as of early 2026, in micros per million tokens (one micro = one millionth of a dollar). These are a default, not a fact: confirm them against your provider's current pricing before you rely on a cost report. A model absent from this table reports UNPRICED rather than zero — cost unknown is not cost nothing."

// Priced reports whether a model has an entry. It is separate from looking the
// price up so that "we do not know what this cost" and "this cost nothing" stay
// different answers everywhere they are asked.
func (b Budget) Priced(model string) bool {
	_, ok := b.PriceMicrosPerMTok[model]
	return ok
}
