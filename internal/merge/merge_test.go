package merge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo builds a git repository with one commit on the trunk, so every test
// starts from the same shape and a failure names the rule rather than the
// fixture.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-b", "main")
	run(t, dir, "config", "user.email", "t@example.invalid")
	run(t, dir, "config", "user.name", "t")
	run(t, dir, "config", "core.autocrlf", "false")
	write(t, dir, "alpha.txt", "alpha v1\n")
	write(t, dir, "beta.txt", "beta v1\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", "base")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// branch cuts a branch from a named starting point, changes files on it, and
// leaves the repository back on the trunk.
func branch(t *testing.T, dir, name, from string, files map[string]string) {
	t.Helper()
	run(t, dir, "checkout", "-q", "-b", name, from)
	for f, body := range files {
		write(t, dir, f, body)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", name)
	run(t, dir, "checkout", "-q", "main")
}

func queue(t *testing.T, dir string) *Queue {
	t.Helper()
	return &Queue{Repo: dir, Trunk: "main", LockDir: t.TempDir(), Log: func(string) {}}
}

func TestACleanBranchFastForwards(t *testing.T) {
	dir := repo(t)
	branch(t, dir, "work", "main", map[string]string{"alpha.txt": "alpha v2\n"})

	out, err := queue(t, dir).Land(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Landed {
		t.Fatalf("a clean branch should land: [%s] %s", out.Reason, out.Detail)
	}
	if out.MergedSHA == "" {
		t.Error("a landed branch must report the trunk tip it produced")
	}
	if got := readFile(t, dir, "alpha.txt"); got != "alpha v2\n" {
		t.Errorf("the trunk should carry the branch's change, got %q", got)
	}
}

// TestTheClobberGuardNamesWhatWouldBeReverted is the reason this package
// exists.
//
// A clean rebase always produces exactly the branch's own patch, so on the
// happy path the guard finds nothing — that is what makes it an assertion
// rather than a heuristic. It earns its place on the paths where a rebase
// produces something else: a commit dropped as already-upstream, a conflict
// resolved by a merge driver or a recorded resolution, a flattened merge
// commit. Each of those reverts work that landed after the branch started, and
// every check stays green on the way through, so the comparison is made
// directly rather than inferred from a check result.
func TestTheClobberGuardNamesWhatWouldBeReverted(t *testing.T) {
	owned := map[string]bool{"internal/api/handler.go": true, "internal/api/handler_test.go": true}

	if got := Unowned(owned, []string{"internal/api/handler.go", "internal/api/handler_test.go"}); len(got) != 0 {
		t.Fatalf("a branch changing only what it touched is clean, got %v", got)
	}
	got := Unowned(owned, []string{
		"internal/api/handler.go",
		"internal/store/schema.sql", // a sibling landed this; the rebase would undo it
		"README.md",
	})
	if len(got) != 2 {
		t.Fatalf("both unowned paths must be reported, got %v", got)
	}
	if got[0] != "README.md" || got[1] != "internal/store/schema.sql" {
		t.Errorf("the paths are sorted so a refusal reads the same twice, got %v", got)
	}
	if len(Unowned(map[string]bool{}, nil)) != 0 {
		t.Error("no changes means nothing to refuse")
	}
}

// TestARefusalToClobberLeavesTheTrunkAlone drives the guard through Land, so
// the refusal path is exercised end to end rather than in isolation.
func TestARefusalToClobberLeavesTheTrunkAlone(t *testing.T) {
	dir := repo(t)
	branch(t, dir, "work", "main", map[string]string{"alpha.txt": "alpha v2\n"})

	q := queue(t, dir)
	// Stand in for the mechanisms above: the rebased tree is reported as
	// changing a file the branch never touched.
	q.Gate = func(context.Context, string) (bool, string, error) {
		t.Error("the gate must not run once the clobber guard has refused")
		return true, "", nil
	}
	q.diffOverride = func(string, string, string) ([]string, error) {
		return []string{"alpha.txt", "beta.txt"}, nil
	}
	out, err := q.Land(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Landed || out.Reason != "stale_base_clobber" {
		t.Fatalf("want stale_base_clobber, got landed=%v [%s] %s", out.Landed, out.Reason, out.Detail)
	}
	if !contains(out.Clobbered, "beta.txt") {
		t.Errorf("the refusal must name the file it would revert, got %v", out.Clobbered)
	}
	if !strings.Contains(out.Detail, "revert") {
		t.Errorf("the refusal should say what would have happened: %q", out.Detail)
	}
	if out.Retryable {
		t.Error("the same rebase reverts the same work every time; retrying it is not a fix")
	}
	if got := readFile(t, dir, "alpha.txt"); got != "alpha v1\n" {
		t.Errorf("nothing should have landed, got %q", got)
	}
}

// TestTwoBranchesOnDisjointFilesBothLand is the clean case for the same guard:
// the second branch rebases over the first, changes only what it owns, and
// lands. Without this, a guard that refused everything would look correct.
func TestTwoBranchesOnDisjointFilesBothLand(t *testing.T) {
	dir := repo(t)
	base := run(t, dir, "rev-parse", "HEAD")
	branch(t, dir, "first", base, map[string]string{"beta.txt": "beta v2\n"})
	branch(t, dir, "second", base, map[string]string{"alpha.txt": "alpha v2\n"})

	q := queue(t, dir)
	for _, b := range []string{"first", "second"} {
		out, err := q.Land(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		if !out.Landed {
			t.Fatalf("%s touches nothing the other does and should land: [%s] %s", b, out.Reason, out.Detail)
		}
	}
	if got := readFile(t, dir, "beta.txt"); got != "beta v2\n" {
		t.Errorf("the first branch's work was lost, got %q", got)
	}
	if got := readFile(t, dir, "alpha.txt"); got != "alpha v2\n" {
		t.Errorf("the second branch's work was lost, got %q", got)
	}
}

func TestABranchThatDoesNotRebaseIsNamedAConflict(t *testing.T) {
	dir := repo(t)
	base := run(t, dir, "rev-parse", "HEAD")
	branch(t, dir, "first", base, map[string]string{"alpha.txt": "alpha from first\n"})
	branch(t, dir, "second", base, map[string]string{"alpha.txt": "alpha from second\n"})

	q := queue(t, dir)
	if out, _ := q.Land(context.Background(), "first"); !out.Landed {
		t.Fatal("the first branch should land")
	}
	out, err := q.Land(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != "merge_conflict" {
		t.Fatalf("want merge_conflict, got [%s] %s", out.Reason, out.Detail)
	}
	if !out.Retryable {
		t.Error("a conflict is worth another attempt once the branch is rebased by hand")
	}
}

// TestAGateThatFailsOnTheRebasedTreeStopsTheMerge pins the second half of the
// rebase problem: a branch that was green before the rebase is not necessarily
// green after it, and the tree that lands is the rebased one.
func TestAGateThatFailsOnTheRebasedTreeStopsTheMerge(t *testing.T) {
	dir := repo(t)
	branch(t, dir, "work", "main", map[string]string{"alpha.txt": "alpha v2\n"})

	q := queue(t, dir)
	var gatedDir string
	q.Gate = func(_ context.Context, d string) (bool, string, error) {
		gatedDir = d
		return false, "one check came back red", nil
	}
	out, err := q.Land(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Landed || out.Reason != "merge_gate_failed" {
		t.Fatalf("a red gate must stop the merge, got landed=%v [%s]", out.Landed, out.Reason)
	}
	if gatedDir == dir {
		t.Error("the gate must run on the rebased scratch tree, not on the repository itself")
	}
	if got := readFile(t, dir, "alpha.txt"); got != "alpha v1\n" {
		t.Errorf("nothing should have landed, got %q", got)
	}
}

// TestTheTipMovingDuringTheGateAbandonsTheAttempt covers the window the lock
// deliberately does not cover. The gate runs outside the lock so one execution
// cannot throttle the fleet; the price is that the trunk can move underneath
// it, and what was gated is then not what would land.
func TestTheTipMovingDuringTheGateAbandonsTheAttempt(t *testing.T) {
	dir := repo(t)
	base := run(t, dir, "rev-parse", "HEAD")
	branch(t, dir, "work", base, map[string]string{"alpha.txt": "alpha v2\n"})
	branch(t, dir, "sibling", base, map[string]string{"gamma.txt": "gamma v1\n"})

	q := queue(t, dir)
	q.Gate = func(context.Context, string) (bool, string, error) {
		// Something else lands while this gate is running.
		run(t, dir, "merge", "--ff-only", "sibling")
		return true, "green", nil
	}
	out, err := q.Land(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Landed || out.Reason != "trunk_moved" {
		t.Fatalf("want trunk_moved, got landed=%v [%s] %s", out.Landed, out.Reason, out.Detail)
	}
	if !out.Retryable {
		t.Error("the branch is fine; it just needs another pass, so this must be retryable")
	}
}

func TestABranchWithNothingOnItIsNotAMerge(t *testing.T) {
	dir := repo(t)
	run(t, dir, "branch", "empty", "main")
	out, err := queue(t, dir).Land(context.Background(), "empty")
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != "nothing_to_land" {
		t.Fatalf("want nothing_to_land, got [%s] %s", out.Reason, out.Detail)
	}
}

func TestALockHeldByALiveHolderIsNotStolen(t *testing.T) {
	dir := repo(t)
	branch(t, dir, "work", "main", map[string]string{"alpha.txt": "alpha v2\n"})
	q := queue(t, dir)

	release, err := q.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out, err := q.Land(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Landed || out.Reason != "lock_busy" {
		t.Fatalf("a held lock must stop a second merger, got landed=%v [%s]", out.Landed, out.Reason)
	}
	if !out.Retryable {
		t.Error("waiting for a lock is the definition of retryable")
	}

	// Clean case: once released, the same branch lands.
	release()
	if out2, err := q.Land(context.Background(), "work"); err != nil || !out2.Landed {
		t.Fatalf("after the lock is released it should land: %v [%s] %s", err, out2.Reason, out2.Detail)
	}
}

func TestLandingNothingIsAnErrorNotASilentNoOp(t *testing.T) {
	if _, err := queue(t, repo(t)).Land(context.Background(), "  "); err == nil {
		t.Fatal("an empty branch name is a caller defect and must be reported as one")
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contains(v []string, want string) bool {
	for _, x := range v {
		if x == want {
			return true
		}
	}
	return false
}
