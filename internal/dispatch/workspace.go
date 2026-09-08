package dispatch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func (d *Dispatcher) prepareWorkspace(runID string) (*Workspace, error) {
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
		dir = filepath.Join(d.Repo, dir)
	}

	switch mode {
	case "none":
		return &Workspace{Dir: d.Repo, EnvelopePath: envelopePath(d.Repo, runID)}, nil

	case "copy":
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if err := copyTree(d.Repo, dir, d.Cfg.SourceRoots); err != nil {
			return nil, err
		}
		return &Workspace{Dir: dir, EnvelopePath: envelopePath(dir, runID),
			cleanup: func() { os.RemoveAll(dir) }}, nil

	case "worktree":
		branch := "adlc/" + runID
		if out, err := git(d.Repo, "worktree", "add", "-b", branch, dir, "HEAD"); err != nil {
			return nil, fmt.Errorf("git worktree add: %v (%s)", err, out)
		}
		return &Workspace{
			Dir: dir, Branch: branch, EnvelopePath: envelopePath(dir, runID),
			// The worktree is removed but the BRANCH is kept. A run's commit can become
			// unreachable when its workspace is torn down, which leaves a finished item
			// pointing at code that no longer exists; the branch is what keeps it
			// reachable and costs nothing.
			cleanup: func() { git(d.Repo, "worktree", "remove", "--force", dir) },
		}, nil
	}
	return nil, fmt.Errorf("unknown dispatch.isolation %q: want worktree, copy or none", mode)
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
