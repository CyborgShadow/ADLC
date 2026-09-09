package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The exit codes are the command's contract with every caller that scripts it,
// and until this file existed nothing held them: the package under cmd had no
// tests, so 0, 1 and 2 could be permuted and the whole suite stayed green. A
// caller that cannot tell 1 from 2 treats its own typo as a failing site and
// fixes the site.

const testdata = "../../internal/sitecheck/testdata"

// runCLI invokes run() the way the shell would and returns what a caller sees.
//
// The flag set is replaced per invocation because flag.String registers on the
// process-wide CommandLine: a second run() would panic on a redefined flag, so
// without this the contract is testable exactly once. ContinueOnError rather
// than ExitOnError for the same reason — an ExitOnError set would take the
// test binary down with it instead of reporting.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	oldArgs, oldOut, oldErr, oldFlags := os.Args, os.Stdout, os.Stderr, flag.CommandLine
	defer func() {
		os.Args, os.Stdout, os.Stderr, flag.CommandLine = oldArgs, oldOut, oldErr, oldFlags
	}()
	flag.CommandLine = flag.NewFlagSet("sitecheck", flag.ContinueOnError)

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	// Drained concurrently: a run whose output outgrew the pipe buffer would
	// otherwise block forever, and a hanging check is one that gets skipped.
	outC, errC := make(chan string, 1), make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outC <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errC <- string(b) }()

	os.Stdout, os.Stderr = outW, errW
	os.Args = append([]string{"sitecheck"}, args...)
	code = run()

	outW.Close()
	errW.Close()
	return code, <-outC, <-errC
}

var imagesChecked = regexp.MustCompile(`^images checked: ([0-9]+)$`)

// TestConformingSiteExitsZeroWithOneCountLine is AC-1. "Exactly one line" is
// asserted rather than "contains", because a run that also printed findings
// and still exited 0 is the failure this criterion is watching for.
func TestConformingSiteExitsZeroWithOneCountLine(t *testing.T) {
	code, out, _ := runCLI(t, "-dir", filepath.Join(testdata, "site-ok"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s", code, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout has %d lines, want exactly 1: %q", len(lines), out)
	}
	m := imagesChecked.FindStringSubmatch(lines[0])
	if m == nil {
		t.Fatalf("stdout line %q does not match %s", lines[0], imagesChecked)
	}
	if m[1] == "0" {
		t.Errorf("reported 0 images checked: a run that examined nothing has not passed")
	}
}

// TestSiteWithNoImagesExitsNonZero is AC-2, and the refusal the whole design
// exists for.
func TestSiteWithNoImagesExitsNonZero(t *testing.T) {
	code, out, errOut := runCLI(t, "-dir", filepath.Join(testdata, "site-empty"))
	if code == 0 {
		t.Fatalf("exit = 0 over a directory holding no images; stdout: %s", out)
	}
	if !strings.Contains(out, "core.zero-work") {
		t.Errorf("output does not name core.zero-work: stdout %q stderr %q", out, errOut)
	}
}

// TestUnknownFamilyIsAUsageError is AC-3's refusal half: exit 2, not 1, so a
// caller can tell its own typo from a site that failed.
func TestUnknownFamilyIsAUsageError(t *testing.T) {
	code, _, errOut := runCLI(t, "-dir", filepath.Join(testdata, "site-ok"), "-rule", "credits,bogus")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "usage:") {
		t.Errorf("no usage message on stderr: %q", errOut)
	}
	if !strings.Contains(errOut, "bogus") {
		t.Errorf("the message does not name the family that was rejected: %q", errOut)
	}
}

// TestEveryFamilySubsetIsAccepted is AC-3's accepting half. A checker that
// quietly rejected a valid subset, or quietly ran everything regardless, looks
// identical from a conforming fixture, so TestSelectingAFamilyRunsOnlyThatFamily
// below carries the other half.
func TestEveryFamilySubsetIsAccepted(t *testing.T) {
	for _, sel := range []string{
		"credits", "images", "css", "html",
		"credits,images", "css,html", "credits,images,css,html", "html,css,images,credits",
	} {
		code, out, errOut := runCLI(t, "-dir", filepath.Join(testdata, "site-ok"), "-rule", sel)
		if code != 0 {
			t.Errorf("-rule %s: exit = %d, want 0; stdout %q stderr %q", sel, code, out, errOut)
		}
	}
}

// TestSelectingAFamilyRunsOnlyThatFamily proves -rule filters: an unselected
// family's fixture has to come back clean, or the flag is decoration.
func TestSelectingAFamilyRunsOnlyThatFamily(t *testing.T) {
	cases := []struct {
		fixture, sel string
		want         int
	}{
		{"bad-css.font-size", "css", 1},
		{"bad-css.font-size", "credits", 0},
		{"bad-credits.licence", "credits", 1},
		{"bad-credits.licence", "html", 0},
		{"bad-images.size", "images", 1},
		{"bad-images.size", "html", 0},
		{"bad-html.no-script", "html", 1},
		{"bad-html.no-script", "images", 0},
	}
	for _, c := range cases {
		code, out, _ := runCLI(t, "-dir", filepath.Join(testdata, c.fixture), "-rule", c.sel)
		if code != c.want {
			t.Errorf("%s -rule %s: exit = %d, want %d; stdout %q", c.fixture, c.sel, code, c.want, out)
		}
	}
}

// TestEveryRuleFixtureExitsOneAndNamesItself is AC-5, walked over the fixture
// directories themselves rather than a list written out here: a list would
// silently stop covering a rule whose fixture was added later.
func TestEveryRuleFixtureExitsOneAndNamesItself(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(testdata, "bad-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatal("no bad-* fixtures found: a run that discovered zero units of work has failed")
	}
	for _, dir := range dirs {
		ruleID := strings.TrimPrefix(filepath.Base(dir), "bad-")
		code, out, errOut := runCLI(t, "-dir", dir)
		if code != 1 {
			t.Errorf("%s: exit = %d, want 1; stdout %q stderr %q", ruleID, code, out, errOut)
			continue
		}
		var found string
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if strings.HasPrefix(line, ruleID+": ") {
				found = line
				break
			}
		}
		if found == "" {
			t.Errorf("%s: no output line names the rule; stdout %q", ruleID, out)
			continue
		}
		// rule id, path, detail — a count tells a reader something is wrong
		// but not what to open.
		if parts := strings.SplitN(found, ": ", 3); len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			t.Errorf("%s: line %q does not carry both a path and a detail", ruleID, found)
		}
	}
}

// TestUnreadableDirIsAUsageError keeps 2 meaning "the invocation was wrong"
// rather than leaking out as 1, which a caller would read as a failing site.
func TestUnreadableDirIsAUsageError(t *testing.T) {
	code, _, errOut := runCLI(t, "-dir", filepath.Join(testdata, "no-such-fixture"))
	if code != 2 {
		t.Fatalf("exit = %d over a missing -dir, want 2; stderr %q", code, errOut)
	}
}
