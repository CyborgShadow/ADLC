package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doubleEncoded is the signature of text that was read as bytes and written
// back re-encoded: the UTF-8 form of "Ã¢" (0xC3 0xA2) followed by the UTF-8
// form of a C1 control (0xC2 0x80-0x9F). Real prose in this repository never
// contains that pair.
var doubleEncoded = []string{
	"\xc3\xa2\xc2\x80", // an em-dash, en-dash or curly quote, mangled
	"\xc3\xa2\xc2\x86", // an arrow, mangled
}

// No source file carries a double-encoded character.
//
// An editing pass that read UTF-8 as bytes and wrote it back re-encoded turned
// every em-dash in two files into three replacement blocks, and it reached the
// dashboard: "Deliverable S1 <blocks> Why we love cats" on a page a person
// reads. Nothing failed and nothing was logged — the bytes are still valid
// UTF-8, they simply mean something else now, so no compiler and no test had
// any reason to object.
func TestNoSourceFileHasDoubleEncodedText(t *testing.T) {
	bad := map[string]int{}
	checked := 0
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Never walk a run's workspace or the git store: those are copies
			// of some past tree, not this repository's source.
			switch info.Name() {
			case ".adlc", ".git", "workspaces":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md", ".json":
		default:
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		checked++
		for _, sig := range doubleEncoded {
			if n := strings.Count(string(b), sig); n > 0 {
				bad[path] += n
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("walked no files at all, so this guard would pass vacuously")
	}
	for path, n := range bad {
		t.Errorf("%s carries %d double-encoded sequence(s): an em-dash renders as three blocks on a page somebody reads", path, n)
	}
}
