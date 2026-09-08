package dispatch

import (
	"context"
	"sync"
)

// Throughput.
//
// Three things were wrong with how much of this fleet could run at once, and
// only one of them looked like a setting.
//
// `dispatch.max_concurrent` was declared, defaulted, rendered on the Config
// page — and read by nothing. An operator raising it to four got exactly the
// same behaviour as leaving it at one, which is worse than the setting not
// existing: a control that does nothing still gets believed.
//
// `max_per_tick` looked like parallelism and was not. A lane fired, dispatched,
// and blocked until the agent finished before considering the next one, so the
// value described how many runs a tick would do *in sequence*. With an hour's
// timeout, one slow agent held its lane for the hour.
//
// And the real concurrency of the fleet was whatever the lane count happened to
// be, since each lane is its own goroutine — an emergent number nobody chose,
// with no ceiling. Ten lanes could have ten agents running, and the only thing
// bounding the spend was that most lanes usually find nothing.
//
// So: the cap is real and global, a lane can genuinely run several at once, and
// both are declared rather than emergent.

// slots bounds how many agents run at once across the whole fleet.
//
// Global rather than per-lane because the resource being protected is global:
// the money, the machine, and the provider's rate limit do not care which lane
// spent them. A per-lane limit multiplies by the lane count into a number
// nobody wrote down.
type slots struct {
	ch chan struct{}
}

func newGate(n int) *slots {
	if n < 1 {
		n = 1
	}
	return &slots{ch: make(chan struct{}, n)}
}

// enter blocks until a slot is free or the context ends.
//
// Blocking rather than skipping: a lane that gave up because the fleet was busy
// would have to wait a whole cadence to try again, and its work would look like
// work nobody wanted rather than work that was queued.
func (g *slots) enter(ctx context.Context) error {
	select {
	case g.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *slots) leave() { <-g.ch }

// InFlight is how many agents are running right now.
func (g *slots) inFlight() int { return len(g.ch) }

// Limit returns the fleet's concurrency slots, built once from the config.
//
// Built lazily and kept, so every lane shares one — a slots per lane would be
// the emergent-ceiling problem again with extra steps.
func (d *Dispatcher) limit() *slots {
	d.slotsOnce.Do(func() {
		n := d.Cfg.Dispatch.MaxConcurrent
		if n < 1 {
			n = 1
		}
		d.slots = newGate(n)
	})
	return d.slots
}

// InFlight reports how many agents are running, for the dashboard.
func (d *Dispatcher) InFlight() int { return d.limit().inFlight() }

// MaxConcurrent reports the configured ceiling, for the dashboard.
func (d *Dispatcher) MaxConcurrent() int {
	if n := d.Cfg.Dispatch.MaxConcurrent; n > 0 {
		return n
	}
	return 1
}

// fireParallel runs up to n dispatches at once for one lane and reports what
// happened.
//
// Each goroutine takes its own candidate through the ordinary path, so the
// lease is still what stops two runs meeting on one item — this changes how
// many are attempted, not what protects them.
func (s *Scheduler) fireParallel(ctx context.Context, f Filter, n int) (TickResult, int, error) {
	if n < 1 {
		n = 1
	}
	if n == 1 {
		res, err := s.D.TickScoped(ctx, f)
		got := 0
		if res.Dispatched {
			got = 1
		}
		return res, got, err
	}

	var (
		mu    sync.Mutex
		last  TickResult
		first error
		got   int
		wg    sync.WaitGroup
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.D.TickScoped(ctx, f)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && first == nil {
				first = err
			}
			// An idle result is kept only while nothing has been dispatched, so
			// the tick record says what the lane did rather than whatever its
			// last goroutine happened to find.
			if res.Dispatched {
				got++
				last = res
			} else if got == 0 {
				last = res
			}
		}()
	}
	wg.Wait()
	return last, got, first
}
