// Package prompt is the versioned prompt library.
//
// A prompt that lives only inside a scheduler or a dispatcher's source is
// unversioned, unreviewable and undiffable. Here the file IS the definition:
// the dispatcher reads it at dispatch time and carries no prompt of its own,
// so changing a worker is an ordinary reviewable edit and every past run can
// still say exactly which bytes it was given.
//
// Two rules govern the library and neither is negotiable.
//
// The shared preamble is stored exactly once and assembled at dispatch, so a
// fleet-wide policy change is one edit rather than N — and so that the
// policy cannot drift between roles, which it does immediately once it is
// duplicated.
//
// Prompts are gated artefacts. The mandatory clauses declared in config must
// appear in every role prompt, and the gate checks it. That is what stops a
// run deleting a safety clause from its own instructions: a fresh session with
// no memory will re-derive a design from first principles, violate a
// constraint nobody restated, and then defend the violation coherently.
package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/CyborgShadow/ADLC/internal/config"
)

// Prompt is one role's instructions, as stored.
type Prompt struct {
	ID      string
	Version string
	Path    string
	Front   map[string]string
	Body    string
}

// SHA is the full digest of the file's normalised bytes. It doubles as the
// content address of the retained prompt, so it is never truncated.
func (p Prompt) SHA() string { return digest(p.Body) }

// Library is a directory of prompts plus the shared preamble.
//
// One process holds one library and both the scheduler and the dashboard are
// given it — `adlc schedule run --serve` runs them side by side. So a prompt
// edited from the dashboard is written while a dispatch may be assembling one,
// and a Go map read during a write is not a stale read, it is a crash. Every
// access to the prompt set therefore goes through mu.
type Library struct {
	Dir      string
	Preamble string
	// mu guards Preamble and prompts. Callers never hold it: they call methods.
	mu        sync.RWMutex
	preambleP string
	prompts   map[string]Prompt
}

// Load reads a prompt library.
func Load(pol config.PromptPolicy) (*Library, error) {
	lib := &Library{Dir: pol.Dir, prompts: map[string]Prompt{}}
	if pol.PreambleFile != "" {
		b, err := os.ReadFile(pol.PreambleFile)
		if err != nil {
			return nil, fmt.Errorf("preamble: %w", err)
		}
		lib.Preamble = string(config.StripBOM(b))
		lib.preambleP = pol.PreambleFile
	}
	entries, err := os.ReadDir(pol.Dir)
	if err != nil {
		return nil, fmt.Errorf("prompt dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p, err := readPrompt(filepath.Join(pol.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if _, dup := lib.prompts[p.ID]; dup {
			return nil, fmt.Errorf("two prompts declare id %q; a pin that resolves to two files resolves to neither", p.ID)
		}
		lib.prompts[p.ID] = p
	}
	return lib, nil
}

func readPrompt(path string) (Prompt, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Prompt{}, err
	}
	text := strings.ReplaceAll(string(config.StripBOM(b)), "\r\n", "\n")
	p := Prompt{Path: path, Front: map[string]string{}, Body: text}

	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			head := text[4 : 4+end]
			p.Body = strings.TrimPrefix(text[4+end+4:], "\n")
			for _, line := range strings.Split(head, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				p.Front[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
			}
		}
	}
	p.ID = p.Front["id"]
	p.Version = p.Front["version"]
	if p.ID == "" {
		p.ID = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if p.Version == "" {
		p.Version = "v1"
	}
	return p, nil
}

// Get returns one prompt, as the file reads now.
func (l *Library) Get(id string) (Prompt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reloadPrompt(id)
	return l.get(id)
}

// reloadPrompt re-reads one role prompt from its file. The write lock is held.
//
// The file is the definition, and one process holds one library for hours:
// `adlc schedule run --serve` loads it at startup and dispatches from it all
// day. Read the loaded snapshot and a prompt change merged onto the trunk at
// noon reaches no run until somebody restarts the scheduler — reviewed,
// committed, live everywhere except in the fleet that is actually reading it,
// and with nothing on any surface saying so.
//
// A file that cannot be read now keeps the copy already loaded. A prompt is
// briefly absent during a checkout, and refusing to dispatch then would trade
// a slightly stale prompt for no prompt at all. A file that appears in the
// directory after Load is not picked up either: nothing can dispatch it until
// the config that names it is reloaded too, which is a restart regardless.
func (l *Library) reloadPrompt(id string) {
	p, ok := l.prompts[id]
	if !ok {
		return
	}
	fresh, err := readPrompt(p.Path)
	if err != nil {
		return
	}
	// A file caught mid-write reads cleanly, so a nil error is not enough. This
	// package's own writeFile goes to temp file, fsync and rename precisely
	// because a half-written prompt is the hazard; a `git merge` rewriting the
	// prompt directory in the tree a serving library re-reads gives the reader
	// no such guarantee, and re-reading at dispatch would otherwise turn a
	// once-per-restart exposure into a once-per-dispatch one.
	//
	// An empty body is the truncate-then-write window. Taking it dispatches an
	// agent with the preamble and no job — the failure SetPrompt refuses in
	// those words — and silently reverts the version the run is recorded under.
	if strings.TrimSpace(fresh.Body) == "" {
		return
	}
	// Front matter the loaded copy had and the fresh read has lost is an opened
	// fence that is not closed yet: readPrompt hands the whole fragment back as
	// the body, and that fragment would be dispatched as the role's instructions.
	if len(p.Front) > 0 && len(fresh.Front) == 0 {
		return
	}
	// A truncation landing after the front matter still reads as a whole, short
	// prompt and cannot be told from one, so this narrows the window rather than
	// closing it. The gate's clause check is what catches a committed tree.
	//
	// Keyed by the id the library knows it under, not by the id the file now
	// declares: a file whose front matter changed id underneath us would
	// otherwise orphan the entry every worker resolves through.
	l.prompts[id] = fresh
}

// reloadAll re-reads every prompt and the preamble. The write lock is held.
func (l *Library) reloadAll() {
	l.reloadPreamble()
	for id := range l.prompts {
		l.reloadPrompt(id)
	}
}

// reloadPreamble re-reads the shared preamble. The write lock is held.
func (l *Library) reloadPreamble() {
	if l.preambleP == "" {
		return
	}
	b, err := os.ReadFile(l.preambleP)
	if err != nil {
		return
	}
	// An empty read is a file caught mid-write, not an emptied preamble:
	// SetPreamble refuses to write one, so nothing legitimate produces this
	// state. Taking it would leave Preamble empty, and Assemble then drops the
	// preamble and the visible seam with it — dispatching a run that carries no
	// fleet policy and none of the mandatory clauses it must not delete from its
	// own instructions, with no error on any surface.
	text := string(config.StripBOM(b))
	if strings.TrimSpace(text) == "" {
		return
	}
	// The same window a few bytes later: a fence opened and not yet closed.
	// reloadPrompt refuses this shape because readPrompt hands the fragment back
	// as the body; here it is worse, because nothing parses the preamble at all,
	// so every byte of the fragment is served as the whole fleet policy — a run
	// dispatched with none of the mandatory clauses it must not delete from its
	// own instructions, and Assemble returning no error to say so.
	//
	// A truncation landing after the front matter still reads as a whole, short
	// preamble and cannot be told from one, so this narrows the window rather
	// than closing it — as on the role path. The gate's clause check is what
	// catches a committed tree.
	if hasClosedFront(l.Preamble) && !hasClosedFront(text) {
		return
	}
	l.Preamble = text
}

// hasClosedFront reports whether text opens a front matter fence and closes it.
//
// It mirrors the fence test readPrompt parses with, but cannot share that code:
// readPrompt needs the closing index into text it has already normalised, while
// the preamble is stored with its line endings as read. So this tolerates both
// endings rather than assuming the caller normalised — a CRLF preamble would
// otherwise fail the test on every read, and a guard comparing the loaded copy
// against the fresh one would then be dead on the hosts that have them.
func hasClosedFront(s string) bool {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.HasPrefix(s, "---\n") && strings.Contains(s[4:], "\n---")
}

func (l *Library) get(id string) (Prompt, error) {
	p, ok := l.prompts[id]
	if !ok {
		return Prompt{}, fmt.Errorf("no prompt %q in %s", id, l.Dir)
	}
	return p, nil
}

// IDs lists the prompts in the library.
func (l *Library) IDs() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ids()
}

func (l *Library) ids() []string {
	out := make([]string, 0, len(l.prompts))
	for id := range l.prompts {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Assembled is the exact text a run is given.
type Assembled struct {
	PromptID string
	Version  string
	Text     string
	SHA      string
}

// Bytes are the exact bytes SHA addresses.
//
// They are normalised the same way the digest is, because the digest doubles
// as the content address of the retained prompt: storing the raw text under a
// hash of the normalised text means the retrieval silently fails, and the run
// then reports that its own prompt was never kept.
func (a Assembled) Bytes() []byte {
	return []byte(strings.ReplaceAll(a.Text, "\r\n", "\n"))
}

// Assemble joins the shared preamble to a role prompt.
//
// The seam is a literal `---` line. It matters that it is visible: a role
// prompt is written to be read starting at the seam, and the preamble above it
// is fleet policy that the role does not restate and cannot override.
func (l *Library) Assemble(promptID string, vars map[string]string) (Assembled, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Read the files rather than the snapshot Load took, so the text a run is
	// given is the text on disk at dispatch. See reloadPrompt.
	l.reloadPreamble()
	l.reloadPrompt(promptID)
	p, err := l.get(promptID)
	if err != nil {
		return Assembled{}, err
	}
	var b strings.Builder
	if l.Preamble != "" {
		b.WriteString(strings.TrimRight(l.Preamble, "\n"))
		b.WriteString("\n\n---\n\n")
	}
	b.WriteString(p.Body)
	text := b.String()
	for k, v := range vars {
		text = strings.ReplaceAll(text, "{{"+k+"}}", v)
	}
	return Assembled{PromptID: p.ID, Version: p.Version, Text: text, SHA: digest(text)}, nil
}

// ClauseFinding is a prompt missing a mandatory clause.
type ClauseFinding struct {
	PromptID string
	Path     string
	Missing  string
}

// CheckClauses verifies every prompt still carries the clauses config declares
// mandatory, and that the preamble does too.
//
// This runs in the gate rather than at dispatch on purpose. Checking at
// dispatch would refuse the run that noticed; checking in the gate refuses the
// commit that removed the clause, which is where the defect actually is.
func (l *Library) CheckClauses(clauses []string) []ClauseFinding {
	var out []ClauseFinding
	if len(clauses) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reloadAll()
	for _, id := range l.ids() {
		p := l.prompts[id]
		whole := l.Preamble + "\n" + p.Body
		for _, c := range clauses {
			if !strings.Contains(whole, c) {
				out = append(out, ClauseFinding{PromptID: id, Path: p.Path, Missing: c})
			}
		}
	}
	return out
}

// PreamblePath is where the shared preamble was read from.
func (l *Library) PreamblePath() string { return l.preambleP }

// PreambleSHA identifies the preamble bytes.
func (l *Library) PreambleSHA() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reloadPreamble()
	return digest(l.Preamble)
}

// PreambleText reads the shared preamble under the lock. Prefer it to the
// exported field anywhere the preamble might be edited while it is read.
func (l *Library) PreambleText() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reloadPreamble()
	return l.Preamble
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(s, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}
