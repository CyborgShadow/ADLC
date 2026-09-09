package dispatch

import "testing"

// The control plane measures what a run spent from the tool's own output.
//
// An envelope's usage block is a claim like every other field in it, and it is
// the one no agent fills in: 157 finished runs in one night reported no tokens
// at all, so every spend figure on every surface was a floor and the per-run
// cap could not fire on anything.
func TestARunsSpendIsMeasuredFromTheToolsOwnOutput(t *testing.T) {
	var w usageWatcher

	// Almost every line is not this, and none of them may disturb it.
	for _, noise := range []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"the result is in"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta"}}`,
		`not json at all`,
		``,
	} {
		w.observe(noise)
	}
	if _, ok := w.measured(); ok {
		t.Fatal("nothing measured yet, but the watcher claims a measurement")
	}

	w.observe(`{"type":"result","subtype":"success","total_cost_usd":0.42,` +
		`"usage":{"input_tokens":11,"output_tokens":22,"cache_read_input_tokens":33,"cache_creation_input_tokens":44}}`)
	got, ok := w.measured()
	if !ok {
		t.Fatal("a result line carrying real numbers was not read as a measurement")
	}
	if got.InputTokens != 11 || got.OutputTokens != 22 ||
		got.CacheReadTokens != 33 || got.CacheWriteTokens != 44 {
		t.Fatalf("the numbers did not survive the read: %+v", got)
	}

	// A resumed session prints one result line per turn. The run's spend is the
	// one it ended on.
	w.observe(`{"type":"result","subtype":"success","total_cost_usd":0.9,` +
		`"usage":{"input_tokens":100,"output_tokens":200,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
	last, _ := w.measured()
	if last.InputTokens != 100 || last.OutputTokens != 200 {
		t.Errorf("the last result should win, got %+v", last)
	}
}

// A result line with no numbers in it measures nothing, and must not be read as
// a measurement of zero. "Nobody counted" and "it cost nothing" are opposite
// facts, and the whole spend surface is built on telling them apart.
func TestAResultWithNoNumbersMeasuresNothing(t *testing.T) {
	var w usageWatcher
	w.observe(`{"type":"result","subtype":"error_during_execution","total_cost_usd":0,` +
		`"usage":{"input_tokens":0,"output_tokens":0}}`)
	if u, ok := w.measured(); ok {
		t.Errorf("an empty result was recorded as a measurement of %+v", u)
	}
}
