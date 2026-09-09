package gate

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// TestTheShippedRootsWatchTheDocumentation puts an uncommitted documentation
// edit to the guard itself, rather than to git with the guard's pathspec.
//
// The criterion for S1-029 is that such an edit is "reported dirty by the
// guard", and the guard is this function: TreeState is what internal/gate calls
// before it collects evidence, and what authority quotes when it refuses. A
// test that reproduces the pathspec elsewhere pins the scope but not the
// reporting — it would still pass on the day TreeState stopped honouring the
// declared list, or stopped setting dirty. Prose here is source: docs/design.md
// states the decision behind every constraint, so a gate that cannot see it
// edited passes a commit whose reasoning was never recorded.
//
// It is red on the commit before docs and README.md were declared.
func TestTheShippedRootsWatchTheDocumentation(t *testing.T) {
	cfg, err := config.Load("../../adlc.json")
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}
	dir := treeRepo(t)
	writeUnder(t, dir, "README.md", "# t\n")
	writeUnder(t, dir, "docs/design.md", "# design\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "prose")

	// Clean case, and it is not a formality: declaring more roots must not make
	// a tree with nothing uncommitted read dirty. A guard that reports every
	// tree dirty is a permanent stop, and it passes any firing case on its own.
	if _, dirty, paths, err := TreeState(dir, cfg.SourceRoots); err != nil || dirty {
		t.Fatalf("a tree with nothing uncommitted must be clean under the shipped roots %v, got dirty=%v paths=%v err=%v", cfg.SourceRoots, dirty, paths, err)
	}

	// Firing case: an edit to README.md that nobody committed.
	writeUnder(t, dir, "README.md", "# t\n\nan edit nobody committed\n")
	_, dirty, paths, err := TreeState(dir, cfg.SourceRoots)
	if err != nil {
		t.Fatalf("TreeState: %v", err)
	}
	if !dirty {
		t.Fatalf("an uncommitted edit to README.md must be reported dirty by the guard under the shipped roots %v; it was not, so the gate would collect evidence over a tree nobody can check out again", cfg.SourceRoots)
	}
	if len(paths) != 1 || !strings.Contains(paths[0], "README.md") {
		t.Errorf("the guard must name the uncommitted path, got %v", paths)
	}

	// The other half of what the change declared.
	writeUnder(t, dir, "docs/design.md", "# design\n\nan edit nobody committed\n")
	if _, dirty, paths, err := TreeState(dir, cfg.SourceRoots); err != nil || !dirty || len(paths) != 2 {
		t.Errorf("an uncommitted edit under docs/ must be reported too, got dirty=%v paths=%v err=%v", dirty, paths, err)
	}

	// The scope is doing the work and not the tree: a directory nobody declared
	// stays invisible, which is why widening the list was the whole of the fix.
	writeUnder(t, dir, "vendor/third_party.go", "package vendor\n")
	if _, _, paths, err := TreeState(dir, cfg.SourceRoots); err != nil || len(paths) != 2 {
		t.Errorf("an undeclared directory must not be walked, got paths=%v err=%v", paths, err)
	}
}

// gitIn runs one git command in a throwaway repository, naming the command that
// broke rather than an errno nobody can place.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
