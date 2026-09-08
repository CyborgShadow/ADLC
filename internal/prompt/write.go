package prompt

// Writing a prompt back.
//
// The file is the definition, so editing a role has to mean editing the file —
// anything that kept the operator's edit somewhere else would give the fleet
// two prompts for one role and no way to tell which one a run was given.
//
// Two properties are load-bearing here. The write is atomic, because a role
// prompt truncated halfway through dispatches an agent with half its
// instructions and no error anywhere. And the in-memory copy is replaced in
// the same call, because the dispatcher assembles from memory: a write that
// only touched the disk would leave every run in this process still using the
// old text, while the dashboard showed the new one.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MissingClauses reports which of clauses do not appear verbatim in text.
//
// It is the same containment test the gate uses, exposed so an editor can run
// it before writing rather than after committing. Checking afterwards tells
// you a safety rule is gone; checking first stops it going.
func MissingClauses(text string, clauses []string) []string {
	whole := normalise(text)
	var out []string
	for _, c := range clauses {
		if c == "" {
			continue
		}
		if !strings.Contains(whole, c) {
			out = append(out, c)
		}
	}
	return out
}

// SetPrompt replaces one role prompt's body and writes it to its file.
//
// Only the body is replaceable. The front matter carries the id a pin resolves
// through and the version a past run recorded, so an editor that could rewrite
// it could rename a role out from under the config or make an old run's
// recorded version point at text it was never given.
func (l *Library) SetPrompt(id, body string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, err := l.get(id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("a role prompt cannot be empty: %s would be dispatched with the preamble and no job", id)
	}
	raw, err := os.ReadFile(p.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", p.Path, err)
	}
	text := head(normalise(string(raw))) + normalise(body)
	if err := writeFile(p.Path, text); err != nil {
		return err
	}
	fresh, err := readPrompt(p.Path)
	if err != nil {
		return err
	}
	// Keyed by the id the library knows it under, not by the id the file now
	// declares: those are the same here because the front matter was preserved,
	// and keying by the file would silently orphan the entry if that ever stops
	// being true.
	l.prompts[id] = fresh
	return nil
}

// SetPreamble replaces the shared preamble and writes it to its file.
//
// One edit here reaches every role, which is the point of storing it once —
// and also why the caller is expected to have checked the mandatory clauses
// against every role first. A clause the preamble carried for a role that does
// not restate it disappears from that role the moment this returns.
func (l *Library) SetPreamble(text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.preambleP == "" {
		return fmt.Errorf("this project declares no preamble file, so there is nothing to write")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("the preamble cannot be emptied: it is the only fleet-wide policy every role is given")
	}
	if err := writeFile(l.preambleP, normalise(text)); err != nil {
		return err
	}
	l.Preamble = normalise(text)
	// The preamble file usually also parses as a prompt in its own directory.
	// Refresh that entry too, so the library does not hold two versions of the
	// same bytes under two names.
	for id, p := range l.prompts {
		if p.Path == l.preambleP {
			if fresh, err := readPrompt(p.Path); err == nil {
				l.prompts[id] = fresh
			}
		}
	}
	return nil
}

// head returns everything up to and including the front matter's closing
// fence, plus the newline that separates it from the body. It mirrors the
// split readPrompt performs, so a round trip through SetPrompt is byte-stable.
func head(text string) string {
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return ""
	}
	return text[:4+end+4] + "\n"
}

// normalise makes line endings LF and guarantees a trailing newline. Both
// matter: the digest normalises the same way, and a prompt whose last line has
// no newline concatenates with whatever is appended after it.
func normalise(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// writeFile replaces a file's contents atomically. A reader either sees the
// whole old file or the whole new one, never a prefix of either.
func writeFile(path, text string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Preserve whatever mode the file already had; a fresh temp file is 0600,
	// and a prompt only the dashboard's user can read breaks the CLI gate.
	if fi, err := os.Stat(path); err == nil {
		_ = os.Chmod(name, fi.Mode().Perm())
	} else {
		_ = os.Chmod(name, 0o644)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
