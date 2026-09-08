package dispatch

import (
	"context"
	"sync"
	"time"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Waking a lane when something moves, instead of when its timer comes round.
//
// The lanes poll, and that is deliberate: a tick is written on every firing,
// including the idle ones, so a lane that has quietly stopped reads STALE
// rather than looking like a lane with nothing to report. Handing work over
// directly would delete that evidence.
//
// But polling ALONE means the first thing that happens on a new project happens
// whenever the slowest entry lane next fires, and there is nothing to wait for:
// the work already exists, the lane is idle, and the record already says so. So
// a state change nudges the lanes to look now.
//
// The nudge carries no authority. It does not say which lane, or what to run —
// every lane that wakes re-selects from the record exactly as it would have on
// its timer, and the timers stay as the floor. A missed wake makes a lane late;
// it cannot make one wrong.

// minWakeGap is the shortest time between one firing of a lane and the next one
// a wake may cause. A run finishing is itself a state change, so without this a
// finishing run would wake the lane that started it, immediately, forever.
const minWakeGap = 5 * time.Second

// wakesLanes reports whether an event changed what work exists.
//
// The list is deliberately about the LIFECYCLE and not about activity. A run
// starting, a prompt being pinned, a lane ticking, a console turn — none of
// those change what is available to pick up, and waking on them would turn the
// fleet's own noise into a spin.
func wakesLanes(k ledger.Kind) bool {
	switch k {
	case ledger.KindItemCreated, ledger.KindItemTransitioned, ledger.KindItemAmended,
		ledger.KindSegmentCreated, ledger.KindSegmentAdvanced,
		ledger.KindQuestionAnswered, ledger.KindApprovalDecided,
		ledger.KindTransitionAdmitted:
		return true
	}
	return false
}

// waker is a broadcast anybody can wait on. Closing and replacing the channel
// wakes every waiter at once and loses nothing: a lane that was mid-fire picks
// up the next channel and sees the following wake.
type waker struct {
	mu sync.Mutex
	ch chan struct{}
}

func (w *waker) chanOf() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ch == nil {
		w.ch = make(chan struct{})
	}
	return w.ch
}

func (w *waker) fire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ch == nil {
		w.ch = make(chan struct{})
		return
	}
	close(w.ch)
	w.ch = make(chan struct{})
}

// Wake asks every lane to look now. Safe to call from anywhere, including from
// inside a ledger append: it takes one lock, touches nothing else, and returns.
func (s *Scheduler) Wake() { s.wake.fire() }

// WakeOn wires a ledger to this scheduler, so a state change is felt rather
// than waited for. Returns the hook so a caller can chain its own.
func (s *Scheduler) WakeOn(l *ledger.Ledger) {
	if l == nil {
		return
	}
	prev := l.OnAppend
	l.OnAppend = func(ev ledger.Event) {
		if prev != nil {
			prev(ev)
		}
		if wakesLanes(ev.Kind) {
			s.Wake()
		}
	}
}

// waitTurn blocks until the lane should look again: its cadence elapsed, or
// something moved. It returns false when the context ended.
func (s *Scheduler) waitTurn(ctx context.Context, wake <-chan struct{}, cadence, since time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(cadence):
		return true
	case <-wake:
	}
	// Woken early. Honour it, but never sooner than minWakeGap after the last
	// firing — a burst of transitions must not turn one lane into a spin.
	if d := minWakeGap - since; d > 0 {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
		}
	}
	return true
}
