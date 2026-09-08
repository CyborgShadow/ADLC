package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

func promptEnv(t *testing.T, dir string, roles ...string) *env {
	t.Helper()
	ws := make([]config.WorkerDecl, 0, len(roles))
	for _, r := range roles {
		ws = append(ws, config.WorkerDecl{Type: r, Prompt: r})
	}
	return &env{cfg: &config.Config{
		Project: "t",
		Prompts: config.PromptPolicy{Dir: dir, PreambleFile: filepath.Join(dir, "_preamble.md")},
		Workers: ws,
	}}
}

// TestPromptListAnswersOnAProjectWithNoPromptsYet is the promise `adlc config
// init` makes on its last line. It used to be a lie: the command refused over
// the absent preamble and named nothing at all.
func TestPromptListAnswersOnAProjectWithNoPromptsYet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	if code := promptList(promptEnv(t, dir, "engineer", "tester")); code == exitOK {
		t.Fatal("every role's prompt is missing, so this must not exit 0")
	}
}

func TestPromptListExitsNonZeroWhileAnyReferencedPromptIsAbsent(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("---\nid: "+name[:len(name)-3]+"\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("_preamble.md")
	write("engineer.md")

	e := promptEnv(t, dir, "engineer", "tester")
	if code := promptList(e); code == exitOK {
		t.Fatal("tester's prompt is not there, and a role that cannot be dispatched is not a clean result")
	}
	write("tester.md")
	if code := promptList(e); code != exitOK {
		t.Fatalf("every referenced prompt is present now, got exit %d", code)
	}
}
