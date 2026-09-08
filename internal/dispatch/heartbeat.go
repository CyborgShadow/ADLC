package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// Knowing that a run is over when nobody is left to say so.
//
// A run in flight is a process the control plane is waiting on. If the AGENT
// dies, the dispatcher sees it: Invoke returns, and the outcome is recorded
// like any other. The hole was the other case — the control plane itself dying,
// killed or crashed, while runs were open. Then nothing recorded an end,
// nothing released the lease, and the item sat claimed by a run that no longer
// existed until the lease's own TTL expired an hour later. Every surface said
// "running" the whole time.
//
// Labelling that after twice the dispatch timeout, which is what an earlier
// version did, is not recovery. It renames the stall.
//
// So a run in flight touches a file every few seconds, and a reaper looks for
// files nobody has touched. A heartbeat rather than a process handle because it
// answers the question that actually matters — is anybody still waiting on this
// run — and answers it the same way on every platform, for a control plane that
// was killed and for one that is wedged.
//
// What the reaper writes is `unknown`, never a failure and never a pass. That
// is not a guess: an absent heartbeat is evidence that nothing will ever report
// this run, and "nobody knows what it did" is precisely true. Closing it is what
// lets the lease go and the work be picked up again.

const (
	// beatEvery is how often a run in flight says it is still there.
	beatEvery = 10 * time.Second
	// beatDead is how long without one before a run is treated as orphaned.
	// Several missed beats, so a slow disk or a paused machine does not reap a
	// run that is fine.
	beatDead = 75 * time.Second
)

// beatFile is what a run in flight leaves behind.
type beatFile struct {
	RunID     string `json:"run_id"`
	ItemID    string `json:"item_id"`
	LeaseKey  string `json:"lease_key"`
	Worker    string `json:"worker"`
	StartedMS int64  `json:"started_ms"`
	BeatMS    int64  `json:"beat_ms"`
}

// runsDir is where the beats live: beside the leases, and never committed.
func (d *Dispatcher) runsDir() string {
	base := ".adlc"
	if d.Cfg != nil && d.Cfg.Lease.Dir != "" {
		base = filepath.Dir(d.Cfg.Lease.Dir)
	}
	if !filepath.IsAbs(base) && d.Repo != "" {
		base = filepath.Join(d.Repo, base)
	}
	return filepath.Join(base, "runs")
}

// beat keeps one run's file warm until the run ends.
type beat struct {
	once sync.Once
	stop chan struct{}
	path string
}

// startBeat begins saying that this run is being waited on.
func (d *Dispatcher) startBeat(f beatFile) *beat {
	dir := d.runsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.log("HEARTBEAT could not be started for %s: %v — if this process dies, that run will look open until its lease expires", f.RunID, err)
		return &beat{stop: make(chan struct{})}
	}
	b := &beat{stop: make(chan struct{}), path: filepath.Join(dir, f.RunID+".beat")}
	f.StartedMS = d.now().UnixMilli()
	write := func() {
		f.BeatMS = d.now().UnixMilli()
		body, err := json.Marshal(f)
		if err != nil {
			return
		}
		_ = os.WriteFile(b.path, body, 0o644)
	}
	write()
	go func() {
		t := time.NewTicker(beatEvery)
		defer t.Stop()
		for {
			select {
			case <-b.stop:
				return
			case <-t.C:
				write()
			}
		}
	}()
	return b
}

// done stops the heartbeat and removes the file. Called however the run ends,
// so a reaper only ever sees runs nobody is waiting on.
func (b *beat) done() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		close(b.stop)
		if b.path != "" {
			_ = os.Remove(b.path)
		}
	})
}

// Reap closes the runs nobody is waiting on any more.
//
// Returns how many it closed. Safe to call from any process and at any time:
// it only acts on a run the ledger still shows as open, and appending the end
// is what makes it idempotent.
func (d *Dispatcher) Reap() (int, error) {
	dir := d.runsDir()
	// A missing directory is not an error and must not end the pass: the case
	// this whole mechanism exists for is a control plane that left runs open
	// without ever writing a beat, and returning early here skipped exactly
	// those.
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	cutoff := d.now().Add(-beatDead).UnixMilli()
	closed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".beat") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		var f beatFile
		if json.Unmarshal(body, &f) != nil || f.RunID == "" {
			// Unreadable. Leave it rather than guess which run it was about.
			continue
		}
		if f.BeatMS > cutoff {
			continue
		}
		if err := d.closeOrphan(f); err != nil {
			d.log("REAP could not close %s: %v", f.RunID, err)
			continue
		}
		_ = os.Remove(path)
		closed++
	}

	// Second pass: runs the ledger shows as open that have NO heartbeat at all.
	//
	// A file-based pass alone only ever finds runs that got as far as writing
	// one. It misses the two cases that matter most — a control plane that died
	// between recording the start and starting the beat, and every run that was
	// already open when this mechanism was introduced. Those would sit open
	// forever, which is precisely the stall being fixed.
	//
	// The age check is what makes it safe: a live run writes its first beat
	// within a second of starting, so an open run with no beat file after more
	// than a minute is not one anybody is waiting on.
	runs, rerr := d.Led.Runs("", 500)
	if rerr != nil {
		return closed, nil
	}
	for _, r := range runs {
		if r.Finished() || r.StartedMS > cutoff {
			continue
		}
		if _, serr := os.Stat(filepath.Join(dir, r.RunID+".beat")); serr == nil {
			continue
		}
		f := beatFile{RunID: r.RunID, ItemID: r.ItemID, Worker: r.WorkerType,
			BeatMS: r.StartedMS, LeaseKey: d.leaseHeldBy(r.RunID)}
		if err := d.closeOrphan(f); err != nil {
			d.log("REAP could not close %s: %v", r.RunID, err)
			continue
		}
		closed++
	}
	return closed, nil
}

// leaseHeldBy finds the claim an orphaned run is still holding, so it can be
// let go. Without it the run closes but the item stays claimed, which fixes the
// record and not the stall.
func (d *Dispatcher) leaseHeldBy(runID string) string {
	if d.Leases == nil {
		return ""
	}
	live, err := d.Leases.Live()
	if err != nil {
		return ""
	}
	for _, l := range live {
		if l.RunID == runID {
			return l.Key
		}
	}
	return ""
}

// closeOrphan records the end of a run nobody will report, and frees its claim.
func (d *Dispatcher) closeOrphan(f beatFile) error {
	r, err := d.Led.Run(f.RunID)
	if err == nil && r.Finished() {
		// Somebody got there first, or this is a stale file from a run that
		// ended cleanly. Nothing to do, and nothing to say about it.
		return nil
	}
	if err != nil && err != ledger.ErrNotFound {
		return err
	}
	if err == nil {
		if _, aerr := d.Led.Append(d.actor(), ledger.KindRunFinished, f.RunID, ledger.RunFinished{
			RunID: f.RunID, Verdict: "unknown",
		}); aerr != nil {
			return aerr
		}
	}
	// The claim goes even when there was no run row to close: a lease held by a
	// run that was never registered blocks the item just as effectively.
	if d.Leases != nil && f.LeaseKey != "" {
		_ = d.Leases.Release(f.LeaseKey, f.RunID)
	}
	age := d.now().Sub(time.UnixMilli(f.BeatMS)).Round(time.Second)
	d.log("REAPED %s (%s) — no heartbeat for %s, so the process waiting on it is gone. Recorded UNKNOWN, not a failure: nobody knows what it did. Its claim on %s is released and the work can be picked up again.",
		f.RunID, f.Worker, age, orNone(f.ItemID))
	return nil
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "that item"
	}
	return s
}

func (d *Dispatcher) actor() string {
	if d.Actor != "" {
		return d.Actor
	}
	return "cli"
}

var _ = fmt.Sprintf
