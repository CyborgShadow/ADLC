package dispatch

import (
	"os"
	"path/filepath"
	"testing"
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
