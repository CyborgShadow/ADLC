package authority

import (
	"fmt"
	"sort"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// CheckEdgesExist refuses a config whose checks are bound to transitions that
// are not in the table.
//
// A check declares the edges it gates. When the verification chain was
// collapsed into one stage the state names changed, and every check went on
// naming edges that no longer existed — so the gate for in_progress had nothing
// to run, every engineer's work was refused with zero_checks_declared, and the
// fleet built the same item over and over while `config check` reported the
// config valid.
//
// It looked exactly like a config that was fine, which is the failure this
// whole loader exists to refuse: an unknown field is rejected because a typo in
// a safety setting that loads cleanly is a setting nobody applied. An edge that
// does not exist is the same defect one level up.
func CheckEdgesExist(cfg *config.Config) error {
	known := map[string]bool{}
	for _, e := range Table() {
		known[string(e.From)+"->"+string(e.To)] = true
	}
	var bad []string
	for _, ch := range cfg.Checks {
		for _, edge := range ch.RequiredFor {
			if !known[edge] {
				bad = append(bad, fmt.Sprintf("check %q gates %q", ch.ID, edge))
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf(
		"%s, and that transition is not in the table. A check bound to an edge that does not exist never runs, so the edge it was meant to gate reports zero checks declared and refuses every proposal — while this config reads as valid. Run `adlc transition table` for the edges that exist:\n  %s",
		bad[0], strings.Join(bad, "\n  "))
}
