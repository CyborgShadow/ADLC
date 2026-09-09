package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// treeRepo makes a throwaway repository with one committed file under cmd/, so
// a test can add and remove uncommitted work and ask what the walk saw.
func treeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	// A checkout that rewrites line endings shows as a file modified by nobody,
	// which is the very state these tests exist to tell apart from real work.
	git("config", "core.autocrlf", "false")
	writeUnder(t, dir, "cmd/main.go", "package main\n")
	git("add", "-A")
	git("commit", "-m", "base")
	return dir
}

func writeUnder(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOnlyDeclaredRootsAreWalked pins the scope of the dirty-tree guard in both
// directions, because each direction is a different failure.
//
// A root that is declared but not walked lets the gate collect evidence over
// uncommitted files — a state that has no commit and that nobody can check out
// again — while reporting a clean tree. A directory that is NOT declared and is
// walked anyway refuses work over vendored or generated files nobody edited,
// which is the failure that gave this function its scope in the first place.
func TestOnlyDeclaredRootsAreWalked(t *testing.T) {
	dir := treeRepo(t)
	roots := []string{"cmd", "internal"}

	// Clean case first. A guard that reports every tree dirty is not a guard,
	// it is a permanent stop, and it would pass a firing-case-only test.
	if _, dirty, paths, err := TreeState(dir, roots); err != nil || dirty {
		t.Fatalf("a tree with nothing uncommitted must be clean, got dirty=%v paths=%v err=%v", dirty, paths, err)
	}

	// Firing case: one untracked file under a declared root.
	writeUnder(t, dir, "cmd/probe.txt", "probe\n")
	_, dirty, paths, err := TreeState(dir, roots)
	if err != nil {
		t.Fatalf("TreeState: %v", err)
	}
	if !dirty || len(paths) != 1 || !strings.Contains(paths[0], "probe.txt") {
		t.Errorf("one untracked file under a declared root must read dirty with that path named, got dirty=%v paths=%v", dirty, paths)
	}

	// The same file under a directory nobody declared is invisible, and that is
	// exactly why widening the declared list is the whole of the fix.
	writeUnder(t, dir, "site/cats/probe.html", "probe\n")
	if _, _, paths, err := TreeState(dir, roots); err != nil || len(paths) != 1 {
		t.Errorf("an undeclared directory must not be walked, got paths=%v err=%v", paths, err)
	}
	if _, dirty, paths, err := TreeState(dir, append(roots, "site")); err != nil || !dirty || len(paths) != 2 {
		t.Errorf("declaring that directory must make its uncommitted work visible, got dirty=%v paths=%v err=%v", dirty, paths, err)
	}
}

// TestADeclaredRootWithNothingInItYetIsCleanNotAnError covers the state a root
// is in between being declared and being filled, which is where site/ stands on
// the commit that declares it.
//
// If a root that does not exist on disk made the walk fail, declaring one ahead
// of the tree it names would refuse every run until somebody committed a file
// there — and the error would name git rather than the config that caused it.
func TestADeclaredRootWithNothingInItYetIsCleanNotAnError(t *testing.T) {
	dir := treeRepo(t)
	roots := []string{"cmd", "site"}

	if _, err := os.Stat(filepath.Join(dir, "site")); !os.IsNotExist(err) {
		t.Fatalf("this test needs site/ to be absent, stat said %v", err)
	}
	if _, dirty, paths, err := TreeState(dir, roots); err != nil {
		t.Fatalf("a declared root that does not exist yet must not fail the walk: %v", err)
	} else if dirty {
		t.Errorf("an absent root has no uncommitted work in it, got paths=%v", paths)
	}

	// Firing case: the same root, once it holds something, is watched like any
	// other — so the clean answer above was scope and not silence.
	writeUnder(t, dir, "site/cats/index.html", "<!doctype html>\n")
	if _, dirty, _, err := TreeState(dir, roots); err != nil || !dirty {
		t.Errorf("work under a declared root must read dirty once it exists, got dirty=%v err=%v", dirty, err)
	}
}

// TestAnUnscopedWalkIsRefusedRatherThanGuessed pins the refusal that gives the
// declared list its authority: with no scope the answer is "nobody asked", not
// "clean". Reporting clean there would turn a missing config into a pass.
func TestAnUnscopedWalkIsRefusedRatherThanGuessed(t *testing.T) {
	dir := treeRepo(t)

	if _, _, _, err := TreeState(dir, nil); err == nil {
		t.Error("a walk with no declared roots must be refused, not answered")
	}
	if _, _, _, err := TreeState(dir, []string{"cmd"}); err != nil {
		t.Errorf("a walk with a declared root must be answered, got %v", err)
	}
}

// TestTheShippedRootsWatchTheSiteTree reads the roots this repository actually
// declares and puts them to the walk, rather than asserting on the string
// "site" in a config file.
//
// The defect was never in TreeState: uncommitted work under site/ was invisible
// because site was not on the declared list, so the gate reported a clean tree
// over a page somebody had edited and never committed. A test that only reads
// the config would still pass if the walk stopped honouring the list; this one
// fails if either half breaks, and it is red on the commit before site was
// declared.
func TestTheShippedRootsWatchTheSiteTree(t *testing.T) {
	cfg, err := config.Load("../../adlc.json")
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}
	dir := treeRepo(t)

	// Clean case, under the shipped roots: declaring more roots must not make a
	// tree with nothing uncommitted read dirty.
	if _, dirty, paths, err := TreeState(dir, cfg.SourceRoots); err != nil || dirty {
		t.Fatalf("a clean tree must stay clean under the shipped roots, got dirty=%v paths=%v err=%v", dirty, paths, err)
	}

	// Firing case: a page nobody committed is uncommitted work, and the gate
	// has to see it before it collects evidence over that tree.
	writeUnder(t, dir, "site/cats/index.html", "<!doctype html>\n")
	_, dirty, paths, err := TreeState(dir, cfg.SourceRoots)
	if err != nil {
		t.Fatalf("TreeState: %v", err)
	}
	if !dirty {
		t.Fatalf("uncommitted work under site/ must be visible to the shipped roots %v; it was not, so the gate would report a clean tree over it", cfg.SourceRoots)
	}
	if len(paths) != 1 || !strings.Contains(paths[0], "site/") {
		t.Errorf("the walk must name the uncommitted site path, got %v", paths)
	}
}
