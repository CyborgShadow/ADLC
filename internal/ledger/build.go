package ledger

import (
	"runtime/debug"
	"strings"
	"sync"
)

// This file answers one question about every row on the chain: which build of
// the control plane wrote it.
//
// Without it, a decision on the record can be read but not attributed. "The
// authority admitted this six weeks ago" is only re-derivable if you know
// which authority — the rules change, and a replay that disagrees is either a
// changed rule or a changed record, which are not the same problem. The
// revision is what tells those two apart.
//
// The one rule the rest of this file exists to enforce: absence is UNKNOWN and
// never a known value. A binary built without VCS stamping cannot say what it
// was built from, and recording an empty string that later renders as "same as
// this one" would turn "I do not know" into a claim.

// RevisionUnknown is what the record says when a build could not tell which
// revision it was — `go run`, an unstamped build, or a row written before this
// field existed. It is a value, not an empty string, so that a surface printing
// it has to print something a person will read as an absence.
const RevisionUnknown = "unknown"

// dirtySuffix marks a revision read from a tree that had uncommitted changes
// when it was built. The commit is known; the build is not, because two
// binaries stamped with the same sha and a modified tree can be different
// programs. So it is recorded — throwing it away would record a precision the
// build never had — and SameBuild refuses to call it a match.
const dirtySuffix = "+dirty"

var (
	buildRevisionOnce sync.Once
	buildRevisionVal  string
)

// BuildRevision is this build's own revision, read once from the build info the
// toolchain stamped in.
func BuildRevision() string {
	buildRevisionOnce.Do(func() { buildRevisionVal = revisionOf(debug.ReadBuildInfo()) })
	return buildRevisionVal
}

// revisionOf extracts the revision from build info.
//
// It takes what debug.ReadBuildInfo returns rather than calling it, because the
// case that matters most here — a build with no VCS stamp at all — cannot be
// produced from inside a test otherwise, and a rule about absence that is only
// reasoned about is a rule nobody has checked.
func revisionOf(bi *debug.BuildInfo, ok bool) string {
	if !ok || bi == nil {
		return RevisionUnknown
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = strings.TrimSpace(s.Value)
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return RevisionUnknown
	}
	if dirty {
		return rev + dirtySuffix
	}
	return rev
}

// RevisionOf normalises a revision read back off the chain. An empty column is
// a row from a build older than this field, and it reads as UNKNOWN — the same
// answer as a build that could not stamp itself, because from here they are
// the same fact: nobody recorded it.
func RevisionOf(stored string) string {
	if strings.TrimSpace(stored) == "" {
		return RevisionUnknown
	}
	return stored
}

// KnownRevision reports whether a recorded revision identifies one build.
func KnownRevision(rev string) bool {
	r := RevisionOf(rev)
	return r != RevisionUnknown && !strings.HasSuffix(r, dirtySuffix)
}

// SameBuild reports whether two recorded revisions are KNOWN to be the same
// build. Two unknowns are not a match, and neither are two dirty trees at the
// same commit: the honest answer to "was this the same build?" when nothing
// was recorded is no, not yes.
func SameBuild(a, b string) bool {
	return KnownRevision(a) && KnownRevision(b) && RevisionOf(a) == RevisionOf(b)
}

// ShortRevision renders a revision for a person: enough of the sha to look it
// up, and the whole word when there is no sha to shorten.
func ShortRevision(rev string) string {
	r := RevisionOf(rev)
	dirty := ""
	if strings.HasSuffix(r, dirtySuffix) {
		r, dirty = strings.TrimSuffix(r, dirtySuffix), dirtySuffix
	}
	if len(r) > 12 {
		r = r[:12]
	}
	return r + dirty
}
