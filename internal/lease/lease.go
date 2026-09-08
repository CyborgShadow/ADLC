// Package lease is the ephemeral claim store: who is working on what, right
// now, and which machines they are touching while they do it.
//
// Three things about it are deliberate and each was paid for.
//
// It is never committed. Coordination state has a lifetime of hours and
// belongs in files with a TTL.
//
// The key is the work item's own id, canonicalised — never a phrase an agent
// composes. The item id is the one string two runs could both have written
// without coordinating, and nothing had asked either of them for it.
//
// It leases RESOURCES as well as items. Two agents editing one file produce a
// merge conflict, which is loud and cheap. Two agents patching one host
// produce an outage.
package lease

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Lease is one live claim.
type Lease struct {
	Key        string   `json:"key"`
	RunID      string   `json:"run_id"`
	Worker     string   `json:"worker"`
	Resources  []string `json:"resources"`
	Subject    string   `json:"subject"`
	AcquiredMS int64    `json:"acquired_ms"`
	TTLMinutes int      `json:"ttl_minutes"`
}

// Expired reports whether a lease has outlived its TTL. An expired lease is
// swept rather than honoured: a crashed run must not hold a host forever.
func (l Lease) Expired(now time.Time) bool {
	return now.After(time.UnixMilli(l.AcquiredMS).Add(time.Duration(l.TTLMinutes) * time.Minute))
}

// Store is a directory of leases.
type Store struct {
	Dir         string
	TTLMinutes  int
	StripTokens []string
	Now         func() time.Time
}

// Outcome is why an acquisition was refused, or that it was granted.
type Outcome struct {
	Granted bool
	// Conflict is the live lease standing in the way.
	Conflict *Lease
	// Reason is one of "held", "resource", or "" when granted.
	Reason string
	Detail string
	// Advisory carries a non-blocking warning: a subject-token overlap with a
	// sibling under a different key.
	//
	// It is advisory ON PURPOSE. A false refusal costs one pick from a backlog
	// that always has more, so the bias is toward refusing — but a token shared
	// only between two free-text descriptions is reported rather than refused.
	Advisory string
}

// New opens a lease store.
func New(dir string, ttlMinutes int, strip []string) *Store {
	if ttlMinutes <= 0 {
		ttlMinutes = 180
	}
	return &Store{Dir: dir, TTLMinutes: ttlMinutes, StripTokens: strip, Now: time.Now}
}

var keyClean = regexp.MustCompile(`[^a-z0-9]+`)

// Canonical reduces every spelling of one id to a single key.
func Canonical(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	k = keyClean.ReplaceAllString(k, "-")
	return strings.Trim(k, "-")
}

func (s *Store) path(key string) string {
	return filepath.Join(s.Dir, Canonical(key)+".json")
}

// Acquire takes a lease, or explains who has it.
func (s *Store) Acquire(l Lease) (Outcome, error) {
	if strings.TrimSpace(l.Key) == "" {
		return Outcome{}, fmt.Errorf("a lease needs a key, and the key is the work item's id — a run either states the item it drains or declares itself untracked")
	}
	if strings.TrimSpace(l.RunID) == "" {
		return Outcome{}, fmt.Errorf("a lease needs a run id: a run wears two names, its workspace and its id, and they must be one string or every guard that keys on either goes blind")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Outcome{}, err
	}
	now := s.now()
	live, err := s.Live()
	if err != nil {
		return Outcome{}, err
	}
	canon := Canonical(l.Key)
	selfHeld := false

	for i := range live {
		other := live[i]
		if Canonical(other.Key) == canon {
			if other.RunID == l.RunID {
				// Re-entrant: the same run restating its own claim. It renews the TTL
				// rather than being refused, so a long run does not have to lose its lease
				// to keep working.
				selfHeld = true
				continue
			}
			return Outcome{Reason: "held", Conflict: &live[i], Detail: fmt.Sprintf(
				"run %s (%s) holds %s and it has not expired; pick something else",
				other.RunID, other.Worker, other.Key)}, nil
		}
		if other.RunID == l.RunID {
			continue // a run never conflicts with itself over its own resources
		}
		if a, b, ok := resourceOverlap(l.Resources, other.Resources); ok {
			return Outcome{Reason: "resource", Conflict: &live[i], Detail: fmt.Sprintf(
				"run %s (%s) holds %s, which touches %s; this run wants %s and the two overlap. Two runs editing one file conflict at merge; two runs changing one machine do not",
				other.RunID, other.Worker, other.Key, b, a)}, nil
		}
	}

	out := Outcome{Granted: true}
	if hint := s.subjectOverlap(l, live); hint != "" {
		out.Advisory = hint
	}

	l.AcquiredMS = now.UnixMilli()
	if l.TTLMinutes <= 0 {
		l.TTLMinutes = s.TTLMinutes
	}
	body, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return Outcome{}, err
	}
	// Exclusive create, so two dispatchers racing for one key cannot both win.
	// The one exception is a run renewing its own live claim, which overwrites.
	mode := os.O_CREATE | os.O_EXCL | os.O_WRONLY
	if selfHeld {
		mode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	}
	f, err := os.OpenFile(s.path(l.Key), mode, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return Outcome{Reason: "held", Detail: fmt.Sprintf("%s was taken between the check and the write", l.Key)}, nil
		}
		return Outcome{}, err
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return Outcome{}, err
	}
	return out, nil
}

// Release drops a lease. Releasing canonicalises the key too: a lease taken
// under one spelling and released under another leaks the claim.
func (s *Store) Release(key, runID string) error {
	p := s.path(key)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var l Lease
	if err := json.Unmarshal(b, &l); err == nil && runID != "" && l.RunID != runID {
		return fmt.Errorf("lease %s is held by run %s, not %s", key, l.RunID, runID)
	}
	return os.Remove(p)
}

// Live lists unexpired leases and sweeps the rest.
func (s *Store) Live() ([]Lease, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	now := s.now()
	var out []Lease
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(s.Dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var l Lease
		if err := json.Unmarshal(b, &l); err != nil {
			// An unreadable lease is swept rather than honoured forever. It cannot be
			// identified, so it cannot be released by its holder.
			os.Remove(p)
			continue
		}
		if l.Expired(now) {
			os.Remove(p)
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AcquiredMS < out[j].AcquiredMS })
	return out, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// resourceOverlap reports whether two resource sets touch.
//
// Resources are hierarchical, "/"-separated: fleet:prod,
// fleet:prod/host:web-01, region:us-east-1/fleet:prod. Two resources conflict
// when they are equal or when one is an ancestor of the other, so leasing a
// fleet locks its hosts and leasing a host is refused while the fleet is held.
// Equality alone would let a fleet-wide patch run underneath a single-host
// repair.
func resourceOverlap(a, b []string) (string, string, bool) {
	for _, x := range a {
		nx := normResource(x)
		if nx == "" {
			continue
		}
		for _, y := range b {
			ny := normResource(y)
			if ny == "" {
				continue
			}
			if nx == ny || isAncestor(nx, ny) || isAncestor(ny, nx) {
				return x, y, true
			}
		}
	}
	return "", "", false
}

func normResource(s string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(s)), "/")
}

func isAncestor(parent, child string) bool {
	return strings.HasPrefix(child, parent+"/")
}

// genericTokens are fleet vocabulary stripped before two subjects are
// compared. Without stripping, every pair of items collides on the words the
// project uses to describe all of its work.
var genericTokens = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true, "to": true,
	"in": true, "on": true, "for": true, "with": true, "is": true, "not": true, "that": true,
	"fix": true, "add": true, "update": true, "remove": true, "test": true, "tests": true,
	"check": true, "checks": true, "run": true, "runs": true, "item": true, "items": true,
	"work": true, "code": true, "build": true, "system": true, "make": true, "use": true,
}

func (s *Store) subjectOverlap(l Lease, live []Lease) string {
	mine := s.tokens(l.Subject)
	if len(mine) == 0 {
		return ""
	}
	for _, other := range live {
		if Canonical(other.Key) == Canonical(l.Key) {
			continue
		}
		for t := range s.tokens(other.Subject) {
			if mine[t] {
				return fmt.Sprintf(
					"advisory: run %s holds %s and its subject shares the distinctive word %q with this one. Not a refusal — check you are not about to do the same job twice",
					other.RunID, other.Key, t)
			}
		}
	}
	return ""
}

func (s *Store) tokens(text string) map[string]bool {
	strip := map[string]bool{}
	for k := range genericTokens {
		strip[k] = true
	}
	for _, t := range s.StripTokens {
		strip[strings.ToLower(t)] = true
	}
	out := map[string]bool{}
	for _, w := range regexp.MustCompile(`[^a-zA-Z0-9]+`).Split(strings.ToLower(text), -1) {
		if len(w) < 4 || strip[w] {
			continue
		}
		out[w] = true
	}
	return out
}
