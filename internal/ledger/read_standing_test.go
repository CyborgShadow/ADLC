package ledger

import (
	"testing"
	"time"
)

// "Still working" and "nobody will record how this ended" are different facts,
// and both show up as an absent verdict. Every surface printed UNKNOWN for
// both, so a healthy fleet mid-run looked exactly like one whose processes had
// been killed — and the only way to tell was to go hunting for a process.
func TestStandingTellsWorkingFromAbandoned(t *testing.T) {
	now := time.Unix(10_000, 0)
	timeout := 30 * time.Minute
	open := func(ago time.Duration) Run {
		return Run{StartedMS: now.Add(-ago).UnixMilli()}
	}

	if got := open(time.Minute).StandingAt(now, timeout); got != StandingWorking {
		t.Errorf("a run one minute into a thirty-minute timeout is working, got %s", got)
	}
	// The firing case has to sit past TWICE the timeout, not past it: a run
	// being killed at the timeout still has to record that it was.
	if got := open(45*time.Minute).StandingAt(now, timeout); got != StandingWorking {
		t.Errorf("a run inside twice its timeout is not yet abandoned, got %s", got)
	}
	if got := open(2*time.Hour).StandingAt(now, timeout); got != StandingAbandoned {
		t.Errorf("a run open for two hours against a thirty-minute timeout is abandoned, got %s", got)
	}
	// A finished run is read from its verdict whatever the clock says.
	done := open(2 * time.Hour)
	done.FinishedMS = now.UnixMilli()
	if got := done.StandingAt(now, timeout); got != StandingDone {
		t.Errorf("a run with a recorded end is done, got %s", got)
	}
	// With no timeout there is nothing to measure against, and calling a run
	// abandoned on no evidence is the thing this type exists to avoid.
	if got := open(100*time.Hour).StandingAt(now, 0); got != StandingWorking {
		t.Errorf("with no timeout declared, an open run cannot be called abandoned, got %s", got)
	}
}
