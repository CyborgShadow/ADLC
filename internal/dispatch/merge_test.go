package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/authority"
	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// gitRepo turns the harness's directory into a repository with one commit and a
// branch carrying a change, and tells the ledger which branch an item's work is
// on — which is what the merge step looks up.
func gitRepo(t *testing.T, h *harness, itemID, branch string) {
	t.Helper()
	dir := h.D.Repo
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	git("config", "core.autocrlf", "false")
	write(t, filepath.Join(dir, "thing.txt"), "v1\n")
	git("add", "-A")
	git("commit", "-m", "base")

	git("checkout", "-q", "-b", branch)
	write(t, filepath.Join(dir, "thing.txt"), "v2\n")
	git("add", "-A")
	git("commit", "-m", "the work")
	git("checkout", "-q", "main")

	if _, err := h.Led.Append("t", ledger.KindRunStarted, "p-merge", ledger.RunStarted{
		RunID: "p-merge", WorkerType: "performer", ItemID: itemID, Branch: branch,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestTheMergeStepLandsClearedWorkAndSaysSo covers the whole control-plane step:
// it takes what is cleared, lands it, and records both halves of the move.
func TestTheMergeStepLandsClearedWorkAndSaysSo(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "ready_to_merge")
	gitRepo(t, h, "S1-001", "adlc/p-merge")
	h.Cfg.Dispatch.Trunk = "main"

	res, err := h.D.Merge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Landed) != 1 || res.Landed[0] != "S1-001" {
		t.Fatalf("the cleared item should have landed: %+v (idle: %q)", res, res.Idle)
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if authority.State(it.State) != authority.StateMerged {
		t.Fatalf("a landed item is merged, got %s", it.State)
	}
	b, err := os.ReadFile(filepath.Join(h.D.Repo, "thing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "v2\n" {
		t.Errorf("the trunk should carry the work, got %q", b)
	}
	// The pass through merging is recorded, not skipped: an item that appears to
	// jump from cleared to merged cannot be explained if the merge later needs
	// auditing.
	evs, err := h.Led.Events(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawMerging bool
	for _, e := range evs {
		if e.Kind == ledger.KindItemTransitioned && e.Subject == "S1-001" &&
			strings.Contains(string(e.Payload), `"to":"merging"`) {
			sawMerging = true
		}
	}
	if !sawMerging {
		t.Error("the item passing through merging must be on the record")
	}
}

// TestAnItemWithNoBranchIsHeldAndSaidSo covers the case the queue cannot
// attempt at all. It must not move the item and must not be silent — an item
// sitting in ready_to_merge with no explanation is indistinguishable from one
// nobody has got to.
func TestAnItemWithNoBranchIsHeldAndSaidSo(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "ready_to_merge")

	res, err := h.D.Merge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Landed) != 0 {
		t.Fatal("nothing can land when there is no branch to land")
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if authority.State(it.State) != authority.StateReadyToMerge {
		t.Errorf("the item should not have moved, got %s", it.State)
	}
	props, err := h.Led.Proposals("S1-001", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) == 0 {
		t.Fatal("the refusal must be recorded where every other refusal lives")
	}
	if !strings.Contains(props[0].Detail, "branch") {
		t.Errorf("the refusal should say what was missing: %q", props[0].Detail)
	}
}

func TestAnEmptyMergeQueueSaysWhyItDidNothing(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	res, err := h.D.Merge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Idle == "" {
		t.Fatal("an empty pass must explain itself; silence reads as a stopped queue")
	}
}

// A branch that cannot rebase goes back to a builder, not round the queue again.
//
// A rebase that conflicts against a fixed trunk conflicts identically every
// time it is tried, so requeuing it to ready_to_merge made the merge lane
// attempt the same impossible rebase on every tick, forever — while the item
// looked, on every surface, exactly like work nobody had got round to. The
// fleet must never get stuck, and an infinite retry is the shape a stall takes
// when nothing errors.
func TestABranchThatCannotRebaseGoesBackToABuilder(t *testing.T) {
	ws, routing := specialists()
	h := newHarness(t, ws, routing)
	h.segment(t, "S1", "seg", "", 0)
	h.item(t, "S1-001", "S1", "ui", "ready_to_merge")
	gitRepo(t, h, "S1-001", "adlc/p-merge")
	h.Cfg.Dispatch.Trunk = "main"

	// The trunk changes the same line the branch changed, so no rebase of that
	// branch onto this trunk can succeed.
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = h.D.Repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write(t, filepath.Join(h.D.Repo, "thing.txt"), "something else entirely\n")
	git("add", "-A")
	git("commit", "-m", "the trunk moved under it")

	res, err := h.D.Merge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Requeued) != 0 {
		t.Errorf("an impossible rebase was queued to be attempted again: %+v", res.Requeued)
	}
	if len(res.Returned) != 1 || res.Returned[0] != "S1-001" {
		t.Fatalf("the conflict should have gone back to a builder: %+v", res)
	}
	it, err := h.Led.Item("S1-001")
	if err != nil {
		t.Fatal(err)
	}
	if authority.State(it.State) != authority.StateInProgress {
		t.Fatalf("only a builder can resolve a conflict, got %s", it.State)
	}
}

// A merge gate that comes back RED names the check that failed.
//
// The refusal used to read "<edge> came back RED" and nothing else, and the
// merge lane appends no gate.observed row — so that one sentence was the whole
// record of the run. Recovering the cause meant a detached worktree at the
// trunk, the item's commits cherry-picked onto it and the gate run there by
// hand: work the gate had already done.
func TestAMergeGateThatComesBackRedNamesTheFailingCheck(t *testing.T) {
	// `go version` prints a line and exits 0, so one command is RED under "the
	// output is the verdict" and GREEN under exit-zero — on any host that can
	// run this suite at all.
	land := func(t *testing.T, rule config.VerdictRule) (MergeResult, []ledger.Proposal) {
		t.Helper()
		ws, routing := specialists()
		h := newHarness(t, ws, routing)
		h.segment(t, "S1", "seg", "", 0)
		h.item(t, "S1-001", "S1", "ui", "ready_to_merge")
		gitRepo(t, h, "S1-001", "adlc/p-merge")
		h.Cfg.Dispatch.Trunk = "main"
		h.Cfg.Checks = []config.Check{{
			ID: "fmt", Command: []string{"go", "version"}, Verdict: rule,
			RequiredFor: []string{"merging->merged"},
		}}
		res, err := h.D.Merge(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		props, err := h.Led.Proposals("S1-001", true, 0)
		if err != nil {
			t.Fatal(err)
		}
		return res, props
	}

	// Firing case: one declared check is RED on the rebased tree.
	res, props := land(t, config.VerdictOutputEmpty)
	if len(res.Landed) != 0 {
		t.Fatalf("a red gate must not land anything: %+v", res)
	}
	var refusal string
	for _, p := range props {
		if p.Reason == string(authority.ReasonMergeGateFailed) {
			refusal = p.Detail
		}
	}
	if refusal == "" {
		t.Fatalf("no merge_gate_failed refusal was recorded: %+v", props)
	}
	if !strings.Contains(refusal, "fmt") {
		t.Errorf("the refusal does not name the check that failed, so it is the whole record of nothing: %q", refusal)
	}
	if !strings.Contains(refusal, "printed") {
		t.Errorf("the refusal does not say what the check observed: %q", refusal)
	}

	// Clean case: the same tree, the same command, a rule it satisfies. Without
	// it the assertions above would pass just as well the day the gate started
	// refusing everything.
	green, clean := land(t, config.VerdictExitZero)
	if len(green.Landed) != 1 || green.Landed[0] != "S1-001" {
		t.Fatalf("a green gate must land the work: %+v (idle %q)", green, green.Idle)
	}
	for _, p := range clean {
		if p.Reason == string(authority.ReasonMergeGateFailed) {
			t.Errorf("a green gate recorded a refusal anyway: %q", p.Detail)
		}
	}
}
