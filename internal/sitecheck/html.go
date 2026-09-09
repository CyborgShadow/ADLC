package sitecheck

import (
	"fmt"
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

// checkHTMLDataSource resolves every citation against the sources section. A
// data-source pointing at nothing is a citation that looks like evidence in
// the markup and is evidence of nothing on the page.
func checkHTMLDataSource(s *site) []Finding {
	var out []Finding
	for _, f := range s.htmls {
		var sources *node
		for _, n := range f.root.tags("section") {
			if n.attrs["id"] == sourcesSectionID {
				sources = n
				break
			}
		}
		ids := map[string]bool{}
		if sources != nil {
			for _, n := range sources.findAll(func(n *node) bool { return n.attrs["id"] != "" }) {
				ids[n.attrs["id"]] = true
			}
		}
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
				if !ids[ref] {
					out = append(out, Finding{RuleID: "html.data-source", Path: f.path,
						Detail: fmt.Sprintf("<%s> cites data-source %q, which is not an id inside section#%s (ids there: %s)",
							n.tag, ref, sourcesSectionID, strings.Join(sortedKeys(ids), ", "))})
				}
			}
		}
	}
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
