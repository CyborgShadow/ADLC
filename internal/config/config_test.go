package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// base is a minimal valid config. Each test breaks exactly one thing, so a
// failure names the rule rather than the fixture.
func base() map[string]any {
	return map[string]any{
		"version":      SchemaVersion,
		"project":      "t",
		"source_roots": []string{"src"},
		"checks": []map[string]any{{
			"id": "test", "command": []string{"go", "test"}, "verdict": "exit_zero",
			"required_for": []string{"in_progress->ready_for_testing"},
		}},
		"workers": []map[string]any{
			{"type": "builder", "prompt": "impl", "capabilities": []string{CapImplement}},
			{"type": "checker", "prompt": "verify", "capabilities": []string{CapTest}},
			{"type": "adjudicator", "prompt": "judge", "capabilities": []string{CapJudge}},
			{"type": "reviewer", "prompt": "review", "capabilities": []string{CapValidate}},
		},
		"routing": map[string]string{"core": "builder"},
		"blast":   map[string]any{"auto_apply_max": "none"},
	}
}

func loadFrom(t *testing.T, m map[string]any) (*Config, error) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "adlc.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func mustLoad(t *testing.T, m map[string]any) *Config {
	t.Helper()
	c, err := loadFrom(t, m)
	if err != nil {
		t.Fatalf("valid config was refused: %v", err)
	}
	return c
}

func TestAValidConfigLoads(t *testing.T) {
	c := mustLoad(t, base())
	if c.Project != "t" || len(c.Checks) != 1 || len(c.Workers) != 4 {
		t.Fatalf("unexpected: %+v", c)
	}
	// Defaults are filled rather than left zero, so nothing downstream has to
	// guess what an unset value meant.
	if c.Dispatch.MaxAttempts == 0 || c.Lease.TTLMinutes == 0 || c.Server.Addr == "" {
		t.Errorf("defaults not applied: %+v %+v %+v", c.Dispatch, c.Lease, c.Server)
	}
}

// TestTheValidatorRefusesConfigsThatWouldStallTheFleet covers the rules whose
// violation produces silence rather than an error at run time — the expensive
// kind, because a stalled fleet looks exactly like an idle one.
func TestTheValidatorRefusesConfigsThatWouldStallTheFleet(t *testing.T) {
	cases := map[string]struct {
		mutate func(m map[string]any)
		want   string
	}{
		"no source roots": {
			func(m map[string]any) { delete(m, "source_roots") },
			"source_roots",
		},
		"no checks at all": {
			func(m map[string]any) { m["checks"] = []map[string]any{} },
			"green over nothing",
		},
		"an area owned by nobody": {
			func(m map[string]any) { m["routing"] = map[string]string{"core": "ghost"} },
			"unreachable",
		},
		"nothing may say done": {
			func(m map[string]any) {
				m["workers"] = []map[string]any{
					{"type": "builder", "prompt": "impl", "capabilities": []string{CapImplement}},
				}
			},
			"terminal edge needs an owner",
		},
		"a check with no verdict channel": {
			func(m map[string]any) {
				m["checks"] = []map[string]any{{"id": "x", "command": []string{"true"}}}
			},
			"verdict rule is required",
		},
		"a lane draining work nobody can do": {
			func(m map[string]any) {
				m["loops"] = []map[string]any{{"name": "apply", "capability": CapOperate}}
			},
			"fire forever and find nothing",
		},
		"two lanes with one name": {
			func(m map[string]any) {
				m["loops"] = []map[string]any{
					{"name": "a", "capability": CapTest},
					{"name": "a", "capability": CapValidate},
				}
			},
			"neither would be measurable",
		},
		"a behavioural check bound to nothing": {
			func(m map[string]any) {
				m["checks"] = []map[string]any{{
					"id": "scan", "kind": "behavioural",
					"command": []string{"true"}, "verdict": "exit_zero",
				}}
			},
			"binds_artifact",
		},
		"a schema version from the future": {
			func(m map[string]any) { m["version"] = SchemaVersion + 1 },
			"this build understands",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := base()
			c.mutate(m)
			_, err := loadFrom(t, m)
			if err == nil {
				t.Fatal("this config should have been refused")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal should explain itself; got %q, wanted something containing %q", err, c.want)
			}
		})
	}
}

func TestAnUnknownFieldIsRefusedRatherThanIgnored(t *testing.T) {
	m := base()
	m["auto_aply_max"] = "host" // a typo of a real setting
	if _, err := loadFrom(t, m); err == nil {
		t.Fatal("a misspelled setting must not be silently ignored — it would read as the default and nobody would know")
	}
}

func TestACommentIsNotAnUnknownField(t *testing.T) {
	m := base()
	m["_comment"] = "why this project is configured like this"
	mustLoad(t, m)
}

// TestOwnerForPrefersTheAreaThenTheGeneralist pins the routing precedence.
func TestOwnerForPrefersTheAreaThenTheGeneralist(t *testing.T) {
	m := base()
	m["workers"] = []map[string]any{
		{"type": "checker", "prompt": "p", "capabilities": []string{CapTest}},
		{"type": "adjudicator", "prompt": "p", "capabilities": []string{CapJudge}},
		{"type": "alpha", "prompt": "p", "capabilities": []string{CapValidate}, "areas": []string{"security"}},
		{"type": "general", "prompt": "p", "capabilities": []string{CapValidate}},
		{"type": "zulu", "prompt": "p", "capabilities": []string{CapValidate}, "areas": []string{"perf"}},
		{"type": "builder", "prompt": "p", "capabilities": []string{CapImplement}},
	}
	m["routing"] = map[string]string{"review": "general", "security": "alpha"}
	c := mustLoad(t, m)

	for _, tc := range []struct{ area, want, why string }{
		{"security", "alpha", "the area's declared owner wins"},
		{"perf", "zulu", "a worker listing the area wins over a generalist"},
		{"unclaimed", "general", "an unclaimed area goes to the generalist, not to whichever specialist sorts first"},
	} {
		got, ok := c.OwnerFor(tc.area, CapValidate)
		if !ok || got != tc.want {
			t.Errorf("area %q went to %q, want %q — %s", tc.area, got, tc.want, tc.why)
		}
	}
	if _, ok := c.OwnerFor("security", CapOperate); ok {
		t.Error("a capability nobody holds must report as unreachable, not fall through to someone who cannot do it")
	}
}

// TestKnownAreaIsStricterThanOwnerFor pins the deliberate asymmetry: dispatch
// must never strand an item that already exists, but generation may not invent
// a taxonomy.
func TestKnownAreaIsStricterThanOwnerFor(t *testing.T) {
	c := mustLoad(t, base())
	if _, ok := c.OwnerFor("invented", CapImplement); !ok {
		t.Error("dispatch must still reach somebody for an unclaimed area")
	}
	if c.KnownArea("invented") {
		t.Error("generation must not be able to file work under an area nobody declared")
	}
	if !c.KnownArea("core") {
		t.Error("a declared area must be known")
	}
}

func TestRadiusFailsClosed(t *testing.T) {
	if Radius("planetary").Known() {
		t.Fatal("an unrecognised radius must not be treated as known")
	}
	if Radius("planetary").Rank() <= RadiusGlobal.Rank() {
		t.Fatal("an unrecognised radius must rank above the widest known one, so policy comparisons fail closed")
	}
	if RadiusNone.Rank() >= RadiusHost.Rank() {
		t.Fatal("radii must order from narrowest to widest")
	}
}

func TestChecksAreSelectedByEdge(t *testing.T) {
	m := base()
	m["checks"] = []map[string]any{
		{"id": "a", "command": []string{"true"}, "verdict": "exit_zero",
			"required_for": []string{"in_progress->ready_for_testing"}},
		{"id": "b", "command": []string{"true"}, "verdict": "exit_zero",
			"required_for": []string{"confirming->done"}},
	}
	c := mustLoad(t, m)
	got := c.ChecksForEdge("in_progress", "ready_for_testing")
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("a check gates only the edges it names; got %d", len(got))
	}
	if len(c.ChecksForEdge("validating", "done")) != 0 {
		t.Fatal("an edge nothing declares must return nothing, so the gate can report that it observed nothing")
	}
}

func TestVerdictRuleChannelIsExplicit(t *testing.T) {
	// Anything reading a check's result has to read it in the channel the gate
	// judged it in, so this classification is exported and pinned.
	for _, r := range []VerdictRule{VerdictExitZero, VerdictExitIn} {
		if !r.TurnsOnExitCode() {
			t.Errorf("%s is an exit-code rule", r)
		}
	}
	for _, r := range []VerdictRule{VerdictOutputEmpty, VerdictOutputMatches, VerdictGoTestJSON, VerdictCountMin} {
		if r.TurnsOnExitCode() {
			t.Errorf("%s reads its verdict from the output, not the exit code", r)
		}
	}
}

func TestSetLoopPersistsAndRefusesAThrashingCadence(t *testing.T) {
	m := base()
	m["loops"] = []map[string]any{{"name": "verify", "capability": CapTest, "every_seconds": 300}}
	b, _ := json.Marshal(m)
	p := filepath.Join(t.TempDir(), "adlc.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetLoop("verify", false, 120, 2); err != nil {
		t.Fatalf("pausing a lane should be allowed: %v", err)
	}
	again, err := Load(p)
	if err != nil {
		t.Fatalf("the saved config must still load: %v", err)
	}
	l := again.Loop("verify")
	if l == nil || l.Enabled || l.EverySeconds != 120 || l.MaxPerTick != 2 {
		t.Fatalf("the change did not persist: %+v", l)
	}
	if err := c.SetLoop("verify", true, 3, 1); err == nil {
		t.Fatal("a cadence that spends more time starting runs than doing them must be refused")
	}
	if err := c.SetLoop("nonexistent", true, 60, 1); err == nil {
		t.Fatal("a lane nobody declared cannot be configured")
	}
}

func TestStripBOMLeavesEverythingElseAlone(t *testing.T) {
	body := []byte(`{"a":1}`)
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, body...)
	if string(StripBOM(withBOM)) != string(body) {
		t.Fatal("a byte-order mark must be removed")
	}
	if string(StripBOM(body)) != string(body) {
		t.Fatal("content without a BOM must be untouched")
	}
}

// TestAnAnchoredCountPatternFindsTheLineItNames pins the fix: a count_pattern
// anchored to its line is compiled so that ^ and $ mean the line, not the whole
// output.
//
// Written the obvious way, "^images checked: ([0-9]+)$" matched nothing at all,
// because a command's output ends in a newline and Go's $ without (?m) is end
// of text. The check counted 0 and refused a scan that had run.
func TestAnAnchoredCountPatternFindsTheLineItNames(t *testing.T) {
	const pattern = `^images checked: ([0-9]+)$`
	ch := Check{ID: "images", Command: []string{"x"}, Verdict: VerdictCountMin, CountPattern: pattern, MinCount: 6}
	if err := ch.Compile(); err != nil {
		t.Fatalf("compile: %v", err)
	}

	for _, output := range []string{
		"images checked: 6\n",
		"scanning site/cats\nimages checked: 6\nall rules passed\n",
	} {
		m := ch.Counter().FindStringSubmatch(output)
		if len(m) < 2 || m[1] != "6" {
			t.Errorf("over %q the compiled counter found %q, want the capture 6", output, m)
		}
	}

	// The defect itself, so this test fails for the right reason if the anchoring
	// is ever dropped again: the raw pattern, compiled as written, finds nothing.
	if m := regexp.MustCompile(pattern).FindStringSubmatch("images checked: 6\n"); m != nil {
		t.Errorf("the unanchored-compile defect is gone from Go itself, got %q — this test no longer proves anything", m)
	}
}

// TestOnlyAnchoredCountPatternsMove is the clean case for the rule above: the
// count patterns that exist in this repository's examples carry no anchors, and
// line anchoring must leave them counting exactly what they counted before.
//
// A count rule that started matching more than it was written to match would be
// a worse defect than the one being fixed, because it would read as green.
func TestOnlyAnchoredCountPatternsMove(t *testing.T) {
	cases := []struct {
		pattern string
		output  string
	}{
		// examples/infra.adlc.json: the ansible-check and cis-scan patterns.
		{`ok=(\d+)`, "host-a : ok=12 changed=3 failed=0\nhost-b : ok=9 changed=0 failed=0\n"},
		{`([0-9]+) rules evaluated`, "profile level1-server\n412 rules evaluated\n"},
	}
	for _, tc := range cases {
		ch := Check{ID: "c", Command: []string{"x"}, Verdict: VerdictCountMin, CountPattern: tc.pattern, MinCount: 1}
		if err := ch.Compile(); err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		got := ch.Counter().FindAllStringSubmatch(tc.output, -1)
		want := regexp.MustCompile(tc.pattern).FindAllStringSubmatch(tc.output, -1)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q counts differently after line anchoring: got %q, want %q", tc.pattern, got, want)
		}
	}
}

// gitInRepo runs one git command in a throwaway repository, failing the test
// with the command that broke rather than an errno nobody can place.
func gitInRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// docsRepo makes a repository holding one file of code and one of prose, both
// committed, so a test can edit either and ask what the declared scope sees.
func docsRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInRepo(t, dir, "init", "-b", "main")
	gitInRepo(t, dir, "config", "user.email", "t@example.invalid")
	gitInRepo(t, dir, "config", "user.name", "t")
	// A checkout that rewrites line endings shows as a file modified by nobody,
	// which is the state this test exists to tell a real edit apart from.
	gitInRepo(t, dir, "config", "core.autocrlf", "false")
	for rel, body := range map[string]string{
		"cmd/main.go":    "package main\n",
		"README.md":      "# t\n",
		"docs/design.md": "# design\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInRepo(t, dir, "add", "-A")
	gitInRepo(t, dir, "commit", "-m", "base")
	return dir
}

// scopedStatus asks git the question the dirty-tree guard asks — the porcelain
// status of exactly the declared roots — and answers with the paths it named.
//
// The guard itself is internal/gate.TreeState, which this test cannot call:
// gate imports config, so a config test importing gate is an import cycle. The
// half of the guard under test here is its scope, and scope is a git pathspec,
// so putting the same pathspec to git asks the same question of the same
// oracle. What the walk does with the answer is pinned next to the walk.
func scopedStatus(t *testing.T, dir string, roots []string) []string {
	t.Helper()
	args := append([]string{"status", "--porcelain", "--untracked-files=normal", "--"}, roots...)
	var paths []string
	for _, line := range strings.Split(gitInRepo(t, dir, args...), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.LastIndex(line, " "); i >= 0 && i+1 < len(line) {
			line = line[i+1:]
		}
		paths = append(paths, line)
	}
	return paths
}

// TestTheShippedRootsWatchTheDocumentation reads the roots this repository
// actually declares and puts them to git, rather than asserting on the strings
// in adlc.json.
//
// The defect: docs/ and README.md were not on the declared list, so a change to
// them was invisible to the dirty-tree guard and the gate collected evidence
// over a tree with uncommitted prose in it — a state nobody can check out
// again. Prose in this repository is source: docs/design.md states the decision
// behind every constraint, and a gate that cannot see it edited will pass a
// commit whose reasoning was never recorded.
//
// This test is red on the commit before docs and README.md were declared.
func TestTheShippedRootsWatchTheDocumentation(t *testing.T) {
	cfg, err := Load("../../adlc.json")
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}
	dir := docsRepo(t)

	// Clean case first, and it is not a formality: declaring more roots must
	// not make a tree with nothing uncommitted read dirty. A guard that reports
	// every tree dirty is a permanent stop, and it would pass a firing case on
	// its own.
	if paths := scopedStatus(t, dir, cfg.SourceRoots); len(paths) != 0 {
		t.Fatalf("a tree with nothing uncommitted must be clean under the shipped roots %v, got %v", cfg.SourceRoots, paths)
	}

	// Firing case: an uncommitted edit to README.md.
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# t\n\nan edit nobody committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := scopedStatus(t, dir, cfg.SourceRoots)
	if len(paths) != 1 || !strings.Contains(paths[0], "README.md") {
		t.Fatalf("an uncommitted edit to README.md must be visible to the shipped roots %v; got %v, so the gate would report a clean tree over it", cfg.SourceRoots, paths)
	}

	// The same for docs/, the other half of what this commit declared.
	if err := os.WriteFile(filepath.Join(dir, "docs", "design.md"), []byte("# design\n\nan edit nobody committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if paths := scopedStatus(t, dir, cfg.SourceRoots); len(paths) != 2 {
		t.Errorf("an uncommitted edit under docs/ must be visible too, got %v", paths)
	}

	// The scope is doing the work and not the tree: a file under a directory
	// nobody declared stays invisible, which is why widening the declared list
	// was the whole of the fix.
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "third_party.go"), []byte("package vendor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if paths := scopedStatus(t, dir, cfg.SourceRoots); len(paths) != 2 {
		t.Errorf("an undeclared directory must not be walked, got %v", paths)
	}
}
