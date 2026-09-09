package dispatch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// Workspace is a run's isolated tree.
//
// It is named for the run id, and that is the whole design. Naming the tree
// after the work, or the worker, or the day, all have the same defect: the
// name is not unique to the run, so the run's identity is not observable from
// the filesystem.
type Workspace struct {
	Dir          string
	EnvelopePath string
	Branch       string
	cleanup      func()
}

// Cleanup releases the workspace.
func (w *Workspace) Cleanup() {
	if w.cleanup != nil {
		w.cleanup()
	}
}

// prepareWorkspace creates isolation for a run according to policy.
//
// The three modes are honest about their trade-offs. "worktree" is real
// isolation with shared object storage and is the default. "copy" suits a tree
// that is not a git repository at all — an image build context, a set of
// playbooks. "none" runs in the shared checkout and is correct only for a
// single-lane fleet; it is offered because pretending otherwise would push
// people into faking a worktree they do not need.
// repoFor is the tree a run works in: the product for every role that builds
// it, and the control plane itself for the improver, whose subject IS this
// system. A tree that does not contain the thing being judged cannot be read to
// judge it.
func (d *Dispatcher) repoFor(capability string) string {
	if capability == config.CapImprove && d.ToolRepo != "" {
		return d.ToolRepo
	}
	return d.Repo
}

func (d *Dispatcher) prepareWorkspace(runID, base, capability string) (*Workspace, error) {
	repo := d.repoFor(capability)
	mode := d.Cfg.Dispatch.Isolation
	if mode == "" {
		mode = "worktree"
	}
	tmpl := d.Cfg.Dispatch.WorkDirTemplate
	if tmpl == "" {
		tmpl = filepath.Join(".adlc", "workspaces", "{{run_id}}")
	}
	if !strings.Contains(tmpl, "{{run_id}}") && mode != "none" {
		return nil, fmt.Errorf(
			"dispatch.workdir_template must contain {{run_id}}: a run wears one name, and a workspace named for anything else is invisible to every guard that keys on the run")
	}
	dir := strings.ReplaceAll(tmpl, "{{run_id}}", runID)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo, dir)
		// Absolute from here on, and that is not tidiness.
		//
		// The path is now relative to the repository, and git is invoked with
		// -C repo — so `worktree add` resolves it relative to repo a SECOND
		// time and builds the tree at repo/repo/... . While the fleet built in
		// the same directory it ran from, repo was "." and joining twice was
		// harmless; the day it was pointed at a product tree of its own, every
		// worktree landed one level too deep. git reported success, the
		// dispatcher wrote the run's prompt to the path it had asked for, and
		// the open failed on a directory that had been created somewhere else.
		// The lane then retried on cadence, because nothing about a workspace
		// that could not be prepared marks the item as having been attempted.
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}

	switch mode {
	case "none":
		return &Workspace{Dir: repo, EnvelopePath: envelopePath(repo, runID)}, nil

	case "copy":
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if err := copyTree(repo, dir, d.Cfg.SourceRoots); err != nil {
			return nil, err
		}
		return &Workspace{Dir: dir, EnvelopePath: envelopePath(dir, runID),
			cleanup: func() { os.RemoveAll(dir) }}, nil

	case "worktree":
		branch := "adlc/" + runID
		// Based on the work this item already has, not on the trunk.
		//
		// Every workspace used to start at HEAD. A judge, a tester and a
		// validator each got a clean checkout of main and were asked to verify
		// work that was not in it, and a builder retrying after a rejection
		// started again from nothing. The agents noticed before this did: the
		// trunk carries commits titled "Graft S1-002's declared file scope
		// from <sha> for the hygiene pass", which is a role hand-copying the
		// work into a workspace that should have contained it.
		if base == "" {
			base = "HEAD"
		}
		if out, err := git(repo, "worktree", "add", "-b", branch, dir, base); err != nil {
			return nil, fmt.Errorf("git worktree add: %v (%s)", err, out)
		}
		return &Workspace{
			Dir: dir, Branch: branch, EnvelopePath: envelopePath(dir, runID),
			// The worktree is removed but the BRANCH is kept. A run's commit can become
			// unreachable when its workspace is torn down, which leaves a finished item
			// pointing at code that no longer exists; the branch is what keeps it
			// reachable and costs nothing.
			cleanup: func() { git(repo, "worktree", "remove", "--force", dir) },
		}, nil
	}
	return nil, fmt.Errorf("unknown dispatch.isolation %q: want worktree, copy or none", mode)
}

// workspaceBase is the ref a run on an item is cut from, or "" for the trunk.
//
// It is not simply branchFor. branchFor answers a different question for the
// merge queue: when nothing is ahead of the trunk it returns the newest branch
// anyway, so the queue can report what it found rather than report no branch at
// all. A workspace cannot take that answer. A branch that is not ahead is one a
// run committed nothing to, or one whose work has already landed — and cutting
// a rework run from a landed tip hides every sibling change that landed since,
// so the item is reworked against a tree the trunk no longer has and the
// difference comes back as a merge conflict nobody wrote.
func (d *Dispatcher) workspaceBase(itemID string) string {
	if itemID == "" || d.Led == nil {
		return ""
	}
	b, err := d.branchFor(itemID)
	if err != nil || b == "" {
		// Unreadable is not a licence to guess at a branch. The trunk is what
		// this did before a branch was consulted at all.
		return ""
	}
	if !d.branchHasWork(d.trunk(), b) {
		return ""
	}
	return b
}

// envelopePath is ABSOLUTE.
//
// The agent runs with its working directory set to the workspace, so a path
// relative to the repository root resolves to the wrong place — or, on a
// shell that treats backslashes as escapes, to nothing at all. This was a live
// defect the first time a real agent was invoked: the runner created the
// directory, the agent wrote nowhere, and the run was recorded as producing no
// envelope.
func envelopePath(dir, runID string) string {
	p := filepath.Join(dir, ".adlc", "envelopes", runID+".json")
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// copyTree copies the declared source roots into an isolated directory.
//
// It copies the DECLARED roots rather than everything, for the same reason the
// dirty-tree guard is scoped to them: copying a whole repository sweeps in
// vendored dependencies, build output and version-control internals, and the
// resulting workspace is both enormous and subtly wrong.
func copyTree(src, dst string, roots []string) error {
	for _, root := range roots {
		from := filepath.Join(src, root)
		info, err := os.Stat(from)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.IsDir() {
			if err := copyFile(from, filepath.Join(dst, root)); err != nil {
				return err
			}
			continue
		}
		err = filepath.Walk(from, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if fi.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			return copyFile(p, target)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}
