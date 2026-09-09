package authority

import (
	"strings"
	"testing"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// A check bound to an edge that does not exist is refused.
//
// When the verification chain collapsed into one stage the state names changed,
// and every check went on naming edges that no longer existed. The gate for
// in_progress had nothing to run, so every engineer's work was refused with
// zero_checks_declared and the fleet rebuilt the same items for an hour — while
// `config check` reported the config valid the whole time.
func TestAConfigWhoseChecksGateNothingIsRefused(t *testing.T) {
	cfg := &config.Config{Checks: []config.Check{
		{ID: "fmt", RequiredFor: []string{"in_progress->ready_for_testing"}},
	}}
	err := CheckEdgesExist(cfg)
	if err == nil {
		t.Fatal("a check gating an edge that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "zero checks declared") {
		t.Errorf("the refusal does not say what goes wrong at runtime: %v", err)
	}

	// The clean case, which is what stops this passing vacuously: a check bound
	// to a real edge is fine.
	ok := &config.Config{Checks: []config.Check{
		{ID: "fmt", RequiredFor: []string{"in_progress->verifying"}},
	}}
	if err := CheckEdgesExist(ok); err != nil {
		t.Fatalf("a check on a real edge was refused: %v", err)
	}
}

// Every edge this project's own config gates must be in the table. The guard
// above is only as good as the thing that runs it.
func TestThisProjectsChecksGateRealEdges(t *testing.T) {
	cfg, err := config.Load("../../adlc.json")
	if err != nil {
		t.Skipf("this project's config is not readable from here: %v", err)
	}
	if err := CheckEdgesExist(cfg); err != nil {
		t.Fatalf("adlc.json gates an edge that does not exist: %v", err)
	}
	gated := 0
	for _, ch := range cfg.Checks {
		gated += len(ch.RequiredFor)
	}
	if gated == 0 {
		t.Fatal("no check gates any edge, so this guard proves nothing")
	}
}

// The config generator emits edges that exist.
//
// A hand-written copy of the gated edges lived in the generator, and when the
// verification chain was renamed it went on emitting the old ones — so
// `adlc config init` produced a config that `adlc config check` refused, and a
// new project was dead at its first command. Nobody looked at the generator,
// because nothing pointed there.
func TestTheEdgesTheGeneratorEmitsAllExist(t *testing.T) {
	edges := GatedEdges()
	if len(edges) == 0 {
		t.Fatal("the generator would emit a check that gates nothing, which runs nowhere")
	}
	cfg := &config.Config{Checks: []config.Check{{ID: "probe", RequiredFor: edges}}}
	if err := CheckEdgesExist(cfg); err != nil {
		t.Fatalf("a scaffolded config would be refused by config check: %v", err)
	}
	// And the edge work actually crosses first is among them, or an
	// implementer's work is admitted with nothing run against it.
	found := false
	for _, e := range edges {
		if e == string(StateInProgress)+"->"+string(StateVerifying) {
			found = true
		}
	}
	if !found {
		t.Error("nothing gates the edge work crosses when it lands")
	}
}
