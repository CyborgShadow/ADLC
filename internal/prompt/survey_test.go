package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

func workers(pairs ...string) []config.WorkerDecl {
	out := make([]config.WorkerDecl, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, config.WorkerDecl{Type: pairs[i], Prompt: pairs[i+1]})
	}
	return out
}

// TestASurveyOfAProjectWithNoPromptsStillAnswers is the case `adlc config init`
// leaves behind: it tells you to write the prompts and that `prompt list` says
// which are missing. Loading the library refuses that project over the absent
// preamble, so the enumeration cannot come from a load.
func TestASurveyOfAProjectWithNoPromptsStillAnswers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	pol := config.PromptPolicy{Dir: dir, PreambleFile: filepath.Join(dir, "_preamble.md")}

	inv := Survey(pol, workers("engineer", "implementer", "tester", "tester"))
	if inv.DirErr == "" {
		t.Error("a directory that is not there should be reported, not hidden")
	}
	if len(inv.Prompts) != 2 {
		t.Fatalf("both referenced prompts must be listed, got %+v", inv.Prompts)
	}
	for _, p := range inv.Prompts {
		if !p.Missing() {
			t.Errorf("%s is not on disk and must read as missing", p.ID)
		}
	}
	// Two prompts and the preamble. The preamble is one missing thing among the
	// others here rather than the error that ends the command.
	if inv.Missing != 3 {
		t.Errorf("missing count: %d, want 3 (two prompts and the preamble)", inv.Missing)
	}
	if inv.PreamblePresent {
		t.Error("there is no preamble file")
	}
}

func TestASurveySeparatesWhatIsThereFromWhatIsNamed(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("_preamble.md", preamble)
	write("builder.md", builder)
	write("spare.md", "an old prompt nothing points at\n")

	pol := config.PromptPolicy{Dir: dir, PreambleFile: filepath.Join(dir, "_preamble.md")}
	inv := Survey(pol, workers("engineer", "builder", "researcher", "ghost-prompt"))

	byID := map[string]Status{}
	for _, p := range inv.Prompts {
		byID[p.ID] = p
	}
	if !byID["builder"].Present || byID["builder"].Missing() {
		t.Error("builder.md is on disk and named by a role")
	}
	if got := byID["builder"].Roles; len(got) != 1 || got[0] != "engineer" {
		t.Errorf("the role that names a prompt should be reported with it, got %v", got)
	}
	if !byID["ghost-prompt"].Missing() {
		t.Error("a role naming a prompt that is not there is the failure this reports")
	}
	// A file nothing points at is dead weight, not a broken dispatch, so it is
	// listed and not counted.
	if !byID["spare"].Present || byID["spare"].Missing() || len(byID["spare"].Roles) != 0 {
		t.Errorf("an unreferenced prompt should be listed with no role: %+v", byID["spare"])
	}
	if inv.Missing != 1 {
		t.Errorf("missing count: %d, want 1", inv.Missing)
	}
	if _, listed := byID["_preamble"]; listed {
		t.Error("the preamble is fleet policy and is reported on its own, not as an unused role prompt")
	}
	if !inv.PreamblePresent {
		t.Error("the preamble is there")
	}
}

// TestALibraryNamesTheRolesItCannotDispatch is the check `adlc prompt check`
// was missing. A role whose prompt file does not exist is the same failure as
// an area with no owner: work is filed and nothing ever picks it up.
func TestALibraryNamesTheRolesItCannotDispatch(t *testing.T) {
	lib := library(t, map[string]string{"_preamble.md": preamble, "builder.md": builder})

	absent := lib.MissingFor(workers("engineer", "builder", "researcher", "ghost-prompt", "docs-writer", "ghost-prompt"))
	if len(absent) != 1 {
		t.Fatalf("one prompt is absent, got %+v", absent)
	}
	if absent[0].ID != "ghost-prompt" {
		t.Fatalf("wrong prompt named: %q", absent[0].ID)
	}
	if len(absent[0].Roles) != 2 {
		t.Errorf("every role that cannot be dispatched should be named, got %v", absent[0].Roles)
	}
	if len(lib.MissingFor(workers("engineer", "builder"))) != 0 {
		t.Error("a library that holds every named prompt has nothing to report")
	}
}
