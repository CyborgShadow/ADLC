package dispatch

import (
	"encoding/json"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/ledger"
)

// An agent's own account of what it spent is a declaration like everything
// else in an envelope, and it is the one field no agent fills in: 157 finished
// runs in one night reported no token usage at all, so every spend figure was a
// floor and the per-run cap could not fire. The tool the control plane invokes
// prints the real numbers on its own last line. Reading them there is the same
// move the gate makes with checks — measure it rather than ask.

// usageWatcher keeps the last terminal result line an agent's stream-json
// output produced. Only the last one counts: a resumed session prints one per
// turn, and the run's cost is the one it ended on.
type usageWatcher struct {
	last ledger.Usage
	cost float64
	seen bool
}

// streamResult is the shape this reads and nothing more. Unknown fields are
// ignored on purpose — a vendor adding a field must not stop the control plane
// measuring what it already understands.
type streamResult struct {
	Type  string `json:"type"`
	Usage struct {
		Input       int64 `json:"input_tokens"`
		Output      int64 `json:"output_tokens"`
		CacheRead   int64 `json:"cache_read_input_tokens"`
		CacheCreate int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	CostUSD float64 `json:"total_cost_usd"`
}

// observe reads one line of agent output. Anything that is not a terminal
// result object is ignored, which is almost every line.
func (w *usageWatcher) observe(line string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") || !strings.Contains(line, `"result"`) {
		return
	}
	var r streamResult
	if json.Unmarshal([]byte(line), &r) != nil || r.Type != "result" {
		return
	}
	total := r.Usage.Input + r.Usage.Output + r.Usage.CacheRead + r.Usage.CacheCreate
	if total == 0 && r.CostUSD == 0 {
		// A result line carrying no numbers measures nothing. Recording it as
		// a measurement would turn "nobody counted" into "it cost zero", which
		// are opposite facts.
		return
	}
	w.last = ledger.Usage{
		InputTokens:      r.Usage.Input,
		OutputTokens:     r.Usage.Output,
		CacheReadTokens:  r.Usage.CacheRead,
		CacheWriteTokens: r.Usage.CacheCreate,
	}
	w.cost, w.seen = r.CostUSD, true
}

// measured returns what the tool reported, and whether anything was measured at
// all. The second return is the whole point: absence must not render as zero.
func (w *usageWatcher) measured() (ledger.Usage, bool) {
	if !w.seen {
		return ledger.Usage{}, false
	}
	return w.last, true
}
