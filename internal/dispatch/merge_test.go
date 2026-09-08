package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/authority"
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
