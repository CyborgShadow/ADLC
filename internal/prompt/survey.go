package prompt

// Surveying the library against the config.
//
// Load is strict on purpose: a dispatch assembled without the shared preamble
// is a run handed no fleet policy, so a library that cannot produce one is
// refused whole. That strictness is exactly what made `adlc config init`'s
// closing line untrue. It says the prompts go in agents/ and that `adlc prompt
// list` says which are missing — but on a project that has not written them
// yet, Load fails on the preamble and the command that was supposed to
// enumerate the gap prints an error about one file instead.
//
// Survey answers that question and cannot fail. It reads what is there, is told
// what the roles ask for, and reports the difference. Nothing dispatches from
// it, so nothing here has to refuse.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// Status is one prompt, and whether the library actually holds it.
type Status struct {
	ID string
	// Roles are the worker types whose dispatch resolves through this prompt.
	// Empty means the file is in the directory and nothing names it.
	Roles   []string
	Present bool
	Path    string
	Version string
	SHA     string
}

// Missing reports a prompt a role names that is not on disk. Those are the only
// entries that break a dispatch: an unreferenced file is dead weight, not a
// failure.
func (s Status) Missing() bool { return !s.Present && len(s.Roles) > 0 }

// Inventory is the whole answer: what the roles need, what is there, and the
// count of the gap between them.
type Inventory struct {
	Dir string
	// DirErr is why the directory could not be read, when it could not be. It is
	// a string rather than an error because it is reported, never handled.
	DirErr string
	// Prompts is every id a role names, plus every file the directory holds,
	// sorted so two runs of the command read the same.
	Prompts []Status

	PreambleFile    string
	PreamblePresent bool
	PreambleSHA     string

	// Missing counts the prompts a role names and the directory does not hold,
	// plus the preamble when one is declared and absent. It is the number that
	// decides the exit code: a role whose prompt is not there cannot be
	// dispatched, which is a broken fleet rather than an untidy one.
	Missing int
}

// Survey reads the prompt directory and compares it with what the workers ask
// for. It reports; it never refuses.
func Survey(pol config.PromptPolicy, workers []config.WorkerDecl) Inventory {
	inv := Inventory{Dir: pol.Dir, PreambleFile: pol.PreambleFile}

	onDisk := map[string]Prompt{}
	if pol.Dir != "" {
		entries, err := os.ReadDir(pol.Dir)
		if err != nil {
			inv.DirErr = err.Error()
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(pol.Dir, e.Name())
			// The preamble is reported on its own line. Listing it here as well
			// would show fleet policy as a role prompt nothing references.
			if pol.PreambleFile != "" && sameFile(path, pol.PreambleFile) {
				continue
			}
			p, err := readPrompt(path)
			if err != nil {
				continue
			}
			onDisk[p.ID] = p
		}
	}

	// Roles first, in the order the config declares them, so the ids a person is
	// looking for are the ones they wrote.
	roles := map[string][]string{}
	var wanted []string
	for _, w := range workers {
		id := strings.TrimSpace(w.Prompt)
		if id == "" {
			continue
		}
		if _, seen := roles[id]; !seen {
			wanted = append(wanted, id)
		}
		roles[id] = append(roles[id], w.Type)
	}
	for id := range onDisk {
		if _, named := roles[id]; !named {
			wanted = append(wanted, id)
		}
	}
	sort.Strings(wanted)

	for _, id := range wanted {
		s := Status{ID: id, Roles: roles[id]}
		if p, ok := onDisk[id]; ok {
			s.Present, s.Path, s.Version, s.SHA = true, p.Path, p.Version, p.SHA()
		}
		if s.Missing() {
			inv.Missing++
		}
		inv.Prompts = append(inv.Prompts, s)
	}

	if pol.PreambleFile != "" {
		if b, err := os.ReadFile(pol.PreambleFile); err == nil {
			inv.PreamblePresent = true
			inv.PreambleSHA = digest(string(config.StripBOM(b)))
		} else {
			inv.Missing++
		}
	}
	return inv
}

// MissingFor names the prompts the given workers reference that the library
// does not hold, as "worker (prompt)" pairs.
//
// A role that cannot be dispatched because its prompt file is absent is the
// same failure as an area with no owner: the work is filed, nothing picks it
// up, and the silence reads as nobody having got round to it.
func (l *Library) MissingFor(workers []config.WorkerDecl) []Status {
	l.mu.RLock()
	defer l.mu.RUnlock()
	roles := map[string][]string{}
	var order []string
	for _, w := range workers {
		id := strings.TrimSpace(w.Prompt)
		if id == "" {
			continue
		}
		if _, ok := l.prompts[id]; ok {
			continue
		}
		if _, seen := roles[id]; !seen {
			order = append(order, id)
		}
		roles[id] = append(roles[id], w.Type)
	}
	sort.Strings(order)
	out := make([]Status, 0, len(order))
	for _, id := range order {
		out = append(out, Status{ID: id, Roles: roles[id]})
	}
	return out
}

// sameFile compares two paths that were built from the same config, so a
// separator difference is the only discrepancy worth normalising here.
func sameFile(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
