package server

import (
	"regexp"
	"strings"
	"testing"
)

// The stylesheets are concatenated into one document, so a class name used by
// two pages is not two styles — it is one, and whichever fragment sorts last
// wins. That failure is silent and it does not look like a CSS problem: the
// Roles page defined `.rail` as a vertical column, the About page's horizontal
// lifecycle rail inherited it, and the lifecycle rendered as ten stacked
// blocks. Nothing errored, no test failed, and the page just looked wrong.
//
// So the sheets are checked against each other. This is a naming rule rather
// than a styling one: the page that owns a class is the only page that may
// name it, and a shared one belongs in the base sheet where it can be seen.

// sheets are the per-page stylesheets, by the file that owns them.
func sheets() map[string]string {
	return map[string]string{
		"about":     aboutCSS,
		"aboutData": aboutDataCSS,
		"console":   consoleCSS,
		"history":   historyPageCSS,
		"roadmap":   roadmapPageCSS,
		"item":      itemPageCSS,
		"config":    configPageCSS,
		"roles":     rolesPageCSS,
		"gates":     gatesCSS,
	}
}

// topLevelClasses returns the classes a sheet defines in its own right —
// selectors that begin with a class at the start of a rule. A descendant
// selector like `.rolerail .lay` is scoped by its ancestor and cannot collide,
// so only the leading class is collected.
var classRe = regexp.MustCompile(`(?m)^\.([a-zA-Z][a-zA-Z0-9_-]*)(?:[\s,{>]|$)`)

func topLevelClasses(css string) map[string]bool {
	out := map[string]bool{}
	for _, m := range classRe.FindAllStringSubmatch(css, -1) {
		out[m[1]] = true
	}
	return out
}

// TestNoTwoPagesClaimTheSameClassName is the guard.
//
// The About page's own two sheets are deliberately exempt from each other:
// `about_data.go` extends `.dia` on the same page, which is one page styling
// one thing rather than two pages disagreeing.
func TestNoTwoPagesClaimTheSameClassName(t *testing.T) {
	owner := map[string]string{}
	for name, css := range sheets() {
		page := name
		if page == "aboutData" {
			page = "about"
		}
		for class := range topLevelClasses(css) {
			if prev, taken := owner[class]; taken && prev != page {
				t.Errorf(
					"both the %s and %s stylesheets define .%s at the top level. They are concatenated into one document, so one silently overrides the other — rename one, or move it into the base sheet in templates.go if it is genuinely shared.",
					prev, page, class)
				continue
			}
			owner[class] = page
		}
	}
}

// TestPageSheetsDoNotRedefineBaseClasses covers the other direction: a page
// sheet quietly changing a class the whole dashboard uses. `.card` meaning
// something different on one page is how a design stops being one design.
func TestPageSheetsDoNotRedefineBaseClasses(t *testing.T) {
	// The shared vocabulary, defined in pageHTML's own <style> block.
	base := []string{
		"card", "grid", "pill", "banner", "wrap", "mono", "dim", "small",
		"num", "bar", "tabs", "q", "tl", "chain", "ok", "warn", "bad", "live", "mute",
	}
	shared := map[string]bool{}
	for _, c := range base {
		shared[c] = true
	}
	for name, css := range sheets() {
		for class := range topLevelClasses(css) {
			if shared[class] {
				t.Errorf(
					"the %s stylesheet redefines .%s, which every page uses. Scope it (.something .%s) or give it its own name — a shared class that means two things is a design that has stopped being one.",
					name, class, class)
			}
		}
	}
}

// TestEveryClassUsedInMarkupIsDefined catches the opposite slip: markup that
// names a class nothing styles, which renders as an unstyled element rather
// than as an error.
func TestEveryClassUsedInMarkupIsDefined(t *testing.T) {
	all := strings.Builder{}
	for _, css := range sheets() {
		all.WriteString(css)
	}
	// Plus the base sheet, which lives inside the page template.
	all.WriteString(pageHTML)
	defined := all.String()

	fragments := map[string]string{
		"about":     aboutHTML,
		"aboutData": aboutDataHTML,
		"console":   consoleHTML,
		"dock":      consoleDockHTML,
		"history":   historyPageHTML,
		"roadmap":   roadmapPageHTML,
		"item":      itemPageHTML,
		"config":    configPageHTML,
		"roles":     rolesPageHTML,
		"questions": questionsPageHTML,
		"approvals": approvalsPageHTML,
	}
	// Classes whose names are computed by a template function rather than
	// written out, so they cannot be found by reading the markup.
	computed := map[string]bool{
		"ok": true, "warn": true, "bad": true, "live": true, "mute": true,
		"on": true, "cleared": true, "yours": true, "ran": true, "no": true,
		"todo": true, "doing": true, "done": true, "term": true, "stop": true,
		"prose": true, "up": true, "left": true, "calm": true, "sec": true,
		"inline": true, "tile": true, "me": true, "wait": true, "fail": true,
	}
	useRe := regexp.MustCompile(`class="([^"{}]+)"`)
	for page, frag := range fragments {
		for _, m := range useRe.FindAllStringSubmatch(frag, -1) {
			for _, class := range strings.Fields(m[1]) {
				if computed[class] {
					continue
				}
				if !strings.Contains(defined, "."+class) {
					t.Errorf("%s markup uses .%s, which no stylesheet defines", page, class)
				}
			}
		}
	}
}

// A tooltip inside a horizontally scrolling table is clipped by the scroller,
// and no z-index can lift it out: an ancestor's overflow clips its absolutely
// positioned descendants whatever their stacking order. Half of the Config
// page's tips were cut in half by exactly this.
//
// The fix is a `.wrap tips` variant that wraps instead of scrolling. This walks
// the templates and refuses a tip that has been put back inside a plain one.
func TestNoTooltipInsideAScrollingTable(t *testing.T) {
	pages := map[string]string{
		"config": configPageHTML, "overview": overviewHTML, "roadmap": roadmapPageHTML,
		"progress": progressHTML, "roles": rolesPageHTML, "history": historyPageHTML,
		"item": itemPageHTML, "about": aboutHTML, "console": consoleHTML,
	}
	// Every one of these wrappers holds exactly one table, so the region to
	// search is from the opening div to the end of that table.
	scan := func(body string) int {
		bad, rest := 0, body
		for {
			i := strings.Index(rest, `<div class="wrap"`)
			if i < 0 {
				return bad
			}
			rest = rest[i+len(`<div class="wrap"`):]
			end := strings.Index(rest, "</table>")
			if end < 0 {
				end = len(rest)
			}
			if strings.Contains(rest[:end], `class="tip"`) {
				bad++
			}
		}
	}
	for name, body := range pages {
		if n := scan(body); n > 0 {
			t.Errorf("%s: %d tooltip(s) sit inside a plain .wrap, which clips them — use `wrap tips`", name, n)
		}
	}
	// The clean case, so this cannot pass by finding no tooltips anywhere: the
	// Config page has them, and they are in wrappers that do not clip.
	if !strings.Contains(configPageHTML, `class="tip"`) {
		t.Fatal("no tooltips on the Config page — this guard would pass vacuously")
	}
	if !strings.Contains(configPageHTML, `<div class="wrap tips"`) {
		t.Fatal("no non-clipping wrapper on the Config page — the fix has been removed")
	}
}
