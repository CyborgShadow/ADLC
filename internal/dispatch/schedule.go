package dispatch

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
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
	// wake lets a state change bring every lane's next look forward. See
	// wake.go: it is a nudge with no authority, and the cadences remain the
	// floor, so a lane that misses one is late rather than stopped.
	wake waker
}

// Enabled lists the loops that will actually fire, in firing order.
func (s *Scheduler) Enabled() []config.LoopDecl {
	var out []config.LoopDecl
	for _, l := range s.Cfg.LoopList() {
		if l.Enabled {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OffsetSeconds < out[j].OffsetSeconds })
	return out
}

// MergeLaneName is the built-in lane that drains the merge queue.
//
// It is not declared in config because it dispatches nothing: merging is the
// control plane's own step, and a lane with no capability cannot be routed to a
// worker. It is still a lane in every way an operator cares about — a cadence,
// an offset, and a tick on every firing — because a merge queue that quietly
// stopped draining and one with nothing to drain look identical otherwise.
const MergeLaneName = "merge"

// MergeLane is that lane's declaration.
func MergeLane() config.LoopDecl {
	return config.LoopDecl{
		Name: MergeLaneName, Enabled: true, EverySeconds: 120, OffsetSeconds: 10, MaxPerTick: 1,
	}
}

// FireMerge drains the merge queue once and records the tick.
func (s *Scheduler) FireMerge(ctx context.Context) (MergeResult, error) {
	l := MergeLane()
	res, err := s.D.Merge(ctx)
	if err != nil {
		s.tick(l, false, "", "error: "+err.Error())
		return res, err
	}
	detail := res.Idle
	if n := len(res.Landed) + len(res.Requeued) + len(res.Returned); n > 0 {
		detail = fmt.Sprintf("%d landed, %d requeued, %d sent back",
			len(res.Landed), len(res.Requeued), len(res.Returned))
	}
	s.tick(l, len(res.Landed) > 0, "", detail)
	return res, nil
}

// FireOnce runs one loop's lane exactly once and records the tick.
//
// The tick is written whether or not anything was dispatched.
func (s *Scheduler) FireOnce(ctx context.Context, l config.LoopDecl) (TickResult, error) {
	f := Filter{Capability: l.Capability, Areas: l.Areas, Worker: l.Worker}

	// Dispatched together rather than back to back. Sequential dispatch made
	// max_per_tick describe how many runs a tick would do one after another,
	// so a single slow agent held its lane for as long as it ran — with an
	// hour's timeout, an hour. The fleet-wide ceiling still applies underneath;
	// this decides how many a lane offers, not how many run.
	last, dispatched, err := s.fireParallel(ctx, f, l.MaxPerTick)
	if err != nil {
		s.tick(l, false, "", "error: "+err.Error())
		return last, err
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
	loops := s.Cfg.LoopList()
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
	// The merge queue runs alongside them, on its own cadence, always.
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runMergeLane(ctx)
	}()
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
	s.D.log("LOOP %s started — every %ds, scope %s (and immediately when something moves)", l.Name, l.EverySeconds, l.Scope())
	for {
		cur := s.Cfg.Loop(l.Name)
		if cur == nil {
			s.D.log("LOOP %s is no longer declared; stopping", l.Name)
			return
		}
		// Taken BEFORE the fire. A state change that lands while this lane is
		// running is a change it has not looked at, and a channel taken
		// afterwards would have missed it.
		wake := s.wake.chanOf()
		started := time.Now()
		if cur.Enabled {
			if _, err := s.FireOnce(ctx, *cur); err != nil && ctx.Err() == nil {
				s.D.log("LOOP %s error: %v", l.Name, err)
			}
		}
		cadence := time.Duration(cur.EverySeconds) * time.Second
		if cadence <= 0 {
			cadence = time.Minute
		}
		if !s.waitTurn(ctx, wake, cadence, time.Since(started)) {
			s.D.log("LOOP %s stopped", l.Name)
			return
		}
	}
}

// runMergeLane is runLane for the built-in merge queue. It has no config entry
// to re-read, so its cadence is fixed and it cannot be paused from the
// dashboard — pausing the one lane that lands work would strand every item
// that reached the front of the queue, with nothing on the board saying why.
func (s *Scheduler) runMergeLane(ctx context.Context) {
	l := MergeLane()
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(l.OffsetSeconds) * time.Second):
	}
	s.D.log("LOOP %s started — every %ds, the control plane's own step", l.Name, l.EverySeconds)
	for {
		if _, err := s.FireMerge(ctx); err != nil && ctx.Err() == nil {
			s.D.log("LOOP %s error: %v", l.Name, err)
		}
		select {
		case <-ctx.Done():
			s.D.log("LOOP %s stopped", l.Name)
			return
		case <-time.After(time.Duration(l.EverySeconds) * time.Second):
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
	declared := s.Cfg.LoopList()
	out := make([]ledger.LoopHealth, 0, len(declared)+1)
	for _, l := range append(declared, MergeLane()) {
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
