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
type Library struct {
	Dir       string
	Preamble  string
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

// Get returns one prompt.
func (l *Library) Get(id string) (Prompt, error) {
	p, ok := l.prompts[id]
	if !ok {
		return Prompt{}, fmt.Errorf("no prompt %q in %s", id, l.Dir)
	}
	return p, nil
}

// IDs lists the prompts in the library.
func (l *Library) IDs() []string {
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
	p, err := l.Get(promptID)
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
	for _, id := range l.IDs() {
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
func (l *Library) PreambleSHA() string { return digest(l.Preamble) }

func digest(s string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(s, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}
