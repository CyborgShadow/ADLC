// Package merge is the one lane onto the trunk.
//
// Work is done in isolated worktrees, so by the time an item is cleared to land
// its branch was gated against a tree that has since moved. Three things follow,
// and each is a step here:
//
//   - Rebase onto the current tip, because a branch that was green before the
//     rebase is not green after it.
//   - Re-run the gate ON THE REBASED TREE. Gating the pre-rebase tree checks a
//     state that will never exist on the trunk.
//   - Fast-forward only, under a lock, and only if the tip has not moved since
//     the gate ran. If it moved, start again.
//
// And one guard that is not obvious. A rebase can silently REVERT a sibling's
// landed work: the rebased tree comes out changing files this branch never
// touched, restoring them to what they were when the branch started. Duplicated
// work is loud — two agents doing one job collide. Reversion is silent, and
// every gate stays green throughout. So the invariant is checked directly: a
// branch may only change files its own commits touch, and everything else in
// the rebased tree must still equal the trunk.
package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Outcome is what happened to one attempt to land a branch.
type Outcome struct {
	Landed bool
	// Reason is empty when the branch landed, and otherwise names why not, in
	// the same closed vocabulary the transition authority uses.
	Reason string
	Detail string
	// MergedSHA is the trunk tip after a successful fast-forward.
	MergedSHA string
	// RebasedSHA is the tip of the rebased branch that was gated.
	RebasedSHA string
	// Clobbered lists paths the rebased tree would change that this branch's
	// own commits never touched. Non-empty means a silent revert was refused.
	Clobbered []string
	// Retryable says whether putting the item back in the queue is sensible.
	Retryable bool
}

// GateFunc re-runs the project's checks over a directory and reports whether
// they were green, with a one-line summary either way.
//
// It is a function rather than a concrete gate so that this package has no
// opinion about what a check is, and so the queue can be tested without one.
type GateFunc func(ctx context.Context, dir string) (green bool, summary string, err error)

// Queue lands branches on a trunk, one at a time.
type Queue struct {
	// Repo is the repository the trunk lives in.
	Repo string
	// Trunk is the branch being landed onto.
	Trunk string
	// Gate re-runs the checks on the rebased tree.
	Gate GateFunc
	// LockDir holds the advisory lock. Outside version control: the lock has a
	// lifetime of minutes and committing it would put coordination in history.
	LockDir string
	// LockTTL steals a lock whose holder is gone. Without it, one crashed
	// merge stops every future one.
	LockTTL time.Duration
	// Now is injectable for tests.
	Now func() time.Time
	// Log receives progress lines.
	Log func(string)

	// diffOverride stands in for the rebased-tree diff so the clobber guard's
	// refusal path can be driven end to end. A clean rebase cannot produce an
	// unowned change, which is the guard's whole point, so the condition it
	// exists for has to be injected rather than staged in git.
	diffOverride func(dir, from, to string) ([]string, error)
}

func (q *Queue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func (q *Queue) log(f string, a ...any) {
	if q.Log != nil {
		q.Log(fmt.Sprintf(f, a...))
	}
}

func (q *Queue) trunk() string {
	if q.Trunk == "" {
		return "main"
	}
	return q.Trunk
}

// Land takes one branch through the queue.
//
// The slow part — the rebase and the gate — happens OUTSIDE the lock. Held
// across a full gate run, the lock throttles the whole fleet behind one
// execution and the queue then grows faster than it drains. The lock is taken
// only to read the tip and to fast-forward, and the fast-forward is refused if
// the tip moved while the gate was running.
func (q *Queue) Land(ctx context.Context, branch string) (Outcome, error) {
	if strings.TrimSpace(branch) == "" {
		return Outcome{}, errors.New("no branch to land")
	}
	trunk := q.trunk()

	base, err := q.rev(trunk)
	if err != nil {
		return Outcome{}, fmt.Errorf("read %s: %w", trunk, err)
	}
	tip, err := q.rev(branch)
	if err != nil {
		return Outcome{}, fmt.Errorf("read %s: %w", branch, err)
	}
	if tip == base {
		return Outcome{Reason: "nothing_to_land",
			Detail: fmt.Sprintf("%s is already at %s", branch, short(base))}, nil
	}

	// The files this branch's own commits touch. Computed BEFORE the rebase,
	// because after it the diff includes whatever the rebase dragged in — which
	// is precisely what the clobber guard exists to catch.
	owned, err := q.filesTouchedBy(base, branch)
	if err != nil {
		return Outcome{}, err
	}
	if len(owned) == 0 {
		return Outcome{Reason: "nothing_to_land",
			Detail: fmt.Sprintf("%s changes no files against %s", branch, short(base))}, nil
	}

	work, cleanup, err := q.scratch(branch)
	if err != nil {
		return Outcome{}, err
	}
	defer cleanup()

	if out, err := q.git(work, "rebase", trunk); err != nil {
		q.git(work, "rebase", "--abort")
		return Outcome{Reason: "merge_conflict", Retryable: true, Detail: fmt.Sprintf(
			"%s does not rebase onto %s cleanly: %s", branch, trunk, firstLine(out))}, nil
	}
	rebased, err := q.rev2(work, "HEAD")
	if err != nil {
		return Outcome{}, err
	}

	// The guard. Everything the rebased tree changes against the trunk must be
	// a file this branch's own commits touched.
	changed, err := q.diffNames(work, trunk, "HEAD")
	if err != nil {
		return Outcome{}, err
	}
	clobbered := Unowned(owned, changed)
	if len(clobbered) > 0 {
		return Outcome{Reason: "stale_base_clobber", Clobbered: clobbered, RebasedSHA: rebased,
			Detail: fmt.Sprintf(
				"the rebased tree changes %d file(s) this branch never touched, which would silently revert work that landed after it started: %s. Rebase the branch yourself and look at what came back before trying again",
				len(clobbered), strings.Join(firstN(clobbered, 6), ", "))}, nil
	}

	if q.Gate != nil {
		green, summary, gerr := q.Gate(ctx, work)
		if gerr != nil {
			return Outcome{Reason: "merge_gate_failed", Retryable: true,
				Detail: "the gate could not be run on the rebased tree: " + gerr.Error()}, nil
		}
		if !green {
			return Outcome{Reason: "merge_gate_failed", RebasedSHA: rebased, Detail: fmt.Sprintf(
				"the branch was green before the rebase and is not after it: %s", summary)}, nil
		}
		q.log("MERGE gate green on the rebased tree %s", short(rebased))
	}

	unlock, err := q.lock(ctx)
	if err != nil {
		return Outcome{Reason: "lock_busy", Retryable: true, Detail: err.Error()}, nil
	}
	defer unlock()

	// The tip may have moved while the gate ran. If it did, what was gated is
	// not what would land, so this attempt is abandoned rather than forced.
	nowBase, err := q.rev(trunk)
	if err != nil {
		return Outcome{}, err
	}
	if nowBase != base {
		return Outcome{Reason: "trunk_moved", Retryable: true, RebasedSHA: rebased, Detail: fmt.Sprintf(
			"%s moved from %s to %s while the gate was running, so the gated tree is not the tree that would land",
			trunk, short(base), short(nowBase))}, nil
	}

	if out, err := q.git(q.Repo, "merge", "--ff-only", rebased); err != nil {
		return Outcome{Reason: "merge_conflict", Retryable: true, RebasedSHA: rebased,
			Detail: "fast-forward refused: " + firstLine(out)}, nil
	}
	landed, err := q.rev(trunk)
	if err != nil {
		return Outcome{}, err
	}
	q.log("MERGED %s -> %s at %s", branch, trunk, short(landed))
	return Outcome{Landed: true, MergedSHA: landed, RebasedSHA: rebased}, nil
}

// ---------------------------------------------------------------- the lock

// lock takes an advisory file lock on the trunk.
//
// Concurrent writes to one repository do not serialise as cleanly as the
// command-line tools suggest — index and ref updates race, and a background
// process touching the same repository races both. One writer at a time, held
// briefly, avoids the whole class.
func (q *Queue) lock(ctx context.Context) (func(), error) {
	dir := q.LockDir
	if dir == "" {
		dir = filepath.Join(q.Repo, ".adlc")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "trunk.lock")
	ttl := q.LockTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	deadline := q.now().Add(30 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", q.now().UnixMilli())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// Steal a lock whose holder is gone, or one crashed merge stops every
		// future one.
		if b, rerr := os.ReadFile(path); rerr == nil {
			var ms int64
			fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &ms)
			if ms > 0 && q.now().Sub(time.UnixMilli(ms)) > ttl {
				q.log("MERGE stealing a lock held since %s", time.UnixMilli(ms).Format(time.RFC3339))
				os.Remove(path)
				continue
			}
		}
		if q.now().After(deadline) {
			return nil, fmt.Errorf("the trunk lock is held by another merge; try again")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------- git

// scratch prepares an isolated worktree to rebase in, so a failed rebase never
// touches the shared checkout or the branch itself.
func (q *Queue) scratch(branch string) (string, func(), error) {
	dir := filepath.Join(q.Repo, ".adlc", "merge", sanitise(branch))
	os.RemoveAll(dir)
	if out, err := q.git(q.Repo, "worktree", "add", "--detach", dir, branch); err != nil {
		return "", func() {}, fmt.Errorf("prepare a merge worktree: %v (%s)", err, out)
	}
	return dir, func() {
		q.git(q.Repo, "worktree", "remove", "--force", dir)
	}, nil
}

func (q *Queue) rev(ref string) (string, error)              { return q.rev2(q.Repo, ref) }
func (q *Queue) rev2(dir, ref string) (string, error)        { return q.gitOut(dir, "rev-parse", ref) }
func (q *Queue) git(dir string, a ...string) (string, error) { return q.gitOut(dir, a...) }

func (q *Queue) gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// filesTouchedBy is the set of paths this branch's own commits change, taken
// from the commits themselves rather than from a diff against the trunk.
func (q *Queue) filesTouchedBy(base, branch string) (map[string]bool, error) {
	out, err := q.gitOut(q.Repo, "diff", "--name-only", base+"..."+branch)
	if err != nil {
		return nil, fmt.Errorf("list the files %s changes: %s", branch, firstLine(out))
	}
	owned := map[string]bool{}
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			owned[p] = true
		}
	}
	return owned, nil
}

// Unowned is the clobber guard: the paths a rebased tree would change that the
// branch's own commits never touched.
//
// For a branch that rebases cleanly onto the trunk this set is always empty,
// which is the point — it is an assertion, not a heuristic. It stops being
// empty when the rebase produced something other than the branch's own patch:
// a commit dropped as already-upstream, a conflict resolved by a merge driver
// or by recorded resolutions, a flattened merge commit, a submodule pointer
// carried backwards. Those all silently revert work that landed after the
// branch started, and every check stays green on the way through, so nothing
// downstream would notice. The comparison is cheap and it is checked directly.
func Unowned(owned map[string]bool, changed []string) []string {
	var out []string
	for _, p := range changed {
		if !owned[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func (q *Queue) diffNames(dir, from, to string) ([]string, error) {
	if q.diffOverride != nil {
		return q.diffOverride(dir, from, to)
	}
	out, err := q.gitOut(dir, "diff", "--name-only", from, to)
	if err != nil {
		return nil, fmt.Errorf("diff %s..%s: %s", from, to, firstLine(out))
	}
	var names []string
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			names = append(names, p)
		}
	}
	return names, nil
}

// ---------------------------------------------------------------- helpers

func sanitise(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, s)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(none)"
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

func firstN(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return append(append([]string{}, v[:n]...), fmt.Sprintf("… and %d more", len(v)-n))
}
