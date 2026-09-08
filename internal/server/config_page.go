package server

// The Config page.
//
// The complaint that produced this rewrite was not "show me less". It was that
// the page listed settings nobody could act on, next to labels that only
// restated the field name. So the organising question here is the operator's:
// what can I change, what happens if I do, and what is currently true?
//
// Three consequences run through everything below.
//
//   - Anything editable says what the edit DOES and when it takes effect. A
//     control whose effect you have to guess is a control nobody touches.
//   - Anything not editable still shows its value and its consequence, and says
//     in one place — not once per row — where it is edited instead. A dead
//     control is worse than an honest sentence.
//   - The prose is derived from internal/config wherever a description exists
//     there, so a setting whose meaning changes cannot leave a confident
//     sentence behind on this page.

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/spend"
)

// cfgCadenceFloor mirrors the floor config.SetLoop enforces. It is repeated
// here only so the input can refuse the value before the round trip; the
// authoritative refusal is still the one in SetLoop.
const cfgCadenceFloor = 15

type configView struct {
	Path string
	// HasPath separates "edited in this file" from "there is no file", so the
	// page never tells an operator to go and edit something that does not exist.
	HasPath bool

	Lanes        []cfgLane
	CadenceFloor int

	Console cfgConsole

	// Radii2 is the derived table of what each radius means under the current
	// policy; Radii is the bare list the selects are built from.
	Radii2   []cfgRadiusRow
	Radii    []config.Radius
	BlastRaw []cfgSetting
	Approval string

	// The live values the editable controls are populated from. Rendered
	// straight rather than through a formatter, because a form that shows a
	// prettified value and posts a raw one is a form that silently changes
	// something you did not touch.
	Blast      config.BlastPolicy
	CapRun     string
	CapDay     string
	CapSegment string
	Attempts   int
	Timeout    int
	Concurrent int
	Refresh    int
	OnDefaults bool
	// AllDark is true when no lane has ever fired, which almost always means
	// the scheduler is simply not running.
	AllDark bool

	Money cfgMoney

	Checks []cfgCheck

	Groups  []cfgGroup
	Clauses []string
}

// cfgLane is a lane's declaration plus enough liveness to answer the question
// an operator actually has here: is the cadence I am about to change even
// being honoured?
type cfgLane struct {
	Name         string
	Scope        string
	Enabled      bool
	EverySeconds int
	MaxPerTick   int
	Status       string
	Since        string
	Ticks        int
	Dispatches   int
	Note         string
}

type cfgConsole struct {
	Enabled   bool
	Worker    string
	Authority string
	Known     bool
	Timeout   int
	History   int
	Levels    []cfgLevel
}

type cfgLevel struct {
	Name     string
	Gives    string
	Advice   string
	Current  bool
	Executes string
}

type cfgRadiusRow struct {
	Radius   string
	Reaches  string
	Requires string
	Class    string
}

// cfgSetting is one file-only value: what it is called, what it is set to, and
// what goes wrong at the wrong value.
type cfgSetting struct {
	Key   string
	Value string
	Means string
}

type cfgGroup struct {
	Title string
	Sub   string
	Rows  []cfgSetting
}

type cfgMoney struct {
	DefaultModel  string
	DefaultPriced bool
	AnyPrices     bool
	Caps          []cfgCap
	Prices        []cfgPrice
	SpentToday    string
	DayCap        string
	Unpriced      int
	UnpricedRun   string
}

type cfgCap struct {
	Key       string
	Value     string
	Unlimited bool
	Means     string
}

type cfgPrice struct {
	Model      string
	In         string
	Out        string
	CacheRead  string
	CacheWrite string
	Default    bool
	// Source is where the rate came from: the operator's table, the built-in
	// defaults, or nowhere. Three values, because a rate somebody checked and a
	// rate that shipped in the binary deserve different degrees of trust.
	Source       string
	FromDefaults bool
	Unpriced     bool
}

type cfgCheck struct {
	ID         string
	Kind       string
	KindMeans  string
	Command    string
	Dir        string
	Verdict    string
	Means      string
	ExitDriven bool
	Detail     string
	Timeout    string
	Binds      bool
	Gates      []string
	Ungated    bool
}

// configPageV2 builds the whole page. It reads the live config rather than the
// file on disk, so what it shows is the config the fleet is actually running
// with — defaults filled in, and any cadence somebody changed since startup.
func (s *Server) configPageV2(*http.Request) (string, any, error) {
	v := configView{
		Path:         s.Cfg.Path(),
		CadenceFloor: cfgCadenceFloor,
	}
	v.HasPath = v.Path != ""
	if !v.HasPath {
		v.Path = "memory — this config was not loaded from a file"
	}
	v.Lanes = s.cfgLanes()
	v.Console = s.cfgConsole()
	v.Radii2, v.BlastRaw, v.Approval = s.cfgSafety()
	v.Money = s.cfgMoney()
	v.Checks = s.cfgChecks()
	v.Groups = s.cfgGroups()
	v.Clauses = s.Cfg.Prompts.MandatoryClauses

	v.Radii = config.Radii()
	v.Blast = s.Cfg.Blast
	v.CapRun = cfgDollars(s.Cfg.Budget.PerRunMicros)
	v.CapDay = cfgDollars(s.Cfg.Budget.PerDayMicros)
	v.CapSegment = cfgDollars(s.Cfg.Budget.PerSegmentMicros)
	v.Attempts = s.Cfg.Dispatch.MaxAttempts
	v.Timeout = s.Cfg.Dispatch.TimeoutSeconds
	v.Concurrent = s.Cfg.Dispatch.MaxConcurrent
	v.Refresh = s.Cfg.Server.RefreshSeconds
	v.OnDefaults = s.Cfg.Budget.UsesDefaultPricing()
	v.AllDark = true
	for _, l := range v.Lanes {
		if l.Ticks > 0 {
			v.AllDark = false
			break
		}
	}
	return "Config", v, nil
}

// cfgDollars renders a micros cap for a text box. Zero comes back empty rather
// than as "0", because the box means unlimited when it is blank and a literal
// zero would read as a cap of nothing.
func cfgDollars(micros int64) string {
	if micros <= 0 {
		return ""
	}
	return strconv.FormatFloat(float64(micros)/1e6, 'f', -1, 64)
}

func (s *Server) cfgLanes() []cfgLane {
	health := map[string]ledger.LoopHealth{}
	if s.Sched != nil {
		if hs, err := s.Sched.Health(s.now()); err == nil {
			for _, h := range hs {
				health[h.Loop] = h
			}
		}
	}
	now := s.now()
	var out []cfgLane
	for _, l := range s.Cfg.LoopList() {
		h := health[l.Name]
		row := cfgLane{
			Name: l.Name, Scope: l.Scope(), Enabled: l.Enabled,
			EverySeconds: l.EverySeconds, MaxPerTick: l.MaxPerTick,
			Ticks: h.Ticks, Dispatches: h.Dispatches,
			Status: "live", Since: "—",
			Note: "Firing on time.",
		}
		switch {
		case !l.Enabled:
			row.Status = "paused"
			row.Note = "Parked. Nothing in its scope is picked up until you tick running again."
		case h.LastTickMS == 0:
			row.Status = "NEVER RUN"
			row.Note = "Declared but never fired once. Usually the scheduler is not running: adlc schedule run."
		case !h.Fresh(now, 3):
			row.Status = "STALE"
			row.Note = "Enabled, but it has missed three cadences. Every firing writes a tick even when it dispatches nothing, so silence means the scheduler stopped, not that there was no work."
		}
		if h.LastTickMS > 0 {
			row.Since = now.Sub(time.UnixMilli(h.LastTickMS)).Round(time.Second).String() + " ago"
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) cfgConsole() cfgConsole {
	p := s.Cfg.Console
	c := cfgConsole{
		Enabled: p.Enabled, Worker: p.Worker, Authority: string(p.Authority),
		Known: p.Authority.Known(), Timeout: p.TimeoutSeconds, History: p.HistoryTurns,
	}
	if c.Worker == "" {
		c.Worker = "—"
	}
	// The comparison is built from the declared set so a level added to the
	// config package appears here rather than quietly missing from the table an
	// operator is using to choose between them.
	for _, lv := range config.ConsoleAuthorities() {
		row := cfgLevel{
			Name: string(lv), Gives: lv.Describe(), Advice: lv.Advice(),
			Current: lv == p.Authority,
		}
		switch {
		case lv.MayExecute(true):
			row.Executes = "everything, gates included"
		case lv.MayExecute(false):
			row.Executes = "ungated actions only"
		default:
			row.Executes = "nothing"
		}
		c.Levels = append(c.Levels, row)
	}
	return c
}

// cfgSafety derives, for every declared radius, what actually happens to a
// change of that size under the current policy.
//
// Derived rather than described, because the four numbers interact: what an
// operator needs is not "auto_apply_max is host" but "a fleet-wide change will
// stop and wait for two named people". The derivation mirrors
// authority.checkRadius and authority.checkApproval; if those move, this line
// is the one to move with them.
func (s *Server) cfgSafety() ([]cfgRadiusRow, []cfgSetting, string) {
	b := s.Cfg.Blast
	var rows []cfgRadiusRow
	for _, r := range config.Radii() {
		row := cfgRadiusRow{Radius: string(r), Reaches: r.Explain()}
		switch {
		case r == config.RadiusNone:
			row.Requires, row.Class = "nothing to apply", "mute"
		case r.Rank() <= b.AutoApplyMax.Rank():
			row.Requires, row.Class = "applied unattended, no approval", "warn"
		case b.TwoApprovalsMin.Known() && r.Rank() >= b.TwoApprovalsMin.Rank():
			row.Requires, row.Class = "two named approvers", "ok"
		case b.NamedApproverMin.Known() && r.Rank() >= b.NamedApproverMin.Rank():
			row.Requires, row.Class = "one named approver", "ok"
		default:
			row.Requires, row.Class = "an approval, unnamed accepted", "live"
		}
		rows = append(rows, row)
	}
	set := []cfgSetting{
		{"auto_apply_max", string(b.AutoApplyMax),
			"The largest radius an agent may apply with nobody watching. Raise it and changes of that size stop appearing on the Approvals page at all."},
		{"named_approver_min", string(b.NamedApproverMin),
			"From this radius up, an approval has to carry a person's name. Below it, an approval row with no name still counts — which is nobody, recorded."},
		{"two_approvals_min", string(b.TwoApprovalsMin),
			"From this radius up, two distinct people must approve. Set above your largest real radius and it never fires."},
		{"approval_ttl_minutes", fmt.Sprintf("%d", b.ApprovalTTLMinutes),
			"How long an approval stays good. Past it the apply is refused even though the plan never changed, so a long weekend does not leave a live approval lying around."},
	}
	approval := fmt.Sprintf(
		"An approval approves one exact plan digest and expires after %s. Change the plan and the approval stops applying — the apply is refused rather than waved through on the old decision.",
		cfgHours(b.ApprovalTTLMinutes))
	return rows, set, approval
}

// cfgHours renders a minute count the way a person would say it. Go's own
// duration string turns half a day into "12h0m0s", which reads as a machine
// talking to itself.
func cfgHours(minutes int) string {
	switch {
	case minutes <= 0:
		return "no time at all"
	case minutes < 60:
		return fmt.Sprintf("%d minutes", minutes)
	case minutes%1440 == 0:
		if minutes == 1440 {
			return "a day"
		}
		return fmt.Sprintf("%d days", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%d hours", minutes/60)
	}
	return fmt.Sprintf("%d hours %d minutes", minutes/60, minutes%60)
}

func (s *Server) cfgMoney() cfgMoney {
	b := s.Cfg.Budget
	m := cfgMoney{
		DefaultModel:  b.DefaultModel,
		DefaultPriced: b.Priced(b.DefaultModel),
		AnyPrices:     len(b.PriceMicrosPerMTok) > 0,
	}
	if m.DefaultModel == "" {
		m.DefaultModel = "—"
		m.DefaultPriced = false
	}
	makeCap := func(key string, micros int64, means string) cfgCap {
		c := cfgCap{Key: key, Value: spend.Micros(micros).String(), Means: means}
		if micros <= 0 {
			c.Unlimited, c.Value = true, "unlimited"
		}
		return c
	}
	day := makeCap("per_day_micros", b.PerDayMicros,
		"No dispatch starts once the last 24 hours have cost this much. Unlimited means no throttle at all — it does not mean exhausted.")
	m.DayCap = day.Value
	m.Caps = []cfgCap{
		makeCap("per_run_micros", b.PerRunMicros,
			"Checked after a run finishes, because a run's cost is not knowable before it. It cannot stop the overrun, only make it loud instead of silent."),
		day,
		makeCap("per_segment_micros", b.PerSegmentMicros,
			"The same cap, per deliverable, so one runaway deliverable cannot spend the whole day's budget."),
	}
	// Every model the fleet could actually price, from either table — not just
	// the operator's. Showing only the configured ones renders an empty table
	// on exactly the project that most needs to see what it is being charged,
	// and leaves the operator unable to tell "no rates" from "no models".
	seen := map[string]bool{}
	models := append([]string{}, b.PricedModels()...)
	for _, m := range models {
		seen[m] = true
	}
	for model := range config.DefaultPricing() {
		if !seen[model] {
			models = append(models, model)
		}
	}
	if b.DefaultModel != "" && !seen[b.DefaultModel] {
		if _, src := b.PriceFor(b.DefaultModel); src == config.PriceUnpriced {
			// An unpriced default model is the one row an operator must not be
			// able to miss, so it is listed even though nothing can price it.
			models = append(models, b.DefaultModel)
		}
	}
	sort.Strings(models)
	for _, model := range models {
		t, src := b.PriceFor(model)
		row := cfgPrice{Model: model, Default: model == b.DefaultModel,
			Source: string(src), FromDefaults: src == config.PriceDefaulted}
		if t == nil {
			row.In, row.Out, row.CacheRead, row.CacheWrite = "—", "—", "—", "—"
			row.Unpriced = true
		} else {
			row.In = spend.Micros(t[config.PriceInput]).String()
			row.Out = spend.Micros(t[config.PriceOutput]).String()
			row.CacheRead = spend.Micros(t[config.PriceCacheRead]).String()
			row.CacheWrite = spend.Micros(t[config.PriceCacheWrite]).String()
		}
		m.Prices = append(m.Prices, row)
	}
	if rep, err := spend.Summarise(s.Led, b, s.now()); err == nil {
		m.SpentToday = rep.Today.String()
		m.Unpriced, m.UnpricedRun = rep.Unpriced, rep.UnpricedRun
	}
	return m
}

func (s *Server) cfgChecks() []cfgCheck {
	var out []cfgCheck
	for i := range s.Cfg.Checks {
		ch := s.Cfg.Checks[i]
		row := cfgCheck{
			ID: ch.ID, Kind: string(ch.Kind), KindMeans: ch.Kind.Explain(),
			Command: strings.Join(ch.Command, " "), Dir: ch.Dir,
			Verdict: string(ch.Verdict), Means: ch.Verdict.Explain(),
			ExitDriven: ch.Verdict.TurnsOnExitCode(), Binds: ch.BindsArtifact,
			Gates:   ch.RequiredFor,
			Ungated: len(ch.RequiredFor) == 0,
			Timeout: ch.Timeout().String(),
		}
		// The rule's own parameters, where it has any. Without them a row saying
		// "passes on the listed exit codes" never says which ones.
		switch {
		case len(ch.AllowedExits) > 0:
			var codes []string
			for _, e := range ch.AllowedExits {
				codes = append(codes, fmt.Sprintf("%d", e))
			}
			row.Detail = "passes on exit " + strings.Join(codes, ", ")
		case ch.ExpectPattern != "":
			row.Detail = "pattern " + ch.ExpectPattern
		case ch.CountPattern != "":
			row.Detail = fmt.Sprintf("counts %s, needs at least %d", ch.CountPattern, ch.MinCount)
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) cfgGroups() []cfgGroup {
	c := s.Cfg
	d, p, l, sv := c.Dispatch, c.Prompts, c.Lease, c.Server
	num := func(n int) string { return fmt.Sprintf("%d", n) }
	orDash := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return "—"
		}
		return v
	}
	return []cfgGroup{
		{Title: "Dispatch", Sub: "how one agent run is started and bounded", Rows: []cfgSetting{
			{"command", orDash(strings.Join(d.Command, " ")),
				"How an agent is actually invoked. The control plane writes the prompt to a file and substitutes its path into this argv, and expects the run's envelope back at the path the argv names for it."},
			{"workdir_template", orDash(d.WorkDirTemplate),
				"Where each run's isolated tree is made. It must carry the run id, because a run whose workspace and run id disagree is invisible to every guard that keys on either."},
			{"isolation", orDash(d.Isolation),
				"How a run's tree is separated from the shared one. At \"none\", two concurrent runs edit the same files and the second one's diff contains the first one's work."},
			{"max_concurrent", num(d.MaxConcurrent),
				"Runs in flight at once. Raising it multiplies spend and lease contention long before it multiplies throughput."},
			{"max_attempts", num(d.MaxAttempts),
				"Rework attempts before an item stops looping and escalates to you. Set it high and a permanently broken item burns runs all night."},
			{"timeout_seconds", num(d.TimeoutSeconds),
				"How long one agent may run. A run still open after twice this shows on the Overview as a process that probably died without recording an end."},
			{"trunk", orDash(d.Trunk),
				"The branch the merge queue lands on. Empty means main."},
		}},
		{Title: "Prompts", Sub: "where the roles live and what every one of them must carry", Rows: []cfgSetting{
			{"prompts.dir", orDash(p.Dir),
				"Read at dispatch time, so editing a role file changes the role on the next run — reviewably, in a commit."},
			{"prompts.preamble_file", orDash(p.PreambleFile),
				"Assembled into every prompt, which is why a fleet-wide policy change is one edit instead of one per role."},
			{"prompts.mandatory_clauses", num(len(p.MandatoryClauses)) + " clause(s)",
				"Text that must survive verbatim in every role prompt. This is what stops a run editing a safety clause out of its own instructions."},
		}},
		{Title: "Leases", Sub: "what stops two runs editing the same thing", Rows: []cfgSetting{
			{"lease.dir", orDash(l.Dir), "Where the ephemeral claims are kept."},
			{"lease.ttl_minutes", num(l.TTLMinutes),
				"How long a claim survives. Too long and a crashed run blocks its subject until somebody notices; too short and two live runs hold the same one."},
			{"lease.strip_tokens", num(len(l.StripTokens)) + " word(s)",
				"Words ignored when two claims are compared for overlap. Too few and one shared generic noun refuses unrelated work; too many and two runs end up holding one subject under keys sharing not a single word."},
		}},
		{Title: "Scope and routing", Sub: "what the guards look at, and who owns what", Rows: []cfgSetting{
			{"source_roots", orDash(strings.Join(c.SourceRoots, ", ")),
				"Every tree-walking guard is scoped to these. Widen it and checks start refusing work over files nobody touched."},
			{"routing", num(len(c.Routing)) + " area(s) mapped",
				"Which worker owns each area tag. An area with no owner strands every item filed under it, and a stranded item looks exactly like one nobody has got round to. Listed on the Coordination page."},
			{"workers", num(len(c.Workers)) + " declared",
				"The roster. A worker that has never run is counted from here rather than inferred from its silence — see the Roles page."},
		}},
		{Title: "This dashboard", Sub: "and why it is only ever reachable from this machine", Rows: []cfgSetting{
			{"server.addr", orDash(sv.Addr),
				"Loopback only, and enforced at startup. This page writes to the ledger and has no authentication; those two facts are welded together."},
			{"server.refresh_seconds", num(sv.RefreshSeconds),
				"How often this page reloads itself. Zero stops the auto-refresh."},
			{"version", num(c.Version),
				"The config schema this build understands. A mismatch is refused at load rather than partially honoured."},
		}},
	}
}
