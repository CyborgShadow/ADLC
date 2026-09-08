package dispatch

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// countingRunner records how many agents are inside Invoke at once, which is
// the only thing that settles whether a concurrency ceiling is real.
type countingRunner struct {
	mu      sync.Mutex
	now     int
	peak    int
	calls   int32
	release chan struct{}
	hold    time.Duration
}

func (r *countingRunner) Invoke(ctx context.Context, in Invocation) (Result, error) {
	atomic.AddInt32(&r.calls, 1)
	r.mu.Lock()
	r.now++
	if r.now > r.peak {
		r.peak = r.now
	}
	r.mu.Unlock()

	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	} else if r.hold > 0 {
		time.Sleep(r.hold)
	}

	r.mu.Lock()
	r.now--
	r.mu.Unlock()

	env := fmt.Sprintf(`{"envelope_version":"1","run_id":%q,"worker_type":%q,
		"work_item_id":%q,"verdict":"pass","summary":"done","commands_run":[],"outputs":{}}`,
		in.RunID, in.WorkerType, in.ItemID)
	_ = os.WriteFile(in.EnvelopePath, []byte(env), 0o600)
	return Result{Envelope: []byte(env)}, nil
}

func (r *countingRunner) peakSeen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// TestTheConcurrencyCeilingIsReal is the whole point of this change. The
// setting existed, was defaulted, was rendered on the Config page, and was read
// by nothing — so raising it did exactly nothing, which is worse than not
// having it, because a control that does nothing still gets believed.
func TestTheConcurrencyCeilingIsReal(t *testing.T) {
	for _, ceiling := range []int{1, 3} {
		t.Run(fmt.Sprintf("max_concurrent=%d", ceiling), func(t *testing.T) {
			ws, routing := specialists()
			h := newHarness(t, ws, routing)
			h.segment(t, "S1", "seg", "", 0)
			for i := 1; i <= 8; i++ {
				h.item(t, fmt.Sprintf("S1-%03d", i), "S1", "ui", "ready")
			}
			run := &countingRunner{hold: 40 * time.Millisecond}
			h.D.Runner = run
			h.Cfg.Dispatch.MaxConcurrent = ceiling

			s := &Scheduler{D: h.D, Cfg: h.Cfg, Grace: 3}
			lane := config.LoopDecl{Name: "build", Enabled: true, EverySeconds: 60,
				Capability: config.CapImplement, MaxPerTick: 8}
			if _, err := s.FireOnce(context.Background(), lane); err != nil {
				t.Fatal(err)
			}
			if got := run.peakSeen(); got > ceiling {
				t.Fatalf("%d agents ran at once against a ceiling of %d — the setting is not enforced",
					got, ceiling)
			}
			if run.calls == 0 {
				t.Fatal("nothing was dispatched at all, so this proves nothing")
			}
		})
	}
}

// TestALaneRunsItsTickInParallel covers the other half. max_per_tick used to
// mean "this many, one after another", so a lane with a slow agent was held for
// as long as that agent ran.
func TestALaneRunsItsTickInParallel(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	for i := 1; i <= 4; i++ {
		h.item(t, fmt.Sprintf("S1-%03d", i), "S1", "ui", "ready")
	}
	run := &countingRunner{release: make(chan struct{})}
	h.D.Runner = run
	h.Cfg.Dispatch.MaxConcurrent = 4

	s := &Scheduler{D: h.D, Cfg: h.Cfg, Grace: 3}
	lane := config.LoopDecl{Name: "build", Enabled: true, EverySeconds: 60,
		Capability: config.CapImplement, MaxPerTick: 3}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.FireOnce(context.Background(), lane)
	}()

	// Nothing is allowed to finish yet. If dispatch were sequential, exactly one
	// agent would be inside Invoke however long we wait.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && run.peakSeen() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	peak := run.peakSeen()
	close(run.release)
	<-done

	if peak < 2 {
		t.Fatalf("a lane with max_per_tick 3 held only %d agent(s) at once; the tick is still sequential", peak)
	}
}

// TestTheCeilingBindsAcrossLanesNotWithinOne pins that the limit is global.
// A per-lane ceiling multiplies by the lane count into a number nobody wrote
// down, which is the state this replaced.
func TestTheCeilingBindsAcrossLanesNotWithinOne(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	for i := 1; i <= 4; i++ {
		h.item(t, fmt.Sprintf("S1-%03d", i), "S1", "ui", "ready")
		h.item(t, fmt.Sprintf("S2-%03d", i), "S1", "ui", "ready_for_testing")
	}
	run := &countingRunner{hold: 30 * time.Millisecond}
	h.D.Runner = run
	h.Cfg.Dispatch.MaxConcurrent = 2

	s := &Scheduler{D: h.D, Cfg: h.Cfg, Grace: 3}
	build := config.LoopDecl{Name: "build", Enabled: true, EverySeconds: 60,
		Capability: config.CapImplement, MaxPerTick: 4}
	test := config.LoopDecl{Name: "test", Enabled: true, EverySeconds: 60,
		Capability: config.CapTest, MaxPerTick: 4}

	var wg sync.WaitGroup
	for _, l := range []config.LoopDecl{build, test} {
		wg.Add(1)
		go func(l config.LoopDecl) {
			defer wg.Done()
			_, _ = s.FireOnce(context.Background(), l)
		}(l)
	}
	wg.Wait()

	if got := run.peakSeen(); got > 2 {
		t.Fatalf("two lanes together ran %d agents against a fleet ceiling of 2 — the limit is per lane, not global", got)
	}
}
