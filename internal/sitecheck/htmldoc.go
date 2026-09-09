package sitecheck

import "strings"

// The site being checked is one we write, so this is a tolerant scanner over
// well-formed markup rather than a conforming HTML parser. It exists because a
// regexp cannot answer "exactly one p.little inside each fact section" — that
// question is about nesting, and a scanner that ignores nesting will happily
// accept a page where every paragraph lives in the first section.
//
// Where it cannot make sense of something it keeps going rather than guessing:
// an unmatched close tag is dropped instead of unwinding the stack, because
// unwinding turns one typo into a page-wide cascade of findings that hides the
// typo.

type node struct {
	tag      string // "" for a text node, "#root" for the document
	attrs    map[string]string
	text     string
	children []*node
}

var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// rawTextTags hold text rather than markup. Their contents are captured whole
// so that a "<" inside a stylesheet or a title is not read as a tag.
var rawTextTags = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true,
}

type htmlFile struct {
	textFile
	root *node
}

func parseHTML(src string) *node {
	root := &node{tag: "#root", attrs: map[string]string{}}
	stack := []*node{root}
	top := func() *node { return stack[len(stack)-1] }

	i := 0
	for i < len(src) {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			addText(top(), src[i:])
			break
		}
		if lt > 0 {
			addText(top(), src[i:i+lt])
		}
		i += lt

		if strings.HasPrefix(src[i:], "<!--") {
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(src[i:], "<!") || strings.HasPrefix(src[i:], "<?") {
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}

		gt := tagEnd(src, i)
		if gt < 0 {
			addText(top(), src[i:])
			break
		}
		raw := src[i+1 : gt]
		i = gt + 1

		if strings.HasPrefix(raw, "/") {
			name := strings.ToLower(strings.TrimSpace(raw[1:]))
			for k := len(stack) - 1; k > 0; k-- {
				if stack[k].tag == name {
					stack = stack[:k]
					break
				}
			}
			continue
		}

		selfClose := strings.HasSuffix(raw, "/")
		if selfClose {
			raw = raw[:len(raw)-1]
		}
		name, attrs := parseTag(raw)
		if name == "" {
			continue
		}
		n := &node{tag: name, attrs: attrs}
		top().children = append(top().children, n)

		if rawTextTags[name] {
			if selfClose {
				continue
			}
			closer := "</" + name
			idx := indexFold(src[i:], closer)
			if idx < 0 {
				n.children = append(n.children, &node{text: src[i:]})
				break
			}
			n.children = append(n.children, &node{text: src[i : i+idx]})
			j := strings.IndexByte(src[i+idx:], '>')
			if j < 0 {
				i = len(src)
			} else {
				i = i + idx + j + 1
			}
			continue
		}
		if !selfClose && !voidTags[name] {
			stack = append(stack, n)
		}
	}
	return root
}

func addText(parent *node, s string) {
	if strings.TrimSpace(s) == "" {
		// Whitespace is kept so that adjacent inline text does not run
		// together into a single word when textContent joins it.
		s = " "
	}
	parent.children = append(parent.children, &node{text: s})
}

// tagEnd finds the '>' closing the tag opened at i, ignoring one inside a
// quoted attribute value — alt="a > b" is a caption, not the end of the tag.
func tagEnd(src string, i int) int {
	var quote byte
	for j := i + 1; j < len(src); j++ {
		c := src[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return j
		}
	}
	return -1
}

func parseTag(raw string) (string, map[string]string) {
	attrs := map[string]string{}
	i := 0
	for i < len(raw) && !isHTMLSpace(raw[i]) {
		i++
	}
	name := strings.ToLower(raw[:i])
	if name == "" {
		return "", attrs
	}
	for i < len(raw) {
		for i < len(raw) && (isHTMLSpace(raw[i]) || raw[i] == '/') {
			i++
		}
		start := i
		for i < len(raw) && !isHTMLSpace(raw[i]) && raw[i] != '=' && raw[i] != '/' {
			i++
		}
		if start == i {
			break
		}
		key := strings.ToLower(raw[start:i])
		for i < len(raw) && isHTMLSpace(raw[i]) {
			i++
		}
		value := ""
		if i < len(raw) && raw[i] == '=' {
			i++
			for i < len(raw) && isHTMLSpace(raw[i]) {
				i++
			}
			if i < len(raw) && (raw[i] == '"' || raw[i] == '\'') {
				q := raw[i]
				i++
				vs := i
				for i < len(raw) && raw[i] != q {
					i++
				}
				value = raw[vs:i]
				if i < len(raw) {
					i++
				}
			} else {
				vs := i
				for i < len(raw) && !isHTMLSpace(raw[i]) {
					i++
				}
				value = raw[vs:i]
			}
		}
		attrs[key] = value
	}
	return name, attrs
}

func isHTMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func indexFold(s, sub string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(sub))
}

// walk visits n and every descendant in document order.
func (n *node) walk(f func(*node)) {
	f(n)
	for _, c := range n.children {
		c.walk(f)
	}
}

// findAll returns every descendant of n (n itself excluded) matching pred.
func (n *node) findAll(pred func(*node) bool) []*node {
	var out []*node
	for _, c := range n.children {
		c.walk(func(m *node) {
			if m.tag != "" && pred(m) {
				out = append(out, m)
			}
		})
	}
	return out
}

func (n *node) tags(name string) []*node {
	return n.findAll(func(m *node) bool { return m.tag == name })
}

// textContent joins the text under n, separating at element boundaries. The
// separator matters because this feeds a word count: "</p><p>" with no
// whitespace between them would otherwise glue two words into one token and
// undercount the budget, and an undercount hides an overrun where an
// overcount shows a finding somebody can look at and dismiss.
func (n *node) textContent() string {
	var b strings.Builder
	var rec func(*node)
	rec = func(m *node) {
		if m.tag == "" {
			b.WriteString(m.text)
			return
		}
		b.WriteByte(' ')
		for _, c := range m.children {
			rec(c)
		}
		b.WriteByte(' ')
	}
	rec(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func (n *node) classes() []string { return strings.Fields(n.attrs["class"]) }

func (n *node) hasClass(c string) bool {
	for _, got := range n.classes() {
		if got == c {
			return true
		}
	}
	return false
}
