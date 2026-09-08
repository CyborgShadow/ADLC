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

// PriceSource says where the numbers behind a cost figure came from.
//
// Three values, not two, for the same reason the gate verdicts are three-valued:
// "priced from the table an operator checked" and "priced from a default nobody
// has looked at" are different degrees of trust, and a report that renders them
// identically invites a figure to be believed further than it has earned.
type PriceSource string

const (
	// PriceUnpriced: no entry for this model in the config table or the
	// defaults. Its cost is unknown, which is not the same as zero.
	PriceUnpriced PriceSource = "unpriced"
	// PriceConfigured: the operator's own table priced it.
	PriceConfigured PriceSource = "configured"
	// PriceDefaulted: the built-in table priced it, because the config had no
	// entry. Real money, unconfirmed rate.
	PriceDefaulted PriceSource = "default"
)

// PriceFor resolves one model's price table and says where it came from.
//
// The fallback exists because the failure it prevents is silent: a project
// configured before DefaultPricing shipped carries an empty table, every run
// prices as UNPRICED, and a spend cap that is never reached is a cap nobody
// notices is missing. Falling back to the defaults gets a number on the screen.
// Reporting the source is what stops that number from being mistaken for one
// somebody confirmed.
//
// The fallback is per-model, not per-table. An operator who priced two models
// and forgot a third should not have their two figures replaced by defaults,
// and the third should not report as free.
func (b Budget) PriceFor(model string) (map[string]int64, PriceSource) {
	if model == "" {
		model = b.DefaultModel
	}
	if t, ok := b.PriceMicrosPerMTok[model]; ok {
		return t, PriceConfigured
	}
	if t, ok := DefaultPricing()[model]; ok {
		return t, PriceDefaulted
	}
	// A model in neither table stays unpriced. Guessing a rate for a model
	// nobody listed would put an invented number where an absent one belongs.
	return nil, PriceUnpriced
}

// Priced reports whether a model has a price at all, from either table. It is
// separate from looking the price up so that "we do not know what this cost"
// and "this cost nothing" stay different answers everywhere they are asked.
func (b Budget) Priced(model string) bool {
	_, src := b.PriceFor(model)
	return src != PriceUnpriced
}

// UsesDefaultPricing reports whether the operator has supplied any price table
// of their own. It answers the question the Overview has to answer before
// anybody reads a money figure off it: is this arithmetic on rates somebody
// here checked, or on the ones that shipped in the binary?
func (b Budget) UsesDefaultPricing() bool { return len(b.PriceMicrosPerMTok) == 0 }
