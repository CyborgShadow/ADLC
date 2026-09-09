package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// A source root that is a plain file reaches an isolated workspace.
//
// copyTree has always had a branch for a non-directory root, and nothing
// exercised it. That matters now that adlc.json and .gitattributes are declared
// roots: under dispatch.isolation "copy" the config the agent's own gate reads
// would simply be absent from its workspace, and copyTree returns no error for
// a root it did not copy — so the failure is silent, and shows up as a run that
// cannot load a config nobody noticed was missing.
func TestCopyTreeCarriesAFileRootIntoTheWorkspace(t *testing.T) {
	src := t.TempDir()
	writeSrcFile(t, filepath.Join(src, "adlc.json"), `{"project":"adlc"}`)
	writeSrcFile(t, filepath.Join(src, "internal", "gate", "tree.go"), "package gate\n")

	dst := t.TempDir()
	if err := copyTree(src, dst, []string{"internal", "adlc.json"}); err != nil {
		t.Fatalf("copyTree over a root list containing a plain file: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "adlc.json"))
	if err != nil {
		t.Fatalf("the file root adlc.json never arrived in the workspace: %v", err)
	}
	if string(got) != `{"project":"adlc"}` {
		t.Fatalf("adlc.json arrived with the wrong contents: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "internal", "gate", "tree.go")); err != nil {
		t.Fatalf("naming a file root cost the directory roots their contents: %v", err)
	}
}

// The clean case for the same branch: the file arrived because it was DECLARED.
//
// Without this, the test above passes just as well over a copyTree that ignored
// its root list and copied the whole repository — which is the defect copyTree
// exists to avoid, since a whole-tree copy sweeps in vendored dependencies,
// build output and .git.
func TestCopyTreeLeavesAnUndeclaredFileBehind(t *testing.T) {
	src := t.TempDir()
	writeSrcFile(t, filepath.Join(src, "adlc.json"), `{"project":"adlc"}`)
	writeSrcFile(t, filepath.Join(src, ".gitattributes"), "*.go text eol=lf\n")

	dst := t.TempDir()
	if err := copyTree(src, dst, []string{"adlc.json"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dst, "adlc.json")); err != nil {
		t.Fatalf("the declared file root did not arrive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatalf("a file nobody declared as a root was copied anyway (err %v), so copying the declared roots is not what happened", err)
	}
}

// A declared root that is not there yet is skipped rather than refused.
//
// A config may name a root before the tree has one — site/ was declared in the
// commit that created it — and a dispatcher that returned an error there would
// refuse every run over an ordering nobody would connect to the message.
func TestCopyTreeSkipsARootThatDoesNotExist(t *testing.T) {
	src := t.TempDir()
	writeSrcFile(t, filepath.Join(src, "adlc.json"), "{}")

	dst := t.TempDir()
	if err := copyTree(src, dst, []string{"adlc.json", ".gitattributes"}); err != nil {
		t.Fatalf("a root that does not exist yet must be skipped, not refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "adlc.json")); err != nil {
		t.Fatalf("the root that does exist was skipped too: %v", err)
	}
}

func writeSrcFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// trunkRepo builds a repository with one commit on a branch named "main".
//
// The branch name is set explicitly rather than left to git's default, because
// the rule under test measures a branch against the configured trunk: on a host
// where `git init` still produces "master", a repository built with the default
// would leave every `main..branch` count unresolvable and the tests would agree
// with a guard that had stopped working. The temporary directory is resolved
// through any symlink for a related reason — git records a worktree's path
// literally, so a repository reached by one spelling and a worktree created
// under another are two paths git will not match.
func trunkRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if p, err := filepath.EvalSymlinks(dir); err == nil {
		dir = p
	}
	mustGit(t, dir, "init")
	mustGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	mustGit(t, dir, "config", "user.email", "t@example.com")
	mustGit(t, dir, "config", "user.name", "t")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	write(t, filepath.Join(dir, "trunk.txt"), "trunk\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "trunk")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
	return out
}

// leftBehindBranch reproduces what a finished run leaves in the repository: a
// commit on the run's branch and no worktree, because the workspace is torn
// down and the branch is what keeps the commit reachable.
func leftBehindBranch(t *testing.T, repo, branch, file string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "scratch")
	mustGit(t, repo, "worktree", "add", "-b", branch, wt, "HEAD")
	write(t, filepath.Join(wt, file), "work\n")
	mustGit(t, wt, "add", ".")
	mustGit(t, wt, "commit", "-m", "work on "+branch)
	sha := mustGit(t, wt, "rev-parse", "HEAD")
	mustGit(t, repo, "worktree", "remove", "--force", wt)
	return sha
}

// emptyBranch is a run that started and committed nothing: the branch exists
// and sits exactly where the trunk was when it was cut.
func emptyBranch(t *testing.T, repo, branch string) {
	t.Helper()
	mustGit(t, repo, "branch", branch, "HEAD")
}

func workspaceDispatcher(t *testing.T, repo string) *Dispatcher {
	t.Helper()
	cfg, err := config.FromChecks("t", []string{"."}, []config.Check{{
		ID: "noop", Command: []string{"go", "version"}, Verdict: config.VerdictExitZero,
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Dispatch.Isolation = "worktree"
	cfg.Dispatch.Trunk = "main"
	led, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { led.Close() })
	n := 0
	led.SetClock(func() time.Time { n++; return testNow.Add(time.Duration(n) * time.Second) })
	return &Dispatcher{
		Cfg: cfg, Led: led, Repo: repo, Actor: "test",
		Now: func() time.Time { return testNow },
	}
}

// pastRun records the run that owns a branch. The dispatcher has no other way
// to know whose work a branch is: the name carries a run id, and only the
// ledger says which item that run was on.
func pastRun(t *testing.T, d *Dispatcher, runID, itemID, branch string) {
	t.Helper()
	if _, err := d.Led.Append("test", ledger.KindRunStarted, runID, ledger.RunStarted{
		RunID: runID, WorkerType: "performer", ItemID: itemID, Branch: branch,
	}); err != nil {
		t.Fatal(err)
	}
}

// workspaceFor is the pair of steps dispatchOne takes: choose the base, then
// cut the tree from it. They are exercised together because either alone is
// vacuous — a correctly chosen base handed to a prepareWorkspace that ignores
// it still produces the trunk, which is the defect these tests exist for.
func workspaceFor(t *testing.T, d *Dispatcher, runID, itemID string) *Workspace {
	t.Helper()
	ws, err := d.prepareWorkspace(runID, d.workspaceBase(itemID), config.CapImplement)
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	t.Cleanup(ws.Cleanup)
	return ws
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	return mustGit(t, dir, "rev-parse", "HEAD")
}

// TestWorkspaceCarriesTheItemsUnmergedWork is the firing case: the role after
// the builder must be able to see what the builder wrote. Cut from the trunk, a
// tester or a judge is dispatched to examine a tree that does not contain the
// work it was sent to examine, and reports on the trunk believing it reviewed
// the change.
func TestWorkspaceCarriesTheItemsUnmergedWork(t *testing.T) {
	repo := trunkRepo(t)
	d := workspaceDispatcher(t, repo)

	sha := leftBehindBranch(t, repo, "adlc/e-builder-01", "built.txt")
	pastRun(t, d, "e-builder-01", "S1-021", "adlc/e-builder-01")

	ws := workspaceFor(t, d, "t-judge-01", "S1-021")

	if got := headOf(t, ws.Dir); got != sha {
		t.Errorf("workspace HEAD = %s, want the builder's commit %s", got, sha)
	}
	// The object store is shared between worktrees, so the commit merely
	// EXISTING here proves nothing — it existed under the old behaviour too.
	// What an agent reads is the checked-out tree.
	if _, err := os.Stat(filepath.Join(ws.Dir, "built.txt")); err != nil {
		t.Errorf("the builder's file is not in the workspace tree: %v", err)
	}
}

// TestWorkspaceIsCutFromTheTrunkUnlessThisItemIsAhead is the clean case. A rule
// that inherits a branch has to be capable of not inheriting one, or it stops
// being a rule about this item's unmerged work and becomes a rule about
// whichever branch the ledger happened to name last.
func TestWorkspaceIsCutFromTheTrunkUnlessThisItemIsAhead(t *testing.T) {
	t.Run("no branch ahead of the trunk", func(t *testing.T) {
		repo := trunkRepo(t)
		d := workspaceDispatcher(t, repo)

		// The trunk is moved on AFTER the branch is made, so the branch is left
		// strictly behind it. A branch sitting level with the trunk would assert
		// nothing: inheriting it and refusing it name the same commit, so the
		// check below would pass however the rule was written. Behind is also
		// the true shape of the case — it is what an item whose work has already
		// landed leaves in the repository, and cutting a rework run from that
		// tip would hide every sibling change that landed since.
		emptyBranch(t, repo, "adlc/e-quiet-01")
		pastRun(t, d, "e-quiet-01", "S1-021", "adlc/e-quiet-01")
		write(t, filepath.Join(repo, "later.txt"), "later\n")
		mustGit(t, repo, "add", ".")
		mustGit(t, repo, "commit", "-m", "trunk moves on")
		trunk := headOf(t, repo)

		ws := workspaceFor(t, d, "t-next-01", "S1-021")

		if got := headOf(t, ws.Dir); got != trunk {
			t.Errorf("workspace HEAD = %s, want the trunk %s (it was cut from a branch that is not ahead)", got, trunk)
		}
	})

	t.Run("another item's branch is never chosen", func(t *testing.T) {
		repo := trunkRepo(t)
		d := workspaceDispatcher(t, repo)
		trunk := headOf(t, repo)

		// This item has a run, so the lookup finds something, and its branch is
		// not ahead. Somebody else's item has a branch that IS ahead, and is
		// recorded later.
		emptyBranch(t, repo, "adlc/e-mine-01")
		pastRun(t, d, "e-mine-01", "S1-021", "adlc/e-mine-01")
		other := leftBehindBranch(t, repo, "adlc/e-theirs-01", "theirs.txt")
		pastRun(t, d, "e-theirs-01", "S1-999", "adlc/e-theirs-01")

		ws := workspaceFor(t, d, "t-next-02", "S1-021")

		if got := headOf(t, ws.Dir); got != trunk {
			t.Errorf("workspace HEAD = %s, want the trunk %s (it took S1-999's branch)", got, trunk)
		}
		if out, err := git(ws.Dir, "merge-base", "--is-ancestor", other, "HEAD"); err == nil {
			t.Errorf("workspace contains S1-999's commit %s: %s", other, out)
		}
	})
}

// Everybody builds the product; the improver judges the system that built it.
//
// Once a fleet is pointed at a separate product repository, an agent's tree
// contains the product and nothing else — which is the point, and is what stops
// a researcher handed a brief about cats spending its run on a Go control plane
// because that was what was in front of it.
//
// The improver is the one role that must not get that tree. Its job is to read
// what the fleet just did and ask what about ADLC made it harder than it needed
// to be: a refusal that did not say what to do, a prompt that pointed the wrong
// way, a check that failed for an unrelated reason. It cannot answer that from
// inside a tree that does not contain the thing it is judging, and it would
// answer it anyway — about the product — which is a lesson recorded against the
// wrong system.
func TestOnlyTheImproverWorksInTheControlPlanesOwnTree(t *testing.T) {
	d := &Dispatcher{
		Cfg:      &config.Config{Dispatch: config.DispatchPolicy{Isolation: "none"}},
		Repo:     filepath.FromSlash("/product"),
		ToolRepo: filepath.FromSlash("/toolchain"),
	}
	for _, cap := range []string{
		config.CapImplement, config.CapJudge, config.CapValidate,
		config.CapPlan, config.CapResearch, config.CapCurate, config.CapArbitrate,
	} {
		if got := d.repoFor(cap); got != d.Repo {
			t.Errorf("%s worked in %q; every role that builds the product must see the product tree and only that", cap, got)
		}
	}
	if got := d.repoFor(config.CapImprove); got != d.ToolRepo {
		t.Errorf("the improver worked in %q, want the control plane's own tree — it is judging this system, and cannot read what it is not given", got)
	}

	// The clean case, and the ordinary one: self-hosting, where there is no
	// second tree and the improver is not sent anywhere special.
	self := &Dispatcher{Cfg: d.Cfg, Repo: filepath.FromSlash("/only")}
	if got := self.repoFor(config.CapImprove); got != self.Repo {
		t.Errorf("with one tree the improver must use it, got %q", got)
	}
}
