// Package envelope is the contract a worker's output has to satisfy.
//
// An envelope is a DECLARATION, never evidence. Everything in it that could be
// checked is checked: the control plane runs the commands itself and compares.
// The distinction is the reason this package exists as a parser rather than as
// a source of truth, and it is worth stating in the type system's own terms
// — nothing here returns a verdict, only what a worker claimed one was.
package envelope

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Version is the envelope schema this build accepts. An unversioned payload
// cannot be rejected when it stops matching, so it gets misread instead.
const Version = "1"

// Envelope is one worker run's output.
type Envelope struct {
	Version    string `json:"envelope_version"`
	RunID      string `json:"run_id"`
	WorkerType string `json:"worker_type"`
	SegmentID  string `json:"segment_id"`
	ItemID     string `json:"work_item_id"`

	// Verdict is the worker's own summary of how the run went. It is never the
	// transition: proposing and deciding are different jobs.
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`

	// HeadSHA is the commit the work claims to describe.
	HeadSHA string `json:"head_sha"`
	// Artifact is the digest of the thing a behavioural check ran against — an
	// image id, a plan hash, a machine state fingerprint. A commit says what the
	// source was; it says nothing about what was built or applied from it, and
	// for infrastructure that gap is the whole risk.
	Artifact string `json:"artifact,omitempty"`
	// PlanDigest is the hash of the dry-run output an approval approves.
	PlanDigest string `json:"plan_digest,omitempty"`

	Transition *Transition `json:"state_transition,omitempty"`
	Commands   []Command   `json:"commands_run"`
	Outputs    Outputs     `json:"outputs"`
	Questions  []Question  `json:"questions,omitempty"`

	Model string `json:"model,omitempty"`
	Usage Usage  `json:"usage"`

	// Environment failure is how a run says the blocker is the machine, not the
	// work. Kept distinct from a question so that a capability outage reaches an
	// operator instead of queueing behind decisions.
	EnvironmentFailure string `json:"environment_failure,omitempty"`

	raw        []byte
	normalised []string
}

// Transition is the state change a worker proposes.
type Transition struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Evidence []string `json:"evidence,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// Command is a worker's account of something it ran.
//
// CheckID joins the account to a declared check, so the comparison is by
// identity rather than by substring-matching a command line. Matching on the
// string is what allowed an agent to satisfy a gate with a command that shared
// a prefix with the real one and ran nothing.
type Command struct {
	CheckID  string `json:"check_id,omitempty"`
	Cmd      string `json:"cmd"`
	ExitCode int    `json:"exit_code"`
	// OutputTail matters for any check whose verdict is its output rather than
	// its exit code, and is compared for those.
	OutputTail string `json:"output_tail,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// NotRun is how a worker reports honestly that it could not run a check. It
	// must be expressible: a claim matcher that punishes honesty about a command
	// that did not run teaches workers to omit the line instead.
	NotRun bool   `json:"not_run,omitempty"`
	Why    string `json:"why,omitempty"`
}

// Outputs is the structured result, by worker role.
type Outputs struct {
	FilesChanged []string     `json:"files_changed,omitempty"`
	Criteria     criteriaList `json:"criteria,omitempty"`
	Findings     findingsList `json:"findings,omitempty"`
	Notes        string       `json:"notes_md,omitempty"`
	Deferred     []string     `json:"deferred,omitempty"`
	// WorkItems is what a planning run produces. They are PROPOSALS: the control
	// plane admits or refuses each one and records both answers. An agent that
	// could create work items directly would be an agent that could invent its
	// own scope.
	WorkItems []ProposedItem `json:"work_items,omitempty"`
}

// Criterion is a verifier's verdict on one acceptance criterion.
type Criterion struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"` // pass | fail | untested
	// CommandIndex points into Commands. A criterion passed on inspection alone
	// is not passed; it must cite an executed command.
	CommandIndex int    `json:"command_index"`
	Evidence     string `json:"evidence"`
}

// Finding is a validator's defect.
type Finding struct {
	Severity  string `json:"severity"` // blocker | major | minor | note
	Location  string `json:"location"`
	Criterion string `json:"criterion,omitempty"`
	Evidence  string `json:"evidence"`
	Required  string `json:"required_change"`
}

// ProposedItem is one work item a planning run proposes.
type ProposedItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Area      string   `json:"area"`
	Radius    string   `json:"blast_radius"`
	Resources []string `json:"resources,omitempty"`
	FileScope []string `json:"file_scope,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	// Criteria are the specification of record. An item with none cannot be
	// verified by anyone, so it cannot be finished either.
	Criteria  []string `json:"criteria"`
	Rationale string   `json:"rationale,omitempty"`
}

// Question is a decision the run could not make for itself.
type Question struct {
	ID       string `json:"id,omitempty"`
	Blocking bool   `json:"blocking"`
	Text     string `json:"text"`
	Lean     string `json:"lean"`
	Evidence string `json:"evidence"`
}

// Usage is the token accounting for the run.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// ErrMalformed marks a payload that is not a usable envelope. A malformed
// envelope is a refusable proposal, which can be retried — not a dead run.
type ErrMalformed struct{ Detail string }

func (e ErrMalformed) Error() string { return "malformed envelope: " + e.Detail }

// Parse reads an envelope from raw bytes.
//
// It is deliberately forgiving about encoding and unforgiving about meaning: a
// BOM, CRLF line endings and leading whitespace are stripped, and everything
// after that must be exactly right.
func Parse(raw []byte) (*Envelope, error) {
	clean := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	clean = bytes.TrimSpace(clean)
	if len(clean) == 0 {
		return nil, ErrMalformed{Detail: "empty"}
	}
	if !utf8.Valid(clean) {
		return nil, ErrMalformed{Detail: "not valid UTF-8"}
	}
	// An agent that wraps its JSON in a fenced code block has still produced the
	// envelope; refusing that costs a whole run to punish formatting.
	clean = unfence(clean)

	var e Envelope
	dec := json.NewDecoder(bytes.NewReader(clean))
	if err := dec.Decode(&e); err != nil {
		return nil, ErrMalformed{Detail: err.Error()}
	}
	e.raw = clean
	// Shape before meaning: an agent that wrote the right facts in a shape the
	// schema did not declare has still done the work, and refusing it costs a
	// whole run to punish formatting. normalise re-shapes and never invents.
	e.normalise()
	if err := e.validate(); err != nil {
		return nil, err
	}
	return &e, nil
}

func unfence(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "```") {
		return b
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return []byte(strings.TrimSpace(s))
}

func (e *Envelope) validate() error {
	if e.Version != Version {
		return ErrMalformed{Detail: fmt.Sprintf("envelope_version %q; this build accepts %q", e.Version, Version)}
	}
	if e.RunID == "" {
		return ErrMalformed{Detail: "run_id is required — a run with no id cannot be attributed, collision-checked or resumed"}
	}
	if e.WorkerType == "" {
		return ErrMalformed{Detail: "worker_type is required"}
	}
	switch e.Verdict {
	case "pass", "fail", "reject", "blocked":
	default:
		return ErrMalformed{Detail: fmt.Sprintf("verdict %q is not one of pass|fail|reject|blocked", e.Verdict)}
	}
	if e.Transition != nil {
		if e.Transition.From == "" || e.Transition.To == "" {
			return ErrMalformed{Detail: "state_transition needs both from and to"}
		}
	}
	for i, c := range e.Criteria() {
		switch c.Status {
		case "pass", "fail", "untested":
		default:
			return ErrMalformed{Detail: fmt.Sprintf("outputs.criteria[%d].status %q is not pass|fail|untested", i, c.Status)}
		}
	}
	for i, f := range e.Outputs.Findings {
		switch f.Severity {
		case "blocker", "major", "minor", "note":
		default:
			return ErrMalformed{Detail: fmt.Sprintf("outputs.findings[%d].severity %q is not blocker|major|minor|note", i, f.Severity)}
		}
	}
	return nil
}

// Criteria returns the per-criterion verdicts.
func (e *Envelope) Criteria() []Criterion { return e.Outputs.Criteria }

// Raw returns the normalised bytes the envelope was parsed from.
func (e *Envelope) Raw() []byte { return e.raw }

// SHA is the digest of those bytes, recorded on the run so that what the
// control plane read can be identified later.
func (e *Envelope) SHA() string {
	sum := sha256.Sum256(e.raw)
	return hex.EncodeToString(sum[:])
}

// Claim returns what the envelope says about a check, which is its LAST run of
// it.
//
// An agent that runs a check, sees it fail, fixes the cause and runs it again
// records both — and it should: the working is the evidence. But the claim it
// is making is about the tree it is proposing, and that is the final run. Taking
// the first one compared the gate's observation of the tree as it IS against
// the agent's note of the tree as it WAS, found them different, and refused the
// proposal for claim_discrepancy.
//
// That punished an agent for showing its work, which is precisely backwards:
// the alternative it teaches is to record only the run that passed, and an
// envelope edited down to its good news is the thing this matcher exists to
// catch.
func (e *Envelope) Claim(checkID string) (Command, bool) {
	var last Command
	found := false
	for _, c := range e.Commands {
		if c.CheckID == checkID {
			last, found = c, true
		}
	}
	return last, found
}

// Blockers returns the blocker-severity findings.
func (e *Envelope) Blockers() []Finding {
	var out []Finding
	for _, f := range e.Outputs.Findings {
		if f.Severity == "blocker" {
			out = append(out, f)
		}
	}
	return out
}

// BlockingQuestion returns the first blocking question, if any.
func (e *Envelope) BlockingQuestion() (Question, bool) {
	for _, q := range e.Questions {
		if q.Blocking {
			return q, true
		}
	}
	return Question{}, false
}

// FailedCriteria returns criteria the worker reported as failing.
func (e *Envelope) FailedCriteria() []Criterion {
	var out []Criterion
	for _, c := range e.Criteria() {
		if c.Status == "fail" {
			out = append(out, c)
		}
	}
	return out
}
