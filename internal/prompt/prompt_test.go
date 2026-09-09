package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

func library(t *testing.T, files map[string]string) *Library {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pol := config.PromptPolicy{Dir: dir}
	if _, ok := files["_preamble.md"]; ok {
		pol.PreambleFile = filepath.Join(dir, "_preamble.md")
	}
	lib, err := Load(pol)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return lib
}

const preamble = "---\nid: _preamble\n---\nFLEET POLICY. You never write the ledger.\n"
const builder = "---\nid: builder\nversion: v2\n---\n# Builder\n\nWork on {{work_item_id}} in {{workdir}}.\n"

func TestAPromptIsReadWithItsFrontMatter(t *testing.T) {
	lib := library(t, map[string]string{"builder.md": builder})
	p, err := lib.Get("builder")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "builder" || p.Version != "v2" {
		t.Fatalf("front matter not parsed: %+v", p.Front)
	}
	if strings.Contains(p.Body, "---") {
		t.Error("the front matter should not survive into the body")
	}
	if !strings.HasPrefix(p.Body, "# Builder") {
		t.Errorf("body starts wrong: %q", p.Body[:20])
	}
}

func TestAFileWithNoFrontMatterStillLoads(t *testing.T) {
	lib := library(t, map[string]string{"plain.md": "just some instructions\n"})
	p, err := lib.Get("plain")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "v1" {
		t.Errorf("an unversioned prompt defaults to v1, got %q", p.Version)
	}
}

func TestTwoPromptsCannotShareAnId(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("---\nid: same\n---\nx\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(config.PromptPolicy{Dir: dir}); err == nil {
		t.Fatal("a pin that resolves to two files resolves to neither, so this must be refused")
	}
}

// TestThePreambleIsAssembledAboveEveryRole pins the single edit point for
// fleet-wide policy.
func TestThePreambleIsAssembledAboveEveryRole(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})
	a, err := lib.Assemble("builder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Text, "FLEET POLICY") {
		t.Fatal("the preamble did not reach the role")
	}
	if !strings.Contains(a.Text, "# Builder") {
		t.Fatal("the role text is missing")
	}
	if strings.Index(a.Text, "FLEET POLICY") > strings.Index(a.Text, "# Builder") {
		t.Fatal("the preamble comes first — a role is written to be read starting at the seam")
	}
	if !strings.Contains(a.Text, "\n---\n") {
		t.Error("the seam between policy and role should be visible")
	}
}

func TestVariablesAreSubstituted(t *testing.T) {
	lib := library(t, map[string]string{"builder.md": builder})
	a, err := lib.Assemble("builder", map[string]string{
		"work_item_id": "S1-001", "workdir": "/tmp/run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Text, "S1-001") || !strings.Contains(a.Text, "/tmp/run") {
		t.Fatalf("substitution failed: %q", a.Text)
	}
	if strings.Contains(a.Text, "{{") {
		t.Error("an unfilled placeholder would reach an agent as literal template text")
	}
}

// TestTheDigestAddressesExactlyTheBytesThatAreStored pins the content-address
// contract: the digest recorded on a run is the key its retained prompt is
// filed under, so the two must hash identical bytes.
func TestTheDigestAddressesExactlyTheBytesThatAreStored(t *testing.T) {
	lib := library(t, map[string]string{"builder.md": builder})
	a, err := lib.Assemble("builder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.SHA) != 64 {
		t.Fatalf("the digest doubles as a content address and must not be truncated; got %d chars", len(a.SHA))
	}
	if digest(string(a.Bytes())) != a.SHA {
		t.Fatal("Bytes() must hash to SHA, or a retained prompt is filed under an address nothing will look up")
	}

	// Line endings are normalised on both sides, so a checkout that converts
	// them does not change a prompt's identity.
	crlf := Assembled{Text: strings.ReplaceAll(a.Text, "\n", "\r\n")}
	if string(crlf.Bytes()) != string(a.Bytes()) {
		t.Fatal("CRLF and LF forms of one prompt must produce identical bytes")
	}
	if digest(crlf.Text) != a.SHA {
		t.Fatal("CRLF and LF forms of one prompt must produce identical digests")
	}
}

func TestAssemblyIsStable(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})
	first, _ := lib.Assemble("builder", map[string]string{"work_item_id": "S1-001"})
	for i := 0; i < 20; i++ {
		again, _ := lib.Assemble("builder", map[string]string{"work_item_id": "S1-001"})
		if again.SHA != first.SHA {
			t.Fatalf("the same inputs produced a different prompt on attempt %d — a pin must stay resolvable", i)
		}
	}
}

// TestAMissingClauseIsReported pins the rule that a run cannot delete a safety
// clause from its own instructions.
func TestAMissingClauseIsReported(t *testing.T) {
	clauses := []string{"You never write the ledger.", "You never mark your own work done."}

	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})
	findings := lib.CheckClauses(clauses)
	// The clause is missing from the preamble, so every prompt in the library
	// is short one — the report is per prompt, because the fix might be in any
	// of them.
	if len(findings) != len(lib.IDs()) {
		t.Fatalf("every prompt should be reported, got %d findings for %d prompts", len(findings), len(lib.IDs()))
	}
	for _, f := range findings {
		if !strings.Contains(f.Missing, "mark your own work done") {
			t.Errorf("the finding should name the missing clause, got %q", f.Missing)
		}
		if f.Path == "" {
			t.Error("the finding should name the file to edit")
		}
	}

	// Clean case: a preamble carrying both satisfies every role at once, so
	// this check cannot be passing vacuously.
	full := preamble + "You never mark your own work done.\n"
	lib2 := library(t, map[string]string{"_preamble.md": full, "builder.md": builder})
	if got := lib2.CheckClauses(clauses); len(got) != 0 {
		t.Fatalf("a complete preamble should satisfy every role, got %+v", got)
	}
}

func TestNoDeclaredClausesMeansNoFindings(t *testing.T) {
	lib := library(t, map[string]string{"builder.md": builder})
	if got := lib.CheckClauses(nil); got != nil {
		t.Fatalf("a project declaring no mandatory clauses has nothing to fail, got %+v", got)
	}
}

func TestABomDoesNotChangeAPromptsIdentity(t *testing.T) {
	withBOM := string([]byte{0xEF, 0xBB, 0xBF}) + builder
	a := library(t, map[string]string{"builder.md": builder})
	b := library(t, map[string]string{"builder.md": withBOM})
	pa, _ := a.Get("builder")
	pb, _ := b.Get("builder")
	if pa.SHA() != pb.SHA() {
		t.Fatal("a byte-order mark must not change a prompt's digest, or the pin breaks on a Windows edit")
	}
}

// thisProjectsLibrary loads the real prompt library this repository dispatches
// from, rather than a fixture. The paths in adlc.json are relative to the repo
// root because that is where the CLI runs, so a test two packages down has to
// rejoin them itself.
func thisProjectsLibrary(t *testing.T) *Library {
	t.Helper()
	cfg, err := config.Load("../../adlc.json")
	if err != nil {
		t.Skipf("this project's config is not readable from here: %v", err)
	}
	pol := cfg.Prompts
	if pol.Dir == "" {
		t.Fatal("this project declares no prompt directory, so there is no library to check")
	}
	pol.Dir = filepath.Join("../..", pol.Dir)
	if pol.PreambleFile != "" {
		pol.PreambleFile = filepath.Join("../..", pol.PreambleFile)
	}
	lib, err := Load(pol)
	if err != nil {
		t.Fatalf("this project's own prompt library does not load: %v", err)
	}
	return lib
}

// The planner is told that `go run` cannot express an exit code.
//
// `go run` returns 1 for any non-zero exit of the program it ran and reports
// the real code only as text on stderr, so a planner that writes
// `exit_in: [3]` against a `go run` command produces a criterion that passes on
// a code nobody checked. The warning is only useful beside the rule vocabulary
// that makes the trap reachable, so both halves are pinned here: remove either
// and the prompt stops carrying a complete instruction.
func TestThePlannerIsWarnedThatGoRunCannotExpressAnExitCode(t *testing.T) {
	lib := thisProjectsLibrary(t)
	a, err := lib.Assemble("planner", nil)
	if err != nil {
		t.Fatalf("assemble planner: %v", err)
	}
	if !strings.Contains(a.Text, "exit_in") {
		t.Fatal("the planner prompt no longer names the exit_in rule, so the go run warning has nothing to attach to")
	}
	if !strings.Contains(a.Text, "go build -o") {
		t.Fatal("the planner prompt does not say to build a binary, so a criterion pinning an exit code other than 0 or 1 will pass on a code nobody checked")
	}
}

// And the warning lives in the planner's own file, not the shared preamble.
//
// The clean case for the guard above: `strings.Contains` over an assembled
// prompt would be satisfied just as well by a clause copied into the preamble,
// which every role then carries. That is the duplication the seam exists to
// prevent, and it would make the guard above pass for a reason that has nothing
// to do with the planner.
func TestTheGoRunWarningIsNotFleetWidePolicy(t *testing.T) {
	lib := thisProjectsLibrary(t)
	if strings.Contains(lib.PreambleText(), "go build -o") {
		t.Fatal("the go run warning is in the shared preamble, so every role restates a rule only the planner acts on")
	}
	other, err := lib.Assemble("tester", nil)
	if err != nil {
		t.Fatalf("assemble tester: %v", err)
	}
	if strings.Contains(other.Text, "go build -o") {
		t.Fatal("a role that writes no acceptance criteria carries the planner's exit-code warning, so the guard above proves nothing about the planner")
	}
}
func TestAPromptEditedOnDiskReachesTheNextAssembly(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})

	// Merged, not written through SetPrompt: the change arrives as a checkout
	// of somebody else's commit, which is the case that was broken.
	merged := "---\nid: builder\nversion: v2\n---\n# Builder\n\nMERGED INSTRUCTION for {{work_item_id}}.\n"
	if err := os.WriteFile(filepath.Join(lib.Dir, "builder.md"), []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := lib.Get("builder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Body, "MERGED INSTRUCTION") {
		t.Errorf("Get served the body loaded at startup, not the file: %q", p.Body)
	}

	asm, err := lib.Assemble("builder", map[string]string{"work_item_id": "S1-032"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asm.Text, "MERGED INSTRUCTION for S1-032.") {
		t.Errorf("Assemble served the body loaded at startup, not the file: %q", asm.Text)
	}
	if !strings.Contains(asm.Text, "FLEET POLICY") {
		t.Error("the shared preamble stopped being assembled above the role")
	}

	// The preamble is one edit that reaches every role, so it is refreshed the
	// same way.
	if err := os.WriteFile(filepath.Join(lib.Dir, "_preamble.md"),
		[]byte("---\nid: _preamble\n---\nMERGED POLICY. You never write the ledger.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asm, err = lib.Assemble("builder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asm.Text, "MERGED POLICY") {
		t.Errorf("a merged preamble change did not reach the assembly: %q", asm.Text)
	}
	if lib.PreambleSHA() == digest(preamble) {
		t.Error("PreambleSHA still addresses the bytes loaded at startup")
	}
}

// The clean case. Rereading must not invent a change: an untouched file serves
// the body and the digest it always did, or every surface that compares a
// prompt pin with the library reports drift on a tree nobody edited.
func TestAnUnchangedFileStillServesItsOriginalBody(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})
	first, err := lib.Assemble("builder", map[string]string{"work_item_id": "S1-032", "workdir": "w"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := lib.Get("builder")
	if err != nil {
		t.Fatal(err)
	}
	again, err := lib.Assemble("builder", map[string]string{"work_item_id": "S1-032", "workdir": "w"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Text != first.Text || again.SHA != first.SHA {
		t.Fatalf("an unchanged file assembled differently the second time:\n%q\n%q", first.Text, again.Text)
	}
	if !strings.HasPrefix(p.Body, "# Builder") || p.Version != "v2" {
		t.Fatalf("an unchanged file no longer reads as itself: %+v", p)
	}
}

// A prompt file that is unreadable — a checkout is mid-flight, or somebody has
// moved it — keeps the copy already loaded. The alternative trades a slightly
// stale prompt for no prompt at all, which stops the fleet dispatching for the
// length of a git operation.
func TestAnUnreadablePromptKeepsTheLoadedCopy(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})
	if err := os.Remove(filepath.Join(lib.Dir, "builder.md")); err != nil {
		t.Fatal(err)
	}
	asm, err := lib.Assemble("builder", map[string]string{"work_item_id": "S1-032"})
	if err != nil {
		t.Fatalf("a vanished file should not stop a dispatch: %v", err)
	}
	if !strings.Contains(asm.Text, "# Builder") {
		t.Errorf("the loaded copy was not kept: %q", asm.Text)
	}
}

// A role prompt caught mid-write must not reach a dispatch. Re-reading at
// dispatch means the library now trusts a writer that gives it none of the
// guarantees writeFile does: a `git merge` rewriting the prompt directory
// truncates the file before it fills it, and a truncated read that succeeds is
// not an error to fall through on. Taking it dispatches an agent with the
// preamble and no job — what SetPrompt refuses in those words — and reverts the
// version the run is recorded under.
func TestAPromptCaughtMidWriteKeepsTheLoadedCopy(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})
	path := filepath.Join(lib.Dir, "builder.md")

	// The truncate-then-write window: the file exists and reads as nothing.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	asm, err := lib.Assemble("builder", map[string]string{"work_item_id": "S1-032", "workdir": "w"})
	if err != nil {
		t.Fatalf("a truncated file should not stop a dispatch: %v", err)
	}
	if !strings.Contains(asm.Text, "# Builder") {
		t.Errorf("a run was dispatched with the preamble and no job: %q", asm.Text)
	}
	if asm.Version != "v2" {
		t.Errorf("the version the run is recorded under reverted to %q", asm.Version)
	}

	// A fence opened and not yet closed: readPrompt cannot parse front matter
	// out of it and hands the whole fragment back as the body.
	if err := os.WriteFile(path, []byte("---\nid: bui"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := lib.Get("builder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Body, "# Builder") {
		t.Errorf("half a file was served as the role's instructions: %q", p.Body)
	}

	// The clean case. The guard must not refuse every fresh read, or the
	// staleness it sits inside comes straight back and no test says so.
	merged := "---\nid: builder\nversion: v3\n---\n# Builder\n\nMERGED INSTRUCTION for {{work_item_id}}.\n"
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}
	asm, err = lib.Assemble("builder", map[string]string{"work_item_id": "S1-032"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asm.Text, "MERGED INSTRUCTION for S1-032.") || asm.Version != "v3" {
		t.Errorf("a whole merged prompt was refused as if it were mid-write: %q %q", asm.Version, asm.Text)
	}
}

// The preamble caught mid-write is the same window with a wider blast radius:
// an empty read taken means Assemble drops the preamble and the seam with it,
// and the dispatched text then carries none of the mandatory clauses a run must
// not delete from its own instructions — with nothing on any surface saying so.
func TestAPreambleCaughtMidWriteKeepsTheLoadedCopy(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})
	path := filepath.Join(lib.Dir, "_preamble.md")

	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	asm, err := lib.Assemble("builder", map[string]string{"work_item_id": "S1-032", "workdir": "w"})
	if err != nil {
		t.Fatalf("a truncated preamble should not stop a dispatch: %v", err)
	}
	if !strings.Contains(asm.Text, "You never write the ledger.") {
		t.Errorf("a run was dispatched with no fleet policy and no mandatory clause: %q", asm.Text)
	}
	if !strings.Contains(asm.Text, "\n---\n") {
		t.Errorf("the seam between fleet policy and the role went with it: %q", asm.Text)
	}
	if lib.PreambleText() == "" || lib.PreambleSHA() != digest(preamble) {
		t.Error("the loaded preamble was replaced by the truncated read")
	}
	if got := lib.CheckClauses([]string{"You never write the ledger."}); len(got) != 0 {
		t.Errorf("the clause check lost the clause the loaded copy still carries: %+v", got)
	}

	// The clean case: a whole merged preamble still reaches the next assembly.
	merged := "---\nid: _preamble\n---\nMERGED POLICY. You never write the ledger.\n"
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}
	asm, err = lib.Assemble("builder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asm.Text, "MERGED POLICY") {
		t.Errorf("a whole merged preamble was refused as if it were mid-write: %q", asm.Text)
	}
}
