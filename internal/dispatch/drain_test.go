package dispatch

import (
	"os"
	"testing"
	"time"
)

// Asking a fleet to stop drains it rather than killing it.
//
// A console process cannot be asked politely to exit on every platform this
// runs on: taskkill refuses without /F, and /F gives it no chance to do
// anything. So every restart killed the agents mid-build and the reaper
// recorded them all as UNKNOWN — six engineer runs went that way in one
// evening, each real money spent on work nobody will ever see.
func TestAskingToStopDrainsRatherThanKills(t *testing.T) {
	h := newHarness(t, nil, nil)
	d := h.D
	draining.Store(false)
	defer draining.Store(false)

	if d.StopRequested() {
		t.Fatal("a fresh fleet was already carrying a stop request")
	}
	if err := d.AskToStop(); err != nil {
		t.Fatal(err)
	}
	if !d.StopRequested() {
		t.Fatal("the request was written and cannot be read back")
	}

	// Draining stops NEW work and leaves what is running alone: it is being
	// paid for either way, and letting it finish is the whole point.
	writeBeat(t, d, beatFile{RunID: "r-live", BeatMS: d.now().UnixMilli()})
	Drain()
	if !Draining() {
		t.Fatal("draining was requested and is not in force")
	}
	if n := d.RunsInFlight(); n != 1 {
		t.Fatalf("%d runs in flight, want the one that is still going", n)
	}
	if d.WaitForQuiet(200*time.Millisecond, nil) {
		t.Error("reported quiet while a run was still in flight")
	}

	// A beat nobody has touched belongs to a run that is already gone. Waiting
	// for it would never end, so it is not counted.
	writeBeat(t, d, beatFile{RunID: "r-dead", BeatMS: d.now().Add(-time.Hour).UnixMilli()})
	if err := os.Chtimes(d.runsDir()+"/r-dead.beat",
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := d.RunsInFlight(); n != 1 {
		t.Fatalf("%d in flight; a cold heartbeat must not hold a drain open forever", n)
	}

	// And the clean case: with nothing running it stops at once.
	if err := os.Remove(d.runsDir() + "/r-live.beat"); err != nil {
		t.Fatal(err)
	}
	if !d.WaitForQuiet(2*time.Second, nil) {
		t.Error("nothing is in flight and the drain did not finish")
	}

	// The signal is cleared, so the NEXT control plane does not read the last
	// one's instruction and shut itself down on startup.
	d.ClearStop()
	if d.StopRequested() {
		t.Error("the stop request outlived the fleet it was meant for")
	}
}
