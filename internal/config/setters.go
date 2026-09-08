package config

import "fmt"

// The settings an operator may change from the dashboard.
//
// The line is drawn at what a *policy* decision is, versus what a *structural*
// one is. How far a change may reach before a person clears it, how much the
// fleet may spend, how much authority the console has, how many attempts an
// item gets — those are judgement calls somebody makes and revises, often
// during an incident, and making them require an editor and a restart means
// they get made badly or not at all.
//
// Checks, workers and routing stay file-only, and not for want of effort. A
// check is a command line: a form that writes arbitrary argv into something the
// control plane will execute is a remote shell wearing a hat, and "it is only
// bound to loopback" is the kind of reasoning that ages badly. Workers and
// routing are structure — they belong in a commit somebody reviewed, next to
// the prompt files they name.

// SetConsole changes the console's authority and whether it is on at all.
func (c *Config) SetConsole(enabled bool, authority ConsoleAuthority) error {
	if !authority.Known() {
		return fmt.Errorf("%q is not an authority level this build knows (propose, act, full)", authority)
	}
	c.mu.Lock()
	c.Console.Enabled = enabled
	c.Console.Authority = authority
	c.mu.Unlock()
	if enabled {
		// Re-runs the same rule a load does: enabling a console with nobody to
		// run it as would produce a panel that fails on its first question.
		if err := c.validateConsole(); err != nil {
			return err
		}
	}
	return Save(c)
}

// SetBlast changes the approval policy.
//
// Every value is checked before any is written. A half-applied safety policy is
// worse than either the old one or the new one, because nobody can say which
// rules were in force.
func (c *Config) SetBlast(autoApplyMax, namedApproverMin, twoApprovalsMin Radius, ttlMinutes int) error {
	for name, r := range map[string]Radius{
		"auto_apply_max":     autoApplyMax,
		"named_approver_min": namedApproverMin,
		"two_approvals_min":  twoApprovalsMin,
	} {
		if !r.Known() {
			return fmt.Errorf("%s: %q is not a radius this build knows (none, host, fleet, region, global)", name, r)
		}
	}
	if ttlMinutes <= 0 {
		return fmt.Errorf("an approval time-to-live of %d minutes means an approval expires before anybody can act on it", ttlMinutes)
	}
	if namedApproverMin.Rank() > twoApprovalsMin.Rank() {
		return fmt.Errorf(
			"two approvals are required from %s but one named approver only from %s, which is backwards: the wider change would ask for less",
			twoApprovalsMin, namedApproverMin)
	}
	c.mu.Lock()
	c.Blast.AutoApplyMax = autoApplyMax
	c.Blast.NamedApproverMin = namedApproverMin
	c.Blast.TwoApprovalsMin = twoApprovalsMin
	c.Blast.ApprovalTTLMinutes = ttlMinutes
	c.mu.Unlock()
	return Save(c)
}

// SetBudget changes the spend caps.
//
// Zero means unlimited and is reported as unlimited everywhere, never as a cap
// of nothing — "no budget set" and "budget exhausted" have to stay different
// answers or a fleet stops for a reason nobody can find.
func (c *Config) SetBudget(perRun, perDay, perSegment int64, defaultModel string) error {
	for name, v := range map[string]int64{
		"per_run": perRun, "per_day": perDay, "per_segment": perSegment,
	} {
		if v < 0 {
			return fmt.Errorf("%s cannot be negative; use 0 for unlimited", name)
		}
	}
	c.mu.Lock()
	c.Budget.PerRunMicros = perRun
	c.Budget.PerDayMicros = perDay
	c.Budget.PerSegmentMicros = perSegment
	if defaultModel != "" {
		c.Budget.DefaultModel = defaultModel
	}
	c.mu.Unlock()
	return Save(c)
}

// SetDispatch changes the rework budget and the per-run timeout.
func (c *Config) SetDispatch(maxAttempts, timeoutSeconds, maxConcurrent int) error {
	if maxConcurrent < 1 {
		return fmt.Errorf("at least one agent has to be able to run; %d would stop the fleet entirely", maxConcurrent)
	}
	if maxConcurrent > 32 {
		return fmt.Errorf(
			"%d concurrent agents is past the point where this is a throughput setting and into where it is a way to spend a month's budget in an afternoon; 32 is the ceiling this build accepts", maxConcurrent)
	}
	if maxAttempts < 1 {
		return fmt.Errorf("an item needs at least one attempt; %d would mean nothing is ever built", maxAttempts)
	}
	if timeoutSeconds < 60 {
		return fmt.Errorf(
			"a %ds timeout kills most agents mid-run, and a run killed halfway is recorded as UNKNOWN rather than failed — which is accurate and useless. 60s is the floor", timeoutSeconds)
	}
	c.mu.Lock()
	c.Dispatch.MaxAttempts = maxAttempts
	c.Dispatch.TimeoutSeconds = timeoutSeconds
	c.Dispatch.MaxConcurrent = maxConcurrent
	c.mu.Unlock()
	return Save(c)
}

// SetServer changes the dashboard's own refresh rate.
func (c *Config) SetServer(refreshSeconds int) error {
	if refreshSeconds < 0 {
		return fmt.Errorf("a refresh interval cannot be negative; use 0 to stop refreshing")
	}
	c.mu.Lock()
	c.Server.RefreshSeconds = refreshSeconds
	c.mu.Unlock()
	return Save(c)
}

// SetPrice sets one model's rate, in whole dollars per million tokens for input
// and output. Cache rates follow the published shape rather than being asked
// for separately: a form with four numbers per model is one nobody fills in.
func (c *Config) SetPrice(model string, inPerMTok, outPerMTok float64) error {
	if model == "" {
		return fmt.Errorf("which model?")
	}
	if inPerMTok < 0 || outPerMTok < 0 {
		return fmt.Errorf("a price cannot be negative")
	}
	c.mu.Lock()
	if c.Budget.PriceMicrosPerMTok == nil {
		c.Budget.PriceMicrosPerMTok = map[string]map[string]int64{}
	}
	c.Budget.PriceMicrosPerMTok[model] = price(int64(inPerMTok*1e6), int64(outPerMTok*1e6))
	c.mu.Unlock()
	return Save(c)
}
