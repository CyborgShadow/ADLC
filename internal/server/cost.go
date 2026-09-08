package server

// Money on the Overview.
//
// The operator complaint this answers was short: the dashboard knew the token
// counts and showed $0.00. Two separate things were wrong. A project configured
// before the default price table shipped has an empty table, so every run priced
// as UNPRICED and its recorded cost went into the ledger as zero. And the
// Overview only ever showed one number — the last 24 hours — which is too short
// a window to notice a trend and too coarse to say who is spending it.
//
// So these helpers re-derive spend from the usage each run recorded, rather
// than summing the cost column. A run recorded before the fallback existed
// carries a zero it should not, and re-pricing it from the same token counts is
// the only way to get a truthful figure out of an existing ledger. What the
// ledger recorded is still reported alongside, because the caps were enforced
// against that number and a divergence between the two is worth seeing.

import (
	"fmt"
	"sort"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// CostView is everything the Overview needs to say about money, including how
// far the numbers should be trusted.
type CostView struct {
	// Day and Week are re-priced totals over the last 24 hours and 7 days.
	Day  spend.Micros
	Week spend.Micros
	// DayCap is the configured daily cap. Zero means unlimited, and reads as
	// unlimited — never as a cap of zero already exhausted.
	DayCap spend.Micros
	// Recorded is what the ledger stored for the same 24 hours. Restated says
	// the two disagree, which means runs were priced at zero when they ran.
	Recorded spend.Micros
	Restated bool

	// TopRole is the worker type that spent the most over the week, which is
	// the question "where is the money going" actually resolves to.
	TopRole      string
	TopRoleSpend spend.Micros

	// Runs is how many finished runs with recorded usage the figures cover.
	Runs int
	// Defaulted is how many of those were priced from the built-in table
	// because the operator's own table had no entry for the model.
	Defaulted int
	// Unpriced is how many nobody can price at all. Their cost is unknown and
	// is NOT in Day or Week — an unknown folded in as zero is a number that
	// will be trusted and should not be.
	Unpriced      int
	UnpricedModel string

	// OnDefaults means the operator has supplied no price table of their own.
	OnDefaults bool
	// Basis is one sentence naming where the rates came from, to be printed
	// next to the figures rather than filed under a help page.
	Basis string
}

// Capped reports whether a daily cap is configured at all.
func (c CostView) Capped() bool { return c.DayCap > 0 }

// PercentOfCap is the day's spend against the cap, for a bar. It is zero when
// no cap is set, because a bar drawn against an absent cap invents a limit.
func (c CostView) PercentOfCap() int {
	if c.DayCap <= 0 {
		return 0
	}
	p := int(int64(c.Day) * 100 / int64(c.DayCap))
	if p > 100 {
		p = 100
	}
	return p
}

// CostView computes the Overview's money figures.
//
// Errors reading the ledger are folded into an empty view rather than failing
// the page: a dashboard that will not render because it could not total the
// spend is worse than one that renders without the total.
func (s *Server) CostView() CostView {
	v := CostView{DayCap: spend.Micros(s.Cfg.Budget.PerDayMicros)}
	v.OnDefaults = s.Cfg.Budget.UsesDefaultPricing()

	now := s.now()
	dayFrom := now.Add(-24 * time.Hour)
	weekFrom := now.AddDate(0, 0, -7)

	runs, err := s.Led.Runs("", 0)
	if err != nil {
		v.Basis = "the spend figures could not be read from the ledger: " + err.Error()
		return v
	}

	byRole := map[string]spend.Micros{}
	for _, r := range runs {
		if !r.Finished() {
			continue
		}
		u := r.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 &&
			u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 {
			continue
		}
		started := time.UnixMilli(r.StartedMS)
		if started.Before(weekFrom) {
			continue
		}
		v.Runs++

		cost, src := spend.CostFrom(s.Cfg.Budget, r.Model, u)
		switch src {
		case config.PriceUnpriced:
			// Counted, not totalled. A run whose model nobody priced is a run of
			// unknown cost, and adding it in as zero is the failure this whole
			// package exists to prevent.
			v.Unpriced++
			if v.UnpricedModel == "" {
				v.UnpricedModel = orDefault(r.Model, s.Cfg.Budget.DefaultModel)
			}
			continue
		case config.PriceDefaulted:
			v.Defaulted++
		}

		v.Week += cost
		byRole[r.WorkerType] += cost
		if !started.Before(dayFrom) {
			v.Day += cost
		}
	}

	// Ties broken by name so the same ledger always renders the same row, and a
	// figure that moves between refreshes is not read as spend moving.
	roles := make([]string, 0, len(byRole))
	for k := range byRole {
		roles = append(roles, k)
	}
	sort.Strings(roles)
	for _, k := range roles {
		if byRole[k] > v.TopRoleSpend {
			v.TopRole, v.TopRoleSpend = k, byRole[k]
		}
	}

	if rec, err := s.Led.SpendMicros(dayFrom, ""); err == nil {
		v.Recorded = spend.Micros(rec)
		v.Restated = v.Recorded != v.Day
	}
	v.Basis = s.PricingBasis()
	return v
}

// SpendDay is the last 24 hours, priced at whatever rates are in force now.
func (s *Server) SpendDay() spend.Micros { return s.CostView().Day }

// SpendWeek is the last 7 days. It exists because 24 hours is long enough to
// see a number and too short to see it changing.
func (s *Server) SpendWeek() spend.Micros { return s.CostView().Week }

// TopSpendingRole names the worker type that cost the most this week, or an
// explicit "nothing" rather than an empty cell.
func (s *Server) TopSpendingRole() string {
	v := s.CostView()
	if v.TopRole == "" {
		return "nothing has cost anything this week"
	}
	return fmt.Sprintf("%s · %s", v.TopRole, v.TopRoleSpend)
}

// PricingBasis says in one sentence where the rates behind every money figure
// on this page came from.
//
// It is a sentence rather than a flag because the distinction it draws is not
// obvious from a badge: default rates produce real-looking money from list
// prices that no invoice has confirmed, and somebody reading a total needs to
// know that before they act on it.
func (s *Server) PricingBasis() string {
	b := s.Cfg.Budget
	if b.UsesDefaultPricing() {
		return "No prices are configured for this project, so every figure here uses the built-in list prices. " +
			"They are a starting point, not your invoice — set price_micros_per_mtok in the config to the rates you are actually billed."
	}
	// A partial table is the case most likely to mislead: the operator believes
	// they configured pricing, and some models are quietly on list prices.
	return "Figures use this project's own price table, falling back to built-in list prices for any model the table does not name."
}

// PricedModelSources lists every model that has appeared in a run alongside
// where its price came from, so "which of these am I guessing at" is answerable
// on the page rather than by diffing two tables by eye.
func (s *Server) PricedModelSources() []ModelPrice {
	runs, err := s.Led.Runs("", 0)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []ModelPrice
	for _, r := range runs {
		m := orDefault(r.Model, s.Cfg.Budget.DefaultModel)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		_, src := s.Cfg.Budget.PriceFor(m)
		out = append(out, ModelPrice{Model: m, Source: string(src), Known: src != config.PriceUnpriced})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// ModelPrice is one model and the provenance of its rate.
type ModelPrice struct {
	Model  string
	Source string
	Known  bool
}
