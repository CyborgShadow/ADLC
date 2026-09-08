package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if c.Project != "t" || len(c.Checks) != 1 || len(c.Workers) != 3 {
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
