package gate

import (
	"bytes"
	"os/exec"
	"sort"
	"strings"
)

// TreeState reports the commit a workspace is on and whether the DECLARED
// SOURCE ROOTS carry uncommitted changes.
//
// The guard was right in principle and wrong on that tree, and it refused real
// work on a workspace nobody had edited.
//
// A guard needs a stated scope as much as it needs a firing case. The scope
// lives once, in the config, beside the checks — not as four separate
// walkers each maintaining its own skip list, which is how one of them came to
// be blind to a directory the others knew about.
//
// Why any of this matters: evidence produced over an uncommitted tree
// describes a state that has no commit and that nobody can check out again. A
// gate must not pass against a tree state that was never recorded.
func TreeState(dir string, sourceRoots []string) (sha string, dirty bool, paths []string, err error) {
	sha, err = gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", false, nil, err
	}
	args := []string{"status", "--porcelain", "--untracked-files=normal", "--"}
	if len(sourceRoots) == 0 {
		// Refuse to guess. An unscoped walk is the defect this function exists to
		// avoid, and reporting nothing would be worse than reporting that the
		// question was not asked.
		return sha, false, nil, errNoScope
	}
	args = append(args, sourceRoots...)
	out, err := gitOut(dir, args...)
	if err != nil {
		return sha, false, nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.LastIndex(line, " "); i >= 0 && i+1 < len(line) {
			paths = append(paths, line[i+1:])
		} else {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return sha, len(paths) > 0, paths, nil
}

type scopeError struct{}

func (scopeError) Error() string {
	return "no source_roots declared: an unscoped tree walk reports vendored and generated files as uncommitted work and refuses trees nobody edited"
}

var errNoScope = scopeError{}

// HeadSHA returns the commit a workspace is on.
func HeadSHA(dir string) (string, error) { return gitOut(dir, "rev-parse", "HEAD") }

// CommitExists reports whether a commit is reachable in this repository.
//
// It is checked before an item is called done, because a run's commit can
// become unreachable when its isolated workspace is removed — which leaves
// finished items pointing at code that no longer exists anywhere.
func CommitExists(dir, sha string) bool {
	if sha == "" {
		return false
	}
	_, err := gitOut(dir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &gitError{args: args, msg: msg}
	}
	return strings.TrimSpace(out.String()), nil
}

type gitError struct {
	args []string
	msg  string
}

func (e *gitError) Error() string { return "git " + strings.Join(e.args, " ") + ": " + e.msg }
