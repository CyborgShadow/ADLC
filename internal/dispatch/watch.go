package dispatch

// Watching a run while it happens.
//
// A lane's run is a full agent doing real work for several minutes, and until
// it ended the record had exactly one thing to say about it: that it was
// dispatched. Everything else — what it read, what it tried, whether it was
// making progress or stuck — arrived at the end or not at all. An operator
// watching a nine-minute run learned nothing from the page about it, which
// makes "is this working?" a question you answer by waiting.
//
// The console already streamed its turns. This is the same mechanism pointed at
// the rest of the fleet.
//
// A Watcher is told about output; it is never asked anything. The dispatcher
// does not read from it, does not wait on it, and does not change what it does
// because of it — so a fleet with no watcher behaves identically, and nothing
// on the record depends on one having been there.
type Watcher interface {
	// Open says a run is starting and its output is about to arrive.
	Open(runID, worker, item string)
	// Line delivers one line of that run's output, as it arrives.
	Line(runID, line string)
	// Close says no more is coming, however the run ended.
	Close(runID string)
}

// watch returns the watcher or a sink, so callers need no branch.
func (d *Dispatcher) watch() Watcher {
	if d.Watch != nil {
		return d.Watch
	}
	return noWatcher{}
}

type noWatcher struct{}

func (noWatcher) Open(string, string, string) {}
func (noWatcher) Line(string, string)         {}
func (noWatcher) Close(string)                {}
