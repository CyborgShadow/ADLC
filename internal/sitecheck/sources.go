package sitecheck

import (
	"fmt"
	"net/url"
	"strings"
)

// A pinnedSource is a citation somebody fetched. The table below is the whole
// point of this file: the page may cite these five and nothing else, so a
// reference that was never opened — a plausible author, a plausible year, a URL
// that resolves to nothing or to something else entirely — fails a command
// instead of surviving review because it reads like the four beside it.
//
// What proves a source was read is not a field here. A "fetched: true" column
// would be the table asserting something about itself, which is the same
// standing an invented citation has. The evidence is the envelope of the run
// that added the row: it records the URL requested, the HTTP status observed
// and the sentence in the source that supports the claim, on a record the run
// cannot edit afterwards. A later reader re-derives the citation from that
// rather than re-judging it.
type pinnedSource struct {
	Topic  string
	Author string
	Year   int

	// URL is absolute and https, and a DOI is pinned in its resolvable
	// https://doi.org/ form. A bare "10.1038/…" is not something a reader can
	// open and not something any run fetched, so it would pin a source nobody
	// read — the defect this table exists to prevent.
	URL string

	// Contested is what the source itself treats as unsettled, empty where the
	// source treats its subject as settled. The page marks one section
	// "fact uncertain"; a source pinned under it that asserted a settled
	// mechanism would put the page and its own citation in disagreement, and
	// neither surface would say so.
	Contested string
}

// pinnedSources is the allowlist: one entry per topic in requiredTopics, and
// no entry for anything else. Every URL here returned HTTP 200 to the run that
// added it.
var pinnedSources = []pinnedSource{
	{
		Topic:  "baby-faces",
		Author: "Borgi and Cirulli",
		Year:   2016,
		URL:    "https://pmc.ncbi.nlm.nih.gov/articles/PMC4782005/",
	},
	{
		Topic:  "purring",
		Author: "Russo, Schild and Knörnschild",
		Year:   2025,
		URL:    "https://pmc.ncbi.nlm.nih.gov/articles/PMC12695941/",
		// The source treats how the purr is driven as open, not the fact that
		// cats purr: it reports that a cat larynx produces purr frequencies
		// "without active neural input or muscle contraction", against the long
		// standing account of active muscle contraction. That is why this is the
		// topic the page states in a hedged voice.
		Contested: "how the purr is produced: the larynx can make purr frequencies with no neural input, so whether active muscle contraction drives it is open",
	},
	{
		Topic:  "oxytocin-touch",
		Author: "Nagasawa, Kimura, Masuda and Uchiyama",
		Year:   2023,
		URL:    "https://pmc.ncbi.nlm.nih.gov/articles/PMC10340037/",
	},
	{
		Topic:  "self-domestication",
		Author: "Hu and others",
		Year:   2014,
		URL:    "https://pmc.ncbi.nlm.nih.gov/articles/PMC3890806/",
	},
	{
		Topic:  "choosing-a-person",
		Author: "Turner",
		Year:   2021,
		URL:    "https://pmc.ncbi.nlm.nih.gov/articles/PMC8044293/",
	},
}

// canonicalSourceURL is the one definition of when two citations name the same
// document, read by the rule and by the table's own check. A trailing slash is
// not a different document; the rest of a URL is, so nothing else is folded —
// silently equating two paths that differ would let a citation point at a
// neighbouring article and still match.
func canonicalSourceURL(raw string) string {
	return strings.TrimSuffix(strings.TrimSpace(raw), "/")
}

// pinnedFor returns the pinned source a citation URL names.
func pinnedFor(raw string) (pinnedSource, bool) {
	want := canonicalSourceURL(raw)
	if want == "" {
		return pinnedSource{}, false
	}
	for _, p := range pinnedSources {
		if canonicalSourceURL(p.URL) == want {
			return p, true
		}
	}
	return pinnedSource{}, false
}

// pinnedURLs lists the allowlist in table order, for a finding that has to tell
// the reader what would have been accepted.
func pinnedURLs() []string {
	out := make([]string, 0, len(pinnedSources))
	for _, p := range pinnedSources {
		out = append(out, p.URL)
	}
	return out
}

// pinnedProblems reports what is wrong with a table of sources, one sentence
// per defect. It takes the table as an argument rather than reading the package
// variable so that the test holding the real table to the criteria also has a
// firing case: a check that only ever sees the good table passes vacuously the
// day it stops checking anything.
func pinnedProblems(list []pinnedSource) []string {
	var out []string

	byTopic := map[string][]pinnedSource{}
	required := map[string]bool{}
	for _, want := range requiredTopics {
		required[want] = true
	}
	for _, p := range list {
		byTopic[p.Topic] = append(byTopic[p.Topic], p)
		if !required[p.Topic] {
			out = append(out, fmt.Sprintf("topic %q is pinned but is not one of the topics the page covers", p.Topic))
		}
	}
	for _, want := range requiredTopics {
		switch n := len(byTopic[want]); {
		case n == 0:
			out = append(out, fmt.Sprintf("topic %q has no pinned source, so a claim about it can cite anything", want))
		case n > 1:
			out = append(out, fmt.Sprintf("topic %q has %d pinned sources, so which one a claim was checked against is unanswerable", want, n))
		}
	}

	for _, p := range list {
		where := fmt.Sprintf("the %q entry", p.Topic)
		if strings.TrimSpace(p.Author) == "" {
			out = append(out, where+" names no author")
		}
		if p.Year == 0 {
			out = append(out, where+" names no year")
		}
		out = append(out, urlProblems(where, p.URL)...)
	}
	return out
}

// urlProblems is the "absolute https" half, separated because it is the half a
// plausible-looking citation fails: a relative path, a bare DOI or an http URL
// all read like a source and name nothing anybody can open over a link that
// cannot be tampered with in transit.
func urlProblems(where, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{where + " names no URL"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return []string{fmt.Sprintf("%s has a URL that does not parse: %q", where, raw)}
	}
	var out []string
	if u.Scheme != "https" {
		out = append(out, fmt.Sprintf("%s has URL %q, which is not https", where, raw))
	}
	if u.Host == "" {
		out = append(out, fmt.Sprintf("%s has URL %q, which is not absolute", where, raw))
	}
	return out
}
