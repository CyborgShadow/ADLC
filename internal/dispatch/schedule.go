package dispatch

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/CyborgShadow/adlc/internal/config"
	"github.com/CyborgShadow/adlc/internal/ledger"
)

// Scheduler runs the declared loops on their own cadences.
//
// Both of those are closed here. Every lane dispatches through the same
// transition authority as everything else, and **every firing writes a ledger
// tick, including the idle ones**. A lane that stops firing therefore stops
// producing ticks, and its silence becomes a derived alarm rather than an
// absence nobody reads.
type Scheduler struct {
	D   *Dispatcher
	Cfg *config.Config
	// Grace multiplies a loop's cadence before its liveness is called stale.
	Grace float64
}

// Enabled lists the loops that will actually fire, in firing order.
func (s *Scheduler) Enabled() []config.LoopDecl {
	var out []config.LoopDecl
	for _, l := range s.Cfg.Loops {
		if l.Enabled {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OffsetSeconds < out[j].OffsetSeconds })
	return out
}

// FireOnce runs one loop's lane exactly once and records the tick.
//
// The tick is written whether or not anything was dispatched.
func (s *Scheduler) FireOnce(ctx context.Context, l config.LoopDecl) (TickResult, error) {
	f := Filter{Capability: l.Capability, Areas: l.Areas, Worker: l.Worker}

	var last TickResult
	dispatched := 0
	for i := 0; i < max(1, l.MaxPerTick); i++ {
		res, err := s.D.TickScoped(ctx, f)
		if err != nil {
			s.tick(l, false, "", "error: "+err.Error())
			return res, err
		}
		last = res
		if !res.Dispatched {
			break
		}
		dispatched++
	}

	detail := last.Idle
	if dispatched > 0 {
		detail = fmt.Sprintf("%d dispatch(es); last %s", dispatched, describe(last))
	}
	s.tick(l, dispatched > 0, last.RunID, detail)
	return last, nil
}

func describe(r TickResult) string {
	switch {
	case r.Created > 0:
		return fmt.Sprintf("%s created %d item(s)", r.RunID, r.Created)
	case r.Admitted:
		return fmt.Sprintf("%s admitted", r.RunID)
	case r.Reason != "":
		return fmt.Sprintf("%s refused [%s]", r.RunID, r.Reason)
	}
	return r.RunID
}

func (s *Scheduler) tick(l config.LoopDecl, dispatched bool, runID, detail string) {
	_, _ = s.D.Led.Append(s.D.Actor, ledger.KindLoopTicked, l.Name, ledger.LoopTicked{
		Loop: l.Name, Scope: l.Scope(), Dispatched: dispatched, RunID: runID, Detail: detail,
	})
}

// Run fires every enabled loop on its cadence until the context is cancelled.
//
// Each lane is its own goroutine with its own timer, staggered by its declared
// offset. Two lanes firing on the same second contend for leases and waste a
// pick each, which is why the stagger is declared rather than left to chance.
//
// Cancellation is clean by construction: a lane finishes the dispatch it is in
// and then stops, and everything it did is already on the ledger, because the
// ledger is written as the run progresses rather than at the end. There is no
// in-memory state to lose, which is what makes stopping and restarting safe at
// any moment.
func (s *Scheduler) Run(ctx context.Context) error {
	// Every DECLARED lane gets a goroutine, not only the enabled ones, so a lane
	// switched on in the dashboard starts firing without a restart.
	loops := s.Cfg.Loops
	if len(loops) == 0 {
		return fmt.Errorf("no loops are enabled: declare some under \"loops\" in the config, or use `adlc dispatch loop` for a single lane")
	}
	var wg sync.WaitGroup
	for _, l := range loops {
		wg.Add(1)
		go func(l config.LoopDecl) {
			defer wg.Done()
			s.runLane(ctx, l)
		}(l)
	}
	wg.Wait()
	return ctx.Err()
}

// runLane re-reads its own declaration on every iteration, so a cadence change
// or a pause made in the dashboard takes effect on the next tick rather than
// on the next restart. An operator reaching for a control during an incident
// should not have to bounce the fleet to use it.
func (s *Scheduler) runLane(ctx context.Context, l config.LoopDecl) {
	if l.OffsetSeconds > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(l.OffsetSeconds) * time.Second):
		}
	}
	s.D.log("LOOP %s started — every %ds, scope %s", l.Name, l.EverySeconds, l.Scope())
	for {
		cur := s.Cfg.Loop(l.Name)
		if cur == nil {
			s.D.log("LOOP %s is no longer declared; stopping", l.Name)
			return
		}
		if cur.Enabled {
			if _, err := s.FireOnce(ctx, *cur); err != nil && ctx.Err() == nil {
				s.D.log("LOOP %s error: %v", l.Name, err)
			}
		}
		wait := time.Duration(cur.EverySeconds) * time.Second
		if wait <= 0 {
			wait = time.Minute
		}
		select {
		case <-ctx.Done():
			s.D.log("LOOP %s stopped", l.Name)
			return
		case <-time.After(wait):
		}
	}
}

// Health derives each declared loop's liveness from the tick record.
//
// A loop that has never ticked reports as never having run rather than as
// silent, and a loop whose last tick is older than its own cadence reports as
// stale. Neither reads as healthy, and neither is self-reported.
func (s *Scheduler) Health(now time.Time) ([]ledger.LoopHealth, error) {
	measured, err := s.D.Led.LoopHealthAll()
	if err != nil {
		return nil, err
	}
	grace := s.Grace
	if grace <= 0 {
		grace = 3
	}
	out := make([]ledger.LoopHealth, 0, len(s.Cfg.Loops))
	for _, l := range s.Cfg.Loops {
		h := measured[l.Name]
		h.Loop = l.Name
		h.Scope = l.Scope()
		h.Enabled = l.Enabled
		h.EverySecond = l.EverySeconds
		out = append(out, h)
	}
	// A lane that has ticks but is no longer declared still shows, because a loop
	// somebody deleted from the config is exactly the kind of thing an operator
	// needs to see once rather than never.
	for name, h := range measured {
		if s.Cfg.Loop(name) == nil {
			h.Loop = name + " (no longer declared)"
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Loop < out[j].Loop })
	return out, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
