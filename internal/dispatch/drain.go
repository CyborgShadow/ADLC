package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Stopping a control plane without throwing away the work it is holding.
//
// A console process on Windows cannot be asked politely to exit: taskkill
// refuses without /F, and /F gives the process no chance to do anything. So
// every restart killed the agents mid-build, and the reaper — correctly —
// recorded them all as UNKNOWN. Six engineer runs went that way in one evening,
// each one real money spent on work nobody will ever see, because there was no
// way to say "stop when you are not in the middle of something".
//
// There is now. A stop file is the signal, for the same reason leases and
// heartbeats are files: it works from any process, on any platform, with
// nothing listening on a port and no new surface that can be prodded.
//
// Draining is the important half. Stopping dispatch is instant; the runs
// already in flight are not, and the whole point is to let them finish rather
// than pay for them twice.

// stopFile is watched by a running control plane.
const stopFile = "stop"

// StopPath is where the signal lives, beside the leases and the heartbeats and
// outside version control.
func (d *Dispatcher) StopPath() string { return filepath.Join(d.runsDir(), stopFile) }

// draining is read on every selection pass, so it takes effect immediately
// rather than at the end of a cadence.
var draining atomic.Bool

// Draining reports whether this control plane has been asked to stop.
func Draining() bool { return draining.Load() }

// Drain stops this process dispatching anything new. Work already running is
// left alone: it is being paid for either way, and the only thing that makes
// stopping cheap is letting it finish.
func Drain() { draining.Store(true) }

// AskToStop writes the signal a running control plane watches for.
func (d *Dispatcher) AskToStop() error {
	if err := os.MkdirAll(d.runsDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(d.StopPath(), []byte("stop\n"), 0o644)
}

// ClearStop removes the signal, so a fresh control plane does not read the
// previous one's instruction and shut down on startup.
func (d *Dispatcher) ClearStop() { _ = os.Remove(d.StopPath()) }

// StopRequested reports whether somebody has asked this fleet to stop.
func (d *Dispatcher) StopRequested() bool {
	_, err := os.Stat(d.StopPath())
	return err == nil
}

// InFlight is how many runs this control plane is still waiting on, counted
// from the heartbeats rather than from anything held in memory — so a caller in
// another process gets the same answer.
func (d *Dispatcher) RunsInFlight() int {
	entries, err := os.ReadDir(d.runsDir())
	if err != nil {
		return 0
	}
	n := 0
	// Wall clock, not the ledger clock: a beat file was written by another
	// process with its own clock, and its freshness is a question about the
	// filesystem rather than about the record.
	cutoff := time.Now().Add(-beatDead)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".beat") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		// A beat nobody has touched belongs to a run that is already gone; the
		// reaper will close it, and waiting for it would never end.
		if info.ModTime().After(cutoff) {
			n++
		}
	}
	return n
}

// WaitForQuiet blocks until no run is in flight or the limit passes, and says
// which happened.
func (d *Dispatcher) WaitForQuiet(limit time.Duration, tick func(int)) bool {
	deadline := time.Now().Add(limit)
	for {
		n := d.RunsInFlight()
		if n == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		if tick != nil {
			tick(n)
		}
		time.Sleep(2 * time.Second)
	}
}
