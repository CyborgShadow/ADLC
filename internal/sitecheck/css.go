package sitecheck

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The stylesheet is scanned rather than fully parsed, for the same reason the
// HTML is: the questions asked of it are about which declarations sit inside
// which rule, and a regexp over the whole file cannot tell a font-size inside
// an @media block from one outside it — which is where a 12px override hides.

type cssDecl struct {
	prop  string
	value string
	line  int
}

type cssRule struct {
	prelude  string // the selector list, or the at-rule including its condition
	line     int
	decls    []cssDecl
	children []*cssRule
}

type cssFile struct {
	textFile
	rules []*cssRule
}

type cssParser struct {
	src  string
	i    int
	line int
}

func parseCSS(src string) []*cssRule {
	p := &cssParser{src: stripCSSComments(src), line: 1}
	return p.block()
}

// stripCSSComments blanks comment bodies but keeps their newlines, so a line
// number in a finding still points at the line the reader will open.
func stripCSSComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], "/*") {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				end = len(src) - i - 2
			}
			for _, c := range src[i : i+2+end] {
				if c == '\n' {
					b.WriteByte('\n')
				}
			}
			i += 2 + end + 2
			if i > len(src) {
				i = len(src)
			}
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}

func (p *cssParser) adv() {
	if p.src[p.i] == '\n' {
		p.line++
	}
	p.i++
}

// block reads until '}' or end of input, returning the rules it found. The
// declarations of the enclosing rule are collected by the caller through
// decls, because a block holds both.
func (p *cssParser) block() []*cssRule {
	rules, _ := p.blockWithDecls()
	return rules
}

func (p *cssParser) blockWithDecls() ([]*cssRule, []cssDecl) {
	var rules []*cssRule
	var decls []cssDecl
	for p.i < len(p.src) {
		if p.src[p.i] == '}' {
			p.adv()
			return rules, decls
		}
		if isHTMLSpace(p.src[p.i]) {
			p.adv()
			continue
		}
		start, startLine := p.i, p.line
		for p.i < len(p.src) && p.src[p.i] != '{' && p.src[p.i] != ';' && p.src[p.i] != '}' {
			p.adv()
		}
		text := strings.TrimSpace(p.src[start:p.i])
		if p.i >= len(p.src) {
			return rules, decls
		}
		switch p.src[p.i] {
		case '{':
			p.adv()
			kids, kidDecls := p.blockWithDecls()
			rules = append(rules, &cssRule{
				prelude: text, line: startLine,
				decls: kidDecls, children: kids,
			})
		case ';':
			p.adv()
			if d, ok := splitDecl(text, startLine); ok {
				decls = append(decls, d)
			}
		case '}':
			// A final declaration with no trailing semicolon.
			if d, ok := splitDecl(text, startLine); ok {
				decls = append(decls, d)
			}
			p.adv()
			return rules, decls
		}
	}
	return rules, decls
}

func splitDecl(text string, line int) (cssDecl, bool) {
	i := strings.IndexByte(text, ':')
	if i < 0 {
		return cssDecl{}, false
	}
	return cssDecl{
		prop:  strings.ToLower(strings.TrimSpace(text[:i])),
		value: strings.TrimSpace(text[i+1:]),
		line:  line,
	}, true
}

// walkCSS visits every rule in the file, nested ones included.
func walkCSS(rules []*cssRule, f func(*cssRule)) {
	for _, r := range rules {
		f(r)
		walkCSS(r.children, f)
	}
}

const (
	minFontSizePx  = 18
	minTapTargetPx = 44
	// narrowestViewportPx is the phone we still support. A fixed width above it
	// is not a layout choice, it is horizontal scrolling.
	narrowestViewportPx = 320
)

// pxValue reads the first px length in a value. It returns ok=false for a value
// with no px in it: this family is about px budgets, and guessing what 2rem
// comes to depends on a root font-size the check cannot see.
var pxPattern = regexp.MustCompile(`(-?[0-9]*\.?[0-9]+)px`)

func pxValue(value string) (float64, bool) {
	m := pxPattern.FindStringSubmatch(value)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func checkCSSFontSize(s *site) []Finding {
	var out []Finding
	for _, c := range s.csss {
		walkCSS(c.rules, func(r *cssRule) {
			for _, d := range r.decls {
				if d.prop != "font-size" {
					continue
				}
				px, ok := pxValue(d.value)
				if !ok {
					continue
				}
				if px < minFontSizePx {
					out = append(out, Finding{RuleID: "css.font-size", Path: c.path,
						Detail: fmt.Sprintf("line %d: %q declares font-size: %s, below the %dpx floor a reader can hold at arm's length",
							d.line, r.prelude, d.value, minFontSizePx)})
				}
			}
		})
	}
	return out
}

// tapTargetSelector matches a selector list containing a bare a or button
// element selector — "a", "a:hover", "nav a", ".card > button". It deliberately
// does not match "article" or "abbr": the element name has to end at a
// boundary, or every selector starting with the letter a would be asked for a
// tap target.
var tapTargetSelector = regexp.MustCompile(`(^|[\s,>+~(])(a|button)($|[\s,>+~:.\[#)])`)

// checkCSSTapTarget requires both dimensions on every rule that styles a link
// or a button. Both, because a target 200px wide and 20px tall is still a
// target a thumb misses; every such rule, because the criterion is that the
// tap target is there, and a rule that overrides height without restoring it
// is exactly how it stops being there.
func checkCSSTapTarget(s *site) []Finding {
	var out []Finding
	for _, c := range s.csss {
		walkCSS(c.rules, func(r *cssRule) {
			if strings.HasPrefix(r.prelude, "@") {
				return
			}
			if !tapTargetSelector.MatchString(r.prelude) {
				return
			}
			for _, axis := range [][2]string{{"min-height", "height"}, {"min-width", "width"}} {
				best, found := 0.0, false
				for _, d := range r.decls {
					if d.prop != axis[0] && d.prop != axis[1] {
						continue
					}
					if px, ok := pxValue(d.value); ok {
						found = true
						if px > best {
							best = px
						}
					}
				}
				if !found || best < minTapTargetPx {
					out = append(out, Finding{RuleID: "css.tap-target", Path: c.path,
						Detail: fmt.Sprintf("line %d: %q styles a link or button but declares no %s or %s of at least %dpx",
							r.line, r.prelude, axis[0], axis[1], minTapTargetPx)})
				}
			}
		})
	}
	return out
}

func checkCSSLandscape(s *site) []Finding {
	for _, c := range s.csss {
		found := false
		walkCSS(c.rules, func(r *cssRule) {
			p := strings.ToLower(strings.Join(strings.Fields(r.prelude), ""))
			if strings.HasPrefix(p, "@media") && strings.Contains(p, "orientation:landscape") {
				found = true
			}
		})
		if found {
			return nil
		}
	}
	path := "."
	if len(s.csss) > 0 {
		path = s.csss[0].path
	}
	return []Finding{{RuleID: "css.landscape", Path: path,
		Detail: "no @media (orientation: landscape) block: a phone held sideways is the second layout this page has, and an untested second layout is one nobody looked at"}}
}

// checkCSSFixedWidth looks for a px width that exceeds the narrowest viewport.
// max-width in px is not a finding: it caps a line length without forcing the
// page wider than the screen.
func checkCSSFixedWidth(s *site) []Finding {
	var out []Finding
	for _, c := range s.csss {
		walkCSS(c.rules, func(r *cssRule) {
			for _, d := range r.decls {
				if d.prop != "width" && d.prop != "min-width" {
					continue
				}
				px, ok := pxValue(d.value)
				if !ok || px <= narrowestViewportPx {
					continue
				}
				out = append(out, Finding{RuleID: "css.fixed-width", Path: c.path,
					Detail: fmt.Sprintf("line %d: %q declares %s: %s, wider than the %dpx viewport it has to fit, so the page scrolls sideways",
						d.line, r.prelude, d.prop, d.value, narrowestViewportPx)})
			}
		})
	}
	return out
}
