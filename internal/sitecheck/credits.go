package sitecheck

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// allowedLicences are compared as strings, deliberately. A licence identifier
// is not a version range and not a family: accepting "CC0" because it is a
// prefix of "CC0-1.0", or "cc0-1.0" because it looks the same to a human, is
// how a file we may not ship gets shipped.
var allowedLicences = map[string]bool{
	"CC0-1.0":       true,
	"PDM-1.0":       true,
	"public-domain": true,
}

type creditsRow struct {
	line      int
	file      string
	licenceID string
	sourceURL string
}

type creditsFile struct {
	path  string
	rows  []creditsRow
	fatal string // why the file could not be used at all; "" when it parsed
}

// parseCredits reads the TSV. Columns are located by header name rather than by
// position, so inserting a column upstream cannot silently shift every licence
// into the source_url check.
func parseCredits(p, body string, found bool) *creditsFile {
	c := &creditsFile{path: p}
	if !found {
		c.fatal = "missing: nothing records where these images came from or what may be done with them"
		return c
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	header := -1
	var cols map[string]int
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		header = i
		cols = map[string]int{}
		for j, name := range strings.Split(line, "\t") {
			cols[strings.TrimSpace(name)] = j
		}
		break
	}
	if header < 0 {
		c.fatal = "empty: a credits file with no header names no columns"
		return c
	}
	for _, want := range []string{"file", "licence_id", "source_url"} {
		if _, ok := cols[want]; !ok {
			c.fatal = fmt.Sprintf("header has no %q column: %v", want, sortedNames(cols))
			return c
		}
	}
	for i := header + 1; i < len(lines); i++ {
		raw := lines[i]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := strings.Split(raw, "\t")
		get := func(name string) string {
			j := cols[name]
			if j >= len(fields) {
				return ""
			}
			return strings.TrimSpace(fields[j])
		}
		c.rows = append(c.rows, creditsRow{
			line:      i + 1,
			file:      get("file"),
			licenceID: get("licence_id"),
			sourceURL: get("source_url"),
		})
	}
	return c
}

func sortedNames(cols map[string]int) []string {
	out := make([]string, 0, len(cols))
	for k := range cols {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// creditsUnusable gives every credits rule the same complaint when the file
// itself is the problem. All three rules report it rather than one: a missing
// credits file means the licence and the source of every image are unknown,
// and UNKNOWN is not a pass for any of them.
func creditsUnusable(s *site, ruleID string) []Finding {
	if s.credits.fatal == "" {
		return nil
	}
	return []Finding{{RuleID: ruleID, Path: s.credits.path, Detail: s.credits.fatal}}
}

// checkCreditsBijection asserts one row per image and one image per row. A
// bijection rather than containment in either direction: an uncredited image is
// a file we cannot prove we may ship, and a row with no image is a credit that
// has silently stopped describing anything.
func checkCreditsBijection(s *site) []Finding {
	if f := creditsUnusable(s, "credits.bijection"); f != nil {
		return f
	}
	var out []Finding

	rowFor := map[string][]creditsRow{}
	for _, r := range s.credits.rows {
		rowFor[r.file] = append(rowFor[r.file], r)
	}
	have := map[string]bool{}
	for _, img := range s.images {
		if path.Dir(img.path) != "img" {
			continue
		}
		name := path.Base(img.path)
		have[name] = true
		switch n := len(rowFor[name]); {
		case n == 0:
			out = append(out, Finding{RuleID: "credits.bijection", Path: img.path,
				Detail: "no row in " + s.credits.path + ": an uncredited image is one we cannot show we may ship"})
		case n > 1:
			out = append(out, Finding{RuleID: "credits.bijection", Path: img.path,
				Detail: fmt.Sprintf("%d rows in %s: two credits for one file cannot both be the credit", n, s.credits.path)})
		}
	}
	for _, r := range s.credits.rows {
		if !have[r.file] {
			out = append(out, Finding{RuleID: "credits.bijection", Path: s.credits.path,
				Detail: fmt.Sprintf("line %d credits %q, which is not an image under img/", r.line, r.file)})
		}
	}
	return out
}

func checkCreditsLicence(s *site) []Finding {
	if f := creditsUnusable(s, "credits.licence"); f != nil {
		return f
	}
	var out []Finding
	for _, r := range s.credits.rows {
		if !allowedLicences[r.licenceID] {
			out = append(out, Finding{RuleID: "credits.licence", Path: s.credits.path,
				Detail: fmt.Sprintf("line %d credits %s under licence_id %q, which is not one of %s",
					r.line, r.file, r.licenceID, strings.Join(sortedLicences(), ", "))})
		}
	}
	return out
}

// checkCreditsSourceURL requires https://, not merely a URL. A source we can
// only fetch over http is one whose answer anyone on the path may have written.
func checkCreditsSourceURL(s *site) []Finding {
	if f := creditsUnusable(s, "credits.source-url"); f != nil {
		return f
	}
	var out []Finding
	for _, r := range s.credits.rows {
		if !strings.HasPrefix(r.sourceURL, "https://") {
			out = append(out, Finding{RuleID: "credits.source-url", Path: s.credits.path,
				Detail: fmt.Sprintf("line %d credits %s with source_url %q, which does not begin https://",
					r.line, r.file, r.sourceURL)})
		}
	}
	return out
}

func sortedLicences() []string { return sortedKeys(allowedLicences) }
