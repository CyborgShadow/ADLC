// Package sitecheck checks a small static site against rules a reviewer would
// otherwise have to hold in their head: that every image is credited under a
// licence we may actually use, that the pages stay readable on a phone, and
// that nothing on the page reaches off the machine it was served from.
//
// Two disciplines shape the whole package.
//
// First, a check that examined nothing has not passed. A run that found zero
// images fails (see the core.zero-work rule), because "I ran nothing" and
// "everything passed" are the same output otherwise, and only one of them is
// true.
//
// Second, an unreadable input is never a pass. An image no decoder could
// measure, a CREDITS.tsv that will not parse, an HTML file with no fact
// sections in it: each of those is a finding naming what could not be
// established, not a silent skip.
package sitecheck

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Finding is one rule's complaint about one path. Both halves are load
// bearing: a count of failures tells a reader that something is wrong but not
// what to open, so every finding names the rule that fired and the file it
// fired on.
type Finding struct {
	RuleID string
	Path   string
	Detail string
}

func (f Finding) String() string {
	p := f.Path
	if p == "" {
		p = "."
	}
	return f.RuleID + ": " + p + ": " + f.Detail
}

// Rule is one assertion with an id. The id is what appears in output and in
// the test roster, so it is the name a defect is discussed by.
type Rule struct {
	ID     string
	Family string
	check  func(*site) []Finding
}

// Families are the rule families -rule may select. core is deliberately absent:
// it holds the refusal this whole design exists for, and a family a caller can
// switch off is one that will be switched off on the day it fires.
var Families = []string{"credits", "images", "css", "html"}

// rules is the declared roster. Every rule here is counted by the test suite,
// which requires a firing case and a clean case for each — a guard tested only
// where it fires passes vacuously the day it starts flagging everything.
var rules = []Rule{
	{ID: "core.zero-work", Family: "core", check: checkZeroWork},

	{ID: "credits.bijection", Family: "credits", check: checkCreditsBijection},
	{ID: "credits.licence", Family: "credits", check: checkCreditsLicence},
	{ID: "credits.source-url", Family: "credits", check: checkCreditsSourceURL},

	{ID: "images.size", Family: "images", check: checkImagesSize},
	{ID: "images.decodable", Family: "images", check: checkImagesDecodable},
	{ID: "images.dimensions", Family: "images", check: checkImagesDimensions},

	{ID: "css.font-size", Family: "css", check: checkCSSFontSize},
	{ID: "css.tap-target", Family: "css", check: checkCSSTapTarget},
	{ID: "css.landscape", Family: "css", check: checkCSSLandscape},
	{ID: "css.fixed-width", Family: "css", check: checkCSSFixedWidth},

	{ID: "html.fact-count", Family: "html", check: checkHTMLFactCount},
	{ID: "html.fact-parts", Family: "html", check: checkHTMLFactParts},
	{ID: "html.topics", Family: "html", check: checkHTMLTopics},
	{ID: "html.data-source", Family: "html", check: checkHTMLDataSource},
	{ID: "html.uncertain", Family: "html", check: checkHTMLUncertain},
	{ID: "html.img-alt", Family: "html", check: checkHTMLImgAlt},
	{ID: "html.img-src", Family: "html", check: checkHTMLImgSrc},
	{ID: "html.viewport", Family: "html", check: checkHTMLViewport},
	{ID: "html.no-script", Family: "html", check: checkHTMLNoScript},
	{ID: "html.no-form", Family: "html", check: checkHTMLNoForm},
	{ID: "html.no-external-ref", Family: "html", check: checkHTMLNoExternalRef},
	{ID: "html.sentence-budget", Family: "html", check: checkHTMLSentenceBudget},
}

// Rules returns the declared roster. Callers get a copy so that a report cannot
// quietly shorten the roster it is reporting on.
func Rules() []Rule {
	out := make([]Rule, len(rules))
	copy(out, rules)
	return out
}

// Result is what one run of the checker established.
type Result struct {
	// ImagesChecked is how many image files the run discovered. It is reported
	// separately from the findings because zero is itself the failure.
	ImagesChecked int
	Findings      []Finding
}

// OK reports whether the run found nothing to complain about.
func (r Result) OK() bool { return len(r.Findings) == 0 }

// UnknownFamilyError names a -rule value that is not a family. It is a distinct
// type so the command can exit 2 (a usage error the caller must fix) rather
// than 1 (a site that failed a check).
type UnknownFamilyError struct{ Name string }

func (e *UnknownFamilyError) Error() string {
	return fmt.Sprintf("unknown rule family %q: known families are %s",
		e.Name, strings.Join(Families, ", "))
}

// Check runs the selected families over fsys. A nil or empty families slice
// selects every family. The core family always runs: it carries the zero-work
// refusal, which is not the caller's to opt out of.
func Check(fsys fs.FS, families []string) (Result, error) {
	selected := map[string]bool{"core": true}
	if len(families) == 0 {
		for _, f := range Families {
			selected[f] = true
		}
	} else {
		for _, name := range families {
			if !known(name) {
				return Result{}, &UnknownFamilyError{Name: name}
			}
			selected[name] = true
		}
	}

	s, err := loadSite(fsys)
	if err != nil {
		return Result{}, err
	}

	res := Result{ImagesChecked: len(s.images)}
	for _, r := range rules {
		if !selected[r.Family] {
			continue
		}
		res.Findings = append(res.Findings, r.check(s)...)
	}
	return res, nil
}

func known(name string) bool {
	for _, f := range Families {
		if f == name {
			return true
		}
	}
	return false
}

// ParseFamilies splits a -rule value. An empty value means every family; an
// empty element (a stray comma) is a usage error rather than a silent skip,
// because "-rule credits," quietly running everything is how a filter stops
// filtering without anybody noticing.
func ParseFamilies(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(value, ",") {
		name := strings.TrimSpace(part)
		if !known(name) {
			return nil, &UnknownFamilyError{Name: name}
		}
		out = append(out, name)
	}
	return out, nil
}

// checkZeroWork is the refusal the design exists for: a checker that examined
// no images has established nothing about the site, and reporting green there
// converts "I ran nothing" into "everything passed".
func checkZeroWork(s *site) []Finding {
	if len(s.images) > 0 {
		return nil
	}
	return []Finding{{
		RuleID: "core.zero-work",
		Path:   ".",
		Detail: "no image files found: a run that discovered zero units of work has failed, not passed",
	}}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
