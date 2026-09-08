package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// A lane woken by a state change looks now rather than at the end of its
// cadence. Without this the first thing that happens on a new project happens
// whenever the slowest entry lane next fires, with the work already sitting
// there and the record already saying so.
func TestWakeCutsTheWaitShort(t *testing.T) {
	s := &Scheduler{}
	ch := s.wake.chanOf()
	done := make(chan bool, 1)
	go func() {
		// A cadence long enough that returning at all proves the wake was used,
		// and a "since" past the gap so the wake is honoured immediately.
		done <- s.waitTurn(context.Background(), ch, time.Hour, time.Minute)
	}()
	time.Sleep(20 * time.Millisecond)
	s.Wake()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waitTurn reported the context ended")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a state change did not wake the lane; it is still waiting on its timer")
	}
}

// The clean case: with nothing moving, the cadence is what decides. A wake that
// fired on its own would be a spin, and the timers are what prove a lane alive.
func TestWithoutAWakeTheCadenceDecides(t *testing.T) {
	s := &Scheduler{}
	start := time.Now()
	if !s.waitTurn(context.Background(), s.wake.chanOf(), 40*time.Millisecond, time.Minute) {
		t.Fatal("waitTurn should have returned on its cadence")
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Fatal("waitTurn returned before its cadence with nothing to wake it")
	}
}

// A wake must never fire a lane faster than minWakeGap after its last run. A
// run finishing is itself a state change, so without the gap a lane would wake
// itself in a loop.
func TestWakeWillNotSpinALane(t *testing.T) {
	s := &Scheduler{}
	ch := s.wake.chanOf()
	s.Wake()
	start := time.Now()
	// since = 0: the lane has only just fired.
	if !s.waitTurn(context.Background(), ch, time.Hour, 0) {
		t.Fatal("waitTurn reported the context ended")
	}
	if d := time.Since(start); d < minWakeGap {
		t.Fatalf("a wake fired the lane again after %v, inside the %v gap", d, minWakeGap)
	}
}

// The cancel case: a lane waiting on a wake still stops when the fleet stops.
func TestWakeStopsWithTheContext(t *testing.T) {
	s := &Scheduler{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if s.waitTurn(ctx, s.wake.chanOf(), time.Hour, time.Minute) {
		t.Fatal("a cancelled context must end the lane, not return a turn")
	}
}

// Only a change to what work EXISTS wakes the lanes. The fleet's own activity —
// a run starting, a lane ticking, a console turn — is not that, and waking on
// it would turn the fleet's noise into a spin.
func TestOnlyLifecycleEventsWakeLanes(t *testing.T) {
	wakes := []ledger.Kind{
		ledger.KindItemCreated, ledger.KindItemTransitioned, ledger.KindItemAmended,
		ledger.KindSegmentCreated, ledger.KindSegmentAdvanced,
		ledger.KindQuestionAnswered, ledger.KindApprovalDecided,
		ledger.KindTransitionAdmitted,
	}
	for _, k := range wakes {
		if !wakesLanes(k) {
			t.Errorf("%s changes what work exists and should wake the lanes", k)
		}
	}
	quiet := []ledger.Kind{
		ledger.KindRunStarted, ledger.KindRunFinished, ledger.KindLoopTicked,
		ledger.KindConsoleAsked, ledger.KindConsoleReplied, ledger.KindPromptPinned,
		ledger.KindGateObserved,
	}
	for _, k := range quiet {
		if wakesLanes(k) {
			t.Errorf("%s is the fleet's own activity, not new work; waking on it is a spin", k)
		}
	}
}

// The hook is a nudge, not a mechanism: it chains rather than replaces, so
// wiring a scheduler to a ledger cannot silently unhook whatever was there.
func TestWakeOnChainsRatherThanReplaces(t *testing.T) {
	l := &ledger.Ledger{}
	first := 0
	l.OnAppend = func(ledger.Event) { first++ }
	s := &Scheduler{}
	s.WakeOn(l)
	l.OnAppend(ledger.Event{Kind: ledger.KindItemCreated})
	if first != 1 {
		t.Fatal("WakeOn replaced an existing listener instead of chaining onto it")
	}
}
