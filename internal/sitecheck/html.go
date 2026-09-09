package sitecheck

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// requiredTopics are the five the page has to cover. They are listed rather
// than counted: "five sections" is satisfied by the same topic five times, and
// what the page promises is five different things.
var requiredTopics = []string{
	"baby-faces",
	"purring",
	"oxytocin-touch",
	"self-domestication",
	"choosing-a-person",
}

const minFactSections = 5

// hedges are what an uncertain claim has to say out loud. A page that states a
// contested finding in the same voice as a settled one is the failure this
// rule exists for.
var hedges = []string{"maybe", "not sure"}

func factSections(f *htmlFile) []*node {
	return f.root.findAll(func(n *node) bool { return n.tag == "section" && n.hasClass("fact") })
}

func checkHTMLFactCount(s *site) []Finding {
	var out []Finding
	if len(s.htmls) == 0 {
		return []Finding{{RuleID: "html.fact-count", Path: ".",
			Detail: "no HTML files: a page with nothing on it has not passed the page checks, it has skipped them"}}
	}
	for _, f := range s.htmls {
		if n := len(factSections(f)); n < minFactSections {
			out = append(out, Finding{RuleID: "html.fact-count", Path: f.path,
				Detail: fmt.Sprintf("%d section.fact, fewer than the %d the page promises", n, minFactSections)})
		}
	}
	return out
}

// checkHTMLFactParts requires exactly one of each, not at least one. Two
// p.little in a section means the short reading and the long reading of the
// page disagree about which one is the short version.
func checkHTMLFactParts(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		for i, sec := range factSections(f) {
			where := fmt.Sprintf("section.fact #%d (data-topic %q)", i+1, sec.attrs["data-topic"])
			for _, part := range []string{"little", "big"} {
				got := sec.findAll(func(n *node) bool { return n.tag == "p" && n.hasClass(part) })
				if len(got) != 1 {
					out = append(out, Finding{RuleID: "html.fact-parts", Path: f.path,
						Detail: fmt.Sprintf("%s has %d p.%s, want exactly 1", where, len(got), part)})
				}
			}
		}
	}
	return out
}

func checkHTMLTopics(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		secs := factSections(f)
		if len(secs) == 0 {
			continue // html.fact-count owns the empty page; two rules on one defect is noise
		}
		seen := map[string]bool{}
		for _, sec := range secs {
			topic := strings.TrimSpace(sec.attrs["data-topic"])
			if topic == "" {
				out = append(out, Finding{RuleID: "html.topics", Path: f.path,
					Detail: "a section.fact carries no data-topic, so what it covers cannot be checked"})
				continue
			}
			seen[topic] = true
		}
		var missing []string
		for _, want := range requiredTopics {
			if !seen[want] {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			out = append(out, Finding{RuleID: "html.topics", Path: f.path,
				Detail: "data-topic does not cover " + strings.Join(missing, ", ")})
		}
	}
	return out
}

const sourcesSectionID = "sources"

// sourcesSection, sourceEntryList and sourceEntries are the one definition of
// what the citation rules read, because three rules resolve a citation and a
// second definition would let them disagree about what an entry is:
// html.data-source would report a reference as resolved while
// html.source-pinned reported the entry it resolved to as absent, and a reader
// would have to guess which was right.
func sourcesSection(f *htmlFile) *node {
	for _, n := range f.root.tags("section") {
		if n.attrs["id"] == sourcesSectionID {
			return n
		}
	}
	return nil
}

// sourceEntryList is every entry in the sources section in document order,
// duplicates included. An entry is anything with an id, not specifically an
// <li>, so a list rewritten as a <dl> or a set of <p> stays checked rather than
// quietly stopping being a source list.
func sourceEntryList(sources *node) []*node {
	if sources == nil {
		return nil
	}
	return sources.findAll(func(n *node) bool { return n.attrs["id"] != "" })
}

// sourceEntries maps each id to the entry carrying it, keeping the first where
// an id repeats. This is the resolution view and only the resolution view: a
// data-source names one id and has to resolve to one entry. A rule asking what
// the list cites must read sourceEntryList instead, because an entry dropped
// here is one whose URL no rule would ever see — copying an <li> to add a
// citation keeps the id it was copied from, and that is how an invented source
// arrives in a real page.
func sourceEntries(sources *node) map[string]*node {
	out := map[string]*node{}
	for _, n := range sourceEntryList(sources) {
		if _, taken := out[n.attrs["id"]]; !taken {
			out[n.attrs["id"]] = n
		}
	}
	return out
}

// repeatedEntryIDs names the ids declared more than once, in the document order
// of the second declaration, so two runs over a page report the same findings
// in the same order.
func repeatedEntryIDs(entries []*node) []string {
	count := map[string]int{}
	var out []string
	for _, e := range entries {
		id := e.attrs["id"]
		count[id]++
		if count[id] == 2 {
			out = append(out, id)
		}
	}
	return out
}

// sourceCitation is one link inside the sources list, paired with the entry it
// sits in. The pair matters because the finding has to send a reader to a line:
// the URL says what is wrong and the id says where it is.
type sourceCitation struct {
	entryID string // "" for a link inside no entry
	url     string
}

func (c sourceCitation) where() string {
	if c.entryID == "" {
		return "a link in section#" + sourcesSectionID + " that sits inside no entry"
	}
	return "source #" + c.entryID
}

// sourceCitations is every link inside the sources section, not one per entry.
// Checking one URL per entry asks a question the page can answer twice: an
// entry carrying a pinned link and then an invented one passes, the same two
// links in the opposite order fail, and whether a citation nobody opened is
// refused comes down to where in the <li> it sits.
func sourceCitations(sources *node) []sourceCitation {
	if sources == nil {
		return nil
	}
	var out []sourceCitation
	var rec func(n *node, entryID string)
	rec = func(n *node, entryID string) {
		if n.tag == "" {
			return
		}
		if id := n.attrs["id"]; id != "" {
			entryID = id
		}
		if href, ok := n.attrs["href"]; ok {
			out = append(out, sourceCitation{entryID: entryID, url: strings.TrimSpace(href)})
		}
		for _, c := range n.children {
			rec(c, entryID)
		}
	}
	// From the children, so the section's own id="sources" is not read as an
	// entry id.
	for _, c := range sources.children {
		rec(c, "")
	}
	return out
}

// entryURL is the document a source entry points at. A citation is a link:
// without one the entry is a name and a year, which is exactly what an invented
// citation also has.
func entryURL(entry *node) string {
	if href, ok := entry.attrs["href"]; ok {
		return strings.TrimSpace(href)
	}
	for _, n := range entry.findAll(func(n *node) bool { _, ok := n.attrs["href"]; return ok }) {
		return strings.TrimSpace(n.attrs["href"])
	}
	return ""
}

// citingNodes returns the elements inside a section that carry data-source,
// including the section itself. The page puts the citation on the <section>,
// but a claim cited on the paragraph that makes it is the same citation and has
// to be checked the same way.
func citingNodes(sec *node) []*node {
	var out []*node
	if _, ok := sec.attrs["data-source"]; ok {
		out = append(out, sec)
	}
	return append(out, sec.findAll(func(n *node) bool { _, ok := n.attrs["data-source"]; return ok })...)
}

// checkHTMLDataSource resolves every citation against the sources section. A
// data-source pointing at nothing is a citation that looks like evidence in
// the markup and is evidence of nothing on the page.
func checkHTMLDataSource(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		sources := sourcesSection(f)
		ids := sourceEntries(sources)
		cited := f.root.findAll(func(n *node) bool { _, ok := n.attrs["data-source"]; return ok })
		if len(cited) == 0 {
			continue
		}
		if sources == nil {
			out = append(out, Finding{RuleID: "html.data-source", Path: f.path,
				Detail: fmt.Sprintf("%d elements carry data-source but there is no section with id %q to resolve it against",
					len(cited), sourcesSectionID)})
			continue
		}
		for _, n := range cited {
			for _, ref := range strings.Fields(n.attrs["data-source"]) {
				if _, ok := ids[ref]; !ok {
					out = append(out, Finding{RuleID: "html.data-source", Path: f.path,
						Detail: fmt.Sprintf("<%s> cites data-source %q, which is not an id inside section#%s (ids there: %s)",
							n.tag, ref, sourcesSectionID, strings.Join(entryIDs(ids), ", "))})
				}
			}
		}
	}
	return out
}

// checkHTMLSourcePinned refuses a citation nobody fetched. Every other rule
// about the sources list asks whether a reference resolves inside the page,
// which an invented source satisfies for the price of adding an <li>: a
// plausible author, a plausible year and a URL that was never opened reads
// exactly like the four beside it. The allowlist in sources.go is the only
// thing that can tell them apart, because each entry there is a URL some run
// requested and recorded a status for.
//
// The rule reads the list rather than a collapsed view of it, and that is the
// whole of its correctness. It asks the same question three ways — every entry
// links somewhere, every link is pinned, no id is declared twice — because each
// of the other two shapes leaves a URL nobody opened sitting in the sources
// list with every declared command green over it.
func checkHTMLSourcePinned(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		sources := sourcesSection(f)
		if sources == nil {
			continue // html.data-source owns a page whose citations resolve to nothing
		}
		entries := sourceEntryList(sources)

		// A repeated id is refused rather than deduplicated, because
		// html.source-topic resolves a data-source to whichever entry came
		// first: with two, what a claim was checked against is a question about
		// document order rather than about the page.
		for _, id := range repeatedEntryIDs(entries) {
			out = append(out, Finding{RuleID: "html.source-pinned", Path: f.path,
				Detail: fmt.Sprintf("source #%s is declared twice, so which document a claim was checked against is unanswerable", id)})
		}

		for _, entry := range entries {
			if entryURL(entry) == "" {
				out = append(out, Finding{RuleID: "html.source-pinned", Path: f.path,
					Detail: fmt.Sprintf("source #%s links to nothing: a name and a year is what an invented citation has too", entry.attrs["id"])})
			}
		}

		for _, c := range sourceCitations(sources) {
			if c.url == "" {
				continue // the "links to nothing" finding owns an entry naming no document
			}
			if _, ok := pinnedFor(c.url); !ok {
				out = append(out, Finding{RuleID: "html.source-pinned", Path: f.path,
					Detail: fmt.Sprintf("%s cites %q, which is not a pinned source; the pinned ones are %s",
						c.where(), c.url, strings.Join(pinnedURLs(), ", "))})
			}
		}
	}
	return out
}

// checkHTMLSourceTopic refuses a real source attached to the wrong claim. That
// defect passes every check that only asks whether a reference resolves: the
// URL is one somebody fetched, the id exists in the sources list, and the
// citation still says nothing about the sentence it is attached to. What the
// allowlist adds is the topic each source was read for, so the page and the
// table can be held to agreeing about which claim it supports.
func checkHTMLSourceTopic(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		sources := sourcesSection(f)
		if sources == nil {
			continue // html.data-source owns that
		}
		entries := sourceEntries(sources)
		for _, sec := range factSections(f) {
			topic := strings.TrimSpace(sec.attrs["data-topic"])
			if topic == "" {
				continue // html.topics owns a section that names no topic
			}
			for _, n := range citingNodes(sec) {
				for _, ref := range strings.Fields(n.attrs["data-source"]) {
					entry, ok := entries[ref]
					if !ok {
						continue // html.data-source owns a reference that resolves to nothing
					}
					pinned, ok := pinnedFor(entryURL(entry))
					if !ok {
						continue // html.source-pinned owns a source nobody fetched
					}
					if pinned.Topic != topic {
						out = append(out, Finding{RuleID: "html.source-topic", Path: f.path,
							Detail: fmt.Sprintf("data-topic %q cites source %q, which was read for %q: a real source under the wrong claim resolves like a correct one",
								topic, ref, pinned.Topic)})
					}
				}
			}
		}
	}
	return out
}

// entryIDs orders a set of source ids, so two runs over the same page report
// the same findings in the same order.
func entryIDs(entries map[string]*node) []string {
	out := make([]string, 0, len(entries))
	for id := range entries {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func checkHTMLUncertain(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		if len(factSections(f)) == 0 {
			continue
		}
		found := false
		for _, sec := range factSections(f) {
			if !sec.hasClass("uncertain") {
				continue
			}
			for _, big := range sec.findAll(func(n *node) bool { return n.hasClass("big") }) {
				text := strings.ToLower(big.textContent())
				for _, h := range hedges {
					if strings.Contains(text, h) {
						found = true
					}
				}
			}
		}
		if !found {
			out = append(out, Finding{RuleID: "html.uncertain", Path: f.path,
				Detail: fmt.Sprintf("no section with class %q whose .big text says %s: a page that states every claim in the same voice hides which ones are contested",
					"fact uncertain", strings.Join(quoteAll(hedges), " or "))})
		}
	}
	return out
}

func checkHTMLImgAlt(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		for _, img := range f.root.tags("img") {
			if strings.TrimSpace(img.attrs["alt"]) == "" {
				out = append(out, Finding{RuleID: "html.img-alt", Path: f.path,
					Detail: fmt.Sprintf("<img src=%q> has no non-empty alt: to a screen reader that image is not there", img.attrs["src"])})
			}
		}
	}
	return out
}

// checkHTMLImgSrc resolves every img src against the tree that was walked. The
// defect is invisible to the two rules that already read a src: html.img-alt
// asks only that the alt text is non-empty, html.no-external-ref asks only that
// the src stays on this site, and a relative src naming a file that is not
// there satisfies both. What the reader gets is a broken-image icon with
// excellent alt text, and every command in the plan exits 0 over it.
func checkHTMLImgSrc(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		for _, img := range f.root.tags("img") {
			src := strings.TrimSpace(img.attrs["src"])
			if src == "" {
				out = append(out, Finding{RuleID: "html.img-src", Path: f.path,
					Detail: fmt.Sprintf("<img alt=%q> has no src, so it names no file to resolve", img.attrs["alt"])})
				continue
			}
			if schemeRef.MatchString(src) {
				continue // html.no-external-ref owns an off-site src; two rules on one defect is noise
			}
			target := resolveRef(f.path, src)
			if !s.files[target] {
				out = append(out, Finding{RuleID: "html.img-src", Path: f.path,
					Detail: fmt.Sprintf("<img src=%q> resolves to %q, which is not a file in the checked tree", src, target)})
			}
		}
	}
	return out
}

// resolveRef turns a reference on a page into a path in the checked tree. A
// leading "/" is the site root rather than the machine's, and the query and
// fragment are dropped because neither names a different file.
func resolveRef(page, ref string) string {
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	if strings.HasPrefix(ref, "/") {
		return path.Clean(strings.TrimPrefix(ref, "/"))
	}
	return path.Clean(path.Join(path.Dir(page), ref))
}

func checkHTMLViewport(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		found := false
		for _, m := range f.root.tags("meta") {
			if strings.EqualFold(m.attrs["name"], "viewport") {
				found = true
			}
		}
		if !found {
			out = append(out, Finding{RuleID: "html.viewport", Path: f.path,
				Detail: "no <meta name=\"viewport\">: without it a phone renders the page at desktop width and scales it down, so every font-size budget in the CSS is measuring nothing"})
		}
	}
	return out
}

func checkHTMLNoScript(s *site) []Finding {
	return bannedTag(s, "html.no-script", "script",
		"the page is meant to be readable with scripting off, and a <script> is the one element that cannot be")
}

func checkHTMLNoForm(s *site) []Finding {
	return bannedTag(s, "html.no-form", "form",
		"a static page with a <form> collects something, and there is nothing here to collect it with")
}

func bannedTag(s *site, ruleID, tag, why string) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		for range f.root.tags(tag) {
			out = append(out, Finding{RuleID: ruleID, Path: f.path, Detail: "<" + tag + "> present: " + why})
		}
	}
	return out
}

// schemeRef matches an absolute reference — a scheme, or the protocol-relative
// "//host" form. Both fetch from somewhere that is not this site.
var schemeRef = regexp.MustCompile(`^(?:[a-zA-Z][a-zA-Z0-9+.\-]*:|//)`)

// cssURLRef finds url(...) targets inside a stylesheet.
var cssURLRef = regexp.MustCompile(`url\(\s*['"]?([^'")]+)`)

// importRef finds @import targets in either of its two spellings.
var importRef = regexp.MustCompile(`@import\s+(?:url\(\s*['"]?([^'")]+)|['"]([^'"]+))`)

// checkHTMLNoExternalRef reads the stylesheets as well as the pages. @import
// and @font-face live in CSS but the defect is the page's: a page that fetches
// a font from a third party tells that third party who read it, and it stops
// rendering the day the third party does. Splitting the rule by file type
// would let the same defect pass by moving one line into the .css.
func checkHTMLNoExternalRef(s *site) []Finding {
	var out []Finding
	report := func(path, what, ref string) {
		out = append(out, Finding{RuleID: "html.no-external-ref", Path: path,
			Detail: fmt.Sprintf("%s names %q, which leaves this site: it reports every reader to whoever serves it, and breaks when they stop", what, ref)})
	}

	for _, f := range s.htmls {
		f.root.walk(func(n *node) {
			if n.tag == "" {
				return
			}
			if src, ok := n.attrs["src"]; ok && schemeRef.MatchString(strings.TrimSpace(src)) {
				report(f.path, "<"+n.tag+" src>", src)
			}
			if n.tag == "link" {
				if href, ok := n.attrs["href"]; ok && schemeRef.MatchString(strings.TrimSpace(href)) {
					report(f.path, "<link href>", href)
				}
			}
			if n.tag == "style" {
				for _, ref := range externalCSSRefs(n.textContent()) {
					report(f.path, "a <style> rule", ref)
				}
			}
		})
	}
	for _, c := range s.csss {
		for _, ref := range externalCSSRefs(c.body) {
			report(c.path, "an @import or @font-face", ref)
		}
	}
	return out
}

// externalCSSRefs returns the off-site references in a stylesheet: @import
// targets anywhere, and url() targets inside an @font-face block.
func externalCSSRefs(src string) []string {
	var out []string
	for _, m := range importRef.FindAllStringSubmatch(src, -1) {
		ref := strings.TrimSpace(m[1] + m[2])
		if schemeRef.MatchString(ref) {
			out = append(out, ref)
		}
	}
	for _, block := range fontFaceBlocks(src) {
		for _, m := range cssURLRef.FindAllStringSubmatch(block, -1) {
			ref := strings.TrimSpace(m[1])
			if schemeRef.MatchString(ref) {
				out = append(out, ref)
			}
		}
	}
	return out
}

func fontFaceBlocks(src string) []string {
	var out []string
	rest := src
	for {
		i := indexFold(rest, "@font-face")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			return out
		}
		shut := strings.IndexByte(rest[open:], '}')
		if shut < 0 {
			out = append(out, rest[open:])
			return out
		}
		out = append(out, rest[open:open+shut])
		rest = rest[open+shut:]
	}
}

func checkHTMLSentenceBudget(s *site) []Finding {
	var out []Finding
	budgets := []struct {
		class string
		max   int
	}{{"little", littleSentenceWords}, {"big", bigSentenceWords}}

	for _, f := range s.htmls {
		for _, sec := range factSections(f) {
			for _, b := range budgets {
				for _, p := range sec.findAll(func(n *node) bool { return n.tag == "p" && n.hasClass(b.class) }) {
					for _, sentence := range sentences(p.textContent()) {
						if n := wordCount(sentence); n > b.max {
							out = append(out, Finding{RuleID: "html.sentence-budget", Path: f.path,
								Detail: fmt.Sprintf("p.%s in data-topic %q has a %d word sentence, over the %d word budget: %q",
									b.class, sec.attrs["data-topic"], n, b.max, sentence)})
						}
					}
				}
			}
		}
	}
	return out
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	sort.Strings(out)
	return out
}
