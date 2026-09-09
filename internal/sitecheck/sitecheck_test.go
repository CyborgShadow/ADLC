package sitecheck

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// ruleCase pairs a rule with the two fixtures that prove it works: one that
// conforms and one that does not. Both are required. A rule with only a firing
// case passes vacuously the day it starts flagging everything, and a rule with
// only a clean case passes vacuously the day it stops flagging anything — the
// second is the failure mode a checker actually dies of.
type ruleCase struct {
	clean  func(*testing.T) fstest.MapFS
	firing func(*testing.T) fstest.MapFS
}

func ruleCases() map[string]ruleCase {
	clean := goodSite

	return map[string]ruleCase{
		"core.zero-work": {clean, emptySite},

		"credits.bijection": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "img/CREDITS.tsv",
				"cat-b.png\tpublic-domain\thttps://example.org/cat-b\tAnon\n", "")
		}},
		"credits.licence": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "img/CREDITS.tsv", "cat-a.png\tCC0-1.0", "cat-a.png\tcc-by-sa-3.0")
		}},
		"credits.source-url": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "img/CREDITS.tsv", "https://example.org/cat-b", "http://example.org/cat-b")
		}},

		"images.size": {clean, func(t *testing.T) fstest.MapFS {
			return setFile(goodSite(t), "img/cat-a.png", pngNoise(t, 400, 400))
		}},
		"images.decodable": {clean, func(t *testing.T) fstest.MapFS {
			return setFile(goodSite(t), "img/cat-c.webp", riffWebP())
		}},
		"images.dimensions": {clean, func(t *testing.T) fstest.MapFS {
			return setFile(goodSite(t), "img/cat-a.png", pngUniform(t, 1400, 300))
		}},

		"css.font-size": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "style.css", "p.little { font-size: 22px; }", "p.little { font-size: 12px; }")
		}},
		"css.tap-target": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "style.css", "a { min-height: 44px; min-width: 44px;", "a { min-width: 44px;")
		}},
		"css.landscape": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "style.css", "@media (orientation: landscape)", "@media (min-width: 40rem)")
		}},
		"css.fixed-width": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "style.css", ".fact { max-width: 40rem;", ".fact { width: 900px;")
		}},

		"html.fact-count": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html",
				`<section class="fact" data-topic="choosing-a-person"`,
				`<section class="aside" data-topic="choosing-a-person"`)
		}},
		"html.fact-parts": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html",
				`<p class="little">A purr is not only a happy sound.</p>`,
				`<p class="little">A purr is not only a happy sound.</p>`+"\n"+`<p class="little">It is also a request.</p>`)
		}},
		"html.topics": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", `data-topic="purring"`, `data-topic="purrs"`)
		}},
		"html.data-source": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", `data-source="s3"`, `data-source="s9"`)
		}},
		"html.uncertain": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", `class="fact uncertain"`, `class="fact"`)
		}},
		"html.img-alt": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", `alt="A cat with a round face and wide eyes"`, `alt=""`)
		}},
		"html.img-src": {clean, func(t *testing.T) fstest.MapFS {
			// A relative src to a file that is not there: it has an alt and it
			// stays on this site, so html.img-alt and html.no-external-ref both
			// pass it.
			return swap(t, goodSite(t), "index.html", `src="img/cat-b.png"`, `src="img/cat-c.png"`)
		}},
		"html.viewport": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", `<meta name="viewport" content="width=device-width, initial-scale=1">`, "")
		}},
		"html.no-script": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", "</body>", "<script>console.log('hi')</script>\n</body>")
		}},
		"html.no-form": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", "</body>", `<form action="/x"><input name="q"></form>`+"\n</body>")
		}},
		"html.no-external-ref": {clean, func(t *testing.T) fstest.MapFS {
			return swap(t, goodSite(t), "index.html", `href="style.css"`, `href="https://cdn.example.com/style.css"`)
		}},
		"html.sentence-budget": {clean, func(t *testing.T) fstest.MapFS {
			// Thirteen words, one over the budget. A firing case well clear of
			// the line would still fire if the budget were quietly edited to
			// 20; this one would not.
			return swap(t, goodSite(t), "index.html",
				"<p class=\"little\">Cats moved in on us before we invited them.</p>",
				"<p class=\"little\">"+littleSentence13+"</p>")
		}},
	}
}

// TestRulesEachHaveBothCases counts the declared roster rather than whatever
// the table happens to mention. A rule added with no cases is silence, and
// silence read as coverage is the defect this test exists for.
func TestRulesEachHaveBothCases(t *testing.T) {
	cases := ruleCases()
	declared := map[string]bool{}
	for _, r := range Rules() {
		declared[r.ID] = true
		c, ok := cases[r.ID]
		if !ok {
			t.Errorf("rule %s has no clean case and no firing case", r.ID)
			continue
		}
		if c.clean == nil {
			t.Errorf("rule %s has no clean case", r.ID)
		}
		if c.firing == nil {
			t.Errorf("rule %s has no firing case", r.ID)
		}
	}
	for id := range cases {
		if !declared[id] {
			t.Errorf("the table has cases for %q, which is not a declared rule", id)
		}
	}
	if len(Rules()) == 0 {
		t.Fatal("no rules declared: a checker with no rules reports green on everything")
	}
}

// TestRules runs, for every declared rule, one subtest proving a conforming
// fixture passes it and one proving a fixture that violates it fails. go test
// -v names each one as TestRules/<rule id>/clean and TestRules/<rule id>/firing.
func TestRules(t *testing.T) {
	cases := ruleCases()
	for _, r := range Rules() {
		c, ok := cases[r.ID]
		if !ok {
			continue // TestRulesEachHaveBothCases owns that failure
		}

		t.Run(r.ID+"/clean", func(t *testing.T) {
			got := runRule(t, r.ID, c.clean(t))
			if len(got) != 0 {
				t.Errorf("%s fired on the conforming fixture: %s", r.ID, joinFindings(got))
			}
		})

		t.Run(r.ID+"/firing", func(t *testing.T) {
			got := runRule(t, r.ID, c.firing(t))
			if len(got) == 0 {
				t.Fatalf("%s did not fire on the fixture that violates it", r.ID)
			}
			for _, f := range got {
				if f.RuleID != r.ID {
					t.Errorf("finding carries rule id %q, want %q", f.RuleID, r.ID)
				}
				if f.Path == "" {
					t.Errorf("%s reported no path, so the output names a count and not a defect", r.ID)
				}
				if f.Detail == "" {
					t.Errorf("%s reported no detail", r.ID)
				}
			}
		})
	}
}

func runRule(t *testing.T, id string, fsys fs.FS) []Finding {
	t.Helper()
	s, err := loadSite(fsys)
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	for _, r := range rules {
		if r.ID == id {
			return r.check(s)
		}
	}
	t.Fatalf("no rule %q", id)
	return nil
}

func joinFindings(fs []Finding) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, "; ")
}

func TestCheckOnTheConformingSiteIsClean(t *testing.T) {
	res, err := Check(goodSite(t), nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.OK() {
		t.Fatalf("conforming fixture produced findings: %s", joinFindings(res.Findings))
	}
	if res.ImagesChecked != 2 {
		t.Errorf("ImagesChecked = %d, want 2", res.ImagesChecked)
	}
}

// TestCheckRefusesASiteItExaminedNothingIn is the refusal the whole design
// exists for: reporting green over zero images turns "I ran nothing" into
// "everything passed".
func TestCheckRefusesASiteItExaminedNothingIn(t *testing.T) {
	res, err := Check(emptySite(t), nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.OK() {
		t.Fatal("a site with no images passed")
	}
	if res.ImagesChecked != 0 {
		t.Errorf("ImagesChecked = %d, want 0", res.ImagesChecked)
	}
	if !hasRule(res.Findings, "core.zero-work") {
		t.Errorf("no core.zero-work finding: %s", joinFindings(res.Findings))
	}
}

func hasRule(findings []Finding, id string) bool {
	for _, f := range findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

// TestSelectingAFamilyRunsOnlyThatFamily proves -rule filters rather than
// decorates: a filter that silently runs everything, or nothing, looks the same
// from the exit code.
func TestSelectingAFamilyRunsOnlyThatFamily(t *testing.T) {
	broken := swap(t, goodSite(t), "img/CREDITS.tsv", "cat-a.png\tCC0-1.0", "cat-a.png\tcc-by-sa-3.0")
	broken = swap(t, broken, "style.css", "p.little { font-size: 22px; }", "p.little { font-size: 12px; }")

	res, err := Check(broken, []string{"credits"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !hasRule(res.Findings, "credits.licence") {
		t.Errorf("credits family did not report the bad licence: %s", joinFindings(res.Findings))
	}
	if hasRule(res.Findings, "css.font-size") {
		t.Errorf("selecting credits still ran the css family: %s", joinFindings(res.Findings))
	}

	// The core family is not selectable and always runs, because the refusal it
	// carries is not the caller's to switch off.
	res, err = Check(emptySite(t), []string{"credits"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !hasRule(res.Findings, "core.zero-work") {
		t.Errorf("selecting one family switched off the zero-work refusal: %s", joinFindings(res.Findings))
	}
}

func TestParseFamilies(t *testing.T) {
	got, err := ParseFamilies("credits, images,css,html")
	if err != nil {
		t.Fatalf("ParseFamilies: %v", err)
	}
	if strings.Join(got, ",") != "credits,images,css,html" {
		t.Errorf("got %v", got)
	}
	if got, err := ParseFamilies(""); err != nil || got != nil {
		t.Errorf("an empty -rule should select every family, got %v, %v", got, err)
	}

	for _, bad := range []string{"typos", "credits,typos", "credits,"} {
		if _, err := ParseFamilies(bad); err == nil {
			t.Errorf("ParseFamilies(%q) was accepted; an unknown family that silently runs everything is a filter that stopped filtering", bad)
		} else if _, ok := err.(*UnknownFamilyError); !ok {
			t.Errorf("ParseFamilies(%q) returned %T, want *UnknownFamilyError so the command can exit 2 rather than 1", bad, err)
		}
	}
}

// TestFindingNamesTheRuleAndThePath pins the output shape: a reviewer given a
// count knows something is wrong and not what to open.
func TestFindingNamesTheRuleAndThePath(t *testing.T) {
	f := Finding{RuleID: "credits.licence", Path: "img/CREDITS.tsv", Detail: "line 2 is cc-by-sa-3.0"}
	got := f.String()
	for _, want := range []string{"credits.licence", "img/CREDITS.tsv", "line 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("Finding.String() = %q, missing %q", got, want)
		}
	}
}

// TestSentencesPinsHowASentenceIsCounted holds the definition stated in
// text.go to the wall, so the criterion and the check cannot drift into
// measuring different things.
func TestSentencesPinsHowASentenceIsCounted(t *testing.T) {
	cases := []struct {
		in    string
		words []int
	}{
		{"One two three.", []int{3}},
		{"Is it? It is! Yes.", []int{2, 2, 1}},
		{"No delimiter at all", []int{4}},
		{"Trailing space is not a sentence.   ", []int{6}},
		{"A twelve-week-old kitten is one word there, not three.", []int{9}},
		{"Dr. Smith is two sentences under this definition.", []int{1, 7}},
		{"", nil},
	}
	for _, c := range cases {
		got := sentences(c.in)
		if len(got) != len(c.words) {
			t.Errorf("sentences(%q) gave %d sentences (%v), want %d", c.in, len(got), got, len(c.words))
			continue
		}
		for i, want := range c.words {
			if n := wordCount(got[i]); n != want {
				t.Errorf("sentences(%q)[%d] = %q has %d words, want %d", c.in, i, got[i], n, want)
			}
		}
	}
}

// TestSentenceBudgetsAreThoseInTheCriteria stops the constants being edited to
// whatever the fixture happens to say.
func TestSentenceBudgetsAreThoseInTheCriteria(t *testing.T) {
	if littleSentenceWords != 12 {
		t.Errorf("littleSentenceWords = %d, want 12", littleSentenceWords)
	}
	if bigSentenceWords != 30 {
		t.Errorf("bigSentenceWords = %d, want 30", bigSentenceWords)
	}
}

// TestHTMLScannerRespectsNesting is the reason this package parses rather than
// greps: a scanner blind to nesting accepts a page whose paragraphs all live in
// the first section.
func TestHTMLScannerRespectsNesting(t *testing.T) {
	src := `<section class="fact"><p class="little">a</p><p class="big">b</p></section>` +
		`<section class="fact"><p class="little">c</p><p class="little">d</p></section>`
	root := parseHTML(src)
	secs := root.findAll(func(n *node) bool { return n.tag == "section" })
	if len(secs) != 2 {
		t.Fatalf("found %d sections, want 2", len(secs))
	}
	for i, want := range []int{1, 2} {
		got := secs[i].findAll(func(n *node) bool { return n.tag == "p" && n.hasClass("little") })
		if len(got) != want {
			t.Errorf("section %d has %d p.little, want %d", i+1, len(got), want)
		}
	}
	if got := secs[0].textContent(); got != "a b" {
		t.Errorf("textContent = %q, want %q", got, "a b")
	}
}

// TestCSSScannerSeesInsideAtRules is the same argument for the stylesheet: an
// override hidden in an @media block is exactly where a 12px font-size lives.
func TestCSSScannerSeesInsideAtRules(t *testing.T) {
	s, err := loadSite(fstest.MapFS{
		"style.css": &fstest.MapFile{Data: []byte(
			"body { font-size: 20px; }\n@media (orientation: landscape) {\n  p { font-size: 12px; }\n}\n")},
		"img/a.png": &fstest.MapFile{Data: pngUniform(t, 10, 10)},
	})
	if err != nil {
		t.Fatalf("loadSite: %v", err)
	}
	got := checkCSSFontSize(s)
	if len(got) != 1 {
		t.Fatalf("got %d findings (%s), want 1 for the declaration inside @media", len(got), joinFindings(got))
	}
	if !strings.Contains(got[0].Detail, "12px") {
		t.Errorf("finding does not name the offending value: %q", got[0].Detail)
	}
}

// TestTheFamilySetIsExactlyTheFourDeclared holds the roster's shape: the rules
// added for the html img src, the unreadable header and the sentence budgets
// each land in a family that already exists. A fifth family would be reachable
// from no command anybody has written down — every plan that runs these rules
// runs them through -rule html or -rule images — so it would be a rule that
// exists and is never selected.
func TestTheFamilySetIsExactlyTheFourDeclared(t *testing.T) {
	if got := strings.Join(Families, ","); got != "credits,images,css,html" {
		t.Errorf("Families = %q, want credits,images,css,html", got)
	}
	allowed := map[string]bool{"core": true}
	for _, f := range Families {
		allowed[f] = true
	}
	for _, r := range Rules() {
		if !allowed[r.Family] {
			t.Errorf("rule %s is in family %q, which -rule cannot select", r.ID, r.Family)
		}
	}
}

// TestAnUnreadableHeaderIsAFailureAndNeverASkip measures the premise rather
// than assuming it. The hole this rule fills is not that DecodeConfig errors —
// it is that it errors and hands back a zero-valued Config at the same time, so
// a checker that reads the dimensions and ignores the error measures 0x0 and
// finds it comfortably inside a 1280 px ceiling.
func TestAnUnreadableHeaderIsAFailureAndNeverASkip(t *testing.T) {
	cfg, err := decodeImageHeader(riffWebP())
	if err == nil {
		t.Fatal("a WEBP header decoded: this test no longer measures what it claims to")
	}
	if err.Error() != "image: unknown format" {
		t.Errorf("decode error = %q, want %q", err.Error(), "image: unknown format")
	}
	if cfg.Width != 0 || cfg.Height != 0 {
		t.Fatalf("config = %dx%d, want 0x0", cfg.Width, cfg.Height)
	}
	if cfg.Width <= maxImageEdge && cfg.Height <= maxImageEdge {
		t.Logf("0x0 is inside the %d px ceiling, which is why the error cannot be dropped", maxImageEdge)
	}

	fsys := setFile(goodSite(t), "img/cat-c.webp", riffWebP())
	if got := runRule(t, "images.dimensions", fsys); len(got) != 0 {
		t.Errorf("images.dimensions reported on a file it could not measure (%s); images.decodable owns that", joinFindings(got))
	}
	got := runRule(t, "images.decodable", fsys)
	if len(got) != 1 {
		t.Fatalf("images.decodable gave %d findings (%s), want 1", len(got), joinFindings(got))
	}
	if got[0].Path != "img/cat-c.webp" {
		t.Errorf("finding names path %q, want img/cat-c.webp", got[0].Path)
	}
	for _, want := range []string{"image: unknown format", "gif, jpeg, png"} {
		if !strings.Contains(got[0].Detail, want) {
			t.Errorf("finding detail %q does not carry %q", got[0].Detail, want)
		}
	}

	res, err := Check(fsys, nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.OK() {
		t.Error("a site holding an image nothing could measure passed")
	}
}

// TestTheAdmittedImageFormatsAreNamedInExactlyOnePlace reads this package's own
// imports rather than its behaviour, because behaviour cannot answer the
// question here: image.DecodeConfig consults one process-wide registry, and
// this test binary imports image/png itself to build fixtures, so a test that
// encodes and decodes a sample would report a format as supported after the
// package's own import of it had been deleted. Dropping _ "image/gif" from
// images.go passes such a test and ships a checker that calls every GIF
// unreadable.
//
// What is asserted is the criterion itself: the decoder imports and
// admittedImageFormats are the same set, they live in one file, and nothing
// here reaches for a decoder outside the standard library.
func TestTheAdmittedImageFormatsAreNamedInExactlyOnePlace(t *testing.T) {
	if len(admittedImageFormats) == 0 {
		t.Fatal("no admitted formats: a checker that decodes nothing measures nothing")
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	imported := map[string]bool{}
	var files []string
	read := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		read++
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		found := false
		for _, spec := range f.Imports {
			p := strings.Trim(spec.Path.Value, `"`)
			if strings.Contains(p, "golang.org/x/image") {
				t.Errorf("%s imports %s: a fourth decoder is a module dependency, and go.mod is not this package's to change", name, p)
			}
			if format, ok := strings.CutPrefix(p, "image/"); ok && format != "color" {
				imported[format] = true
				found = true
			}
		}
		if found {
			files = append(files, name)
		}
	}
	// A run that discovered zero units of work has failed: an empty read would
	// otherwise agree with an empty list and report green over nothing.
	if read == 0 {
		t.Fatal("parsed no source files in this package")
	}

	if len(files) != 1 {
		t.Errorf("image decoders are imported in %v, want exactly one file: two lists of admitted formats drift, and the drift is silent in both directions", files)
	}
	want := map[string]bool{}
	for _, f := range admittedImageFormats {
		want[f] = true
	}
	for f := range want {
		if !imported[f] {
			t.Errorf("%q is admitted but no decoder for it is imported: the checker will call every such file unreadable", f)
		}
	}
	for f := range imported {
		if !want[f] {
			t.Errorf("image/%s is imported but not named in admittedImageFormats: a finding will tell the reader that format is not allowed while the checker quietly measures it", f)
		}
	}

	// The refusing half, measured rather than assumed: a format outside the set
	// has no decoder, so its header is unreadable and images.decodable owns it.
	if _, err := decodeImageHeader(riffWebP()); err == nil {
		t.Error("webp decoded: the admitted set is wider than the list says")
	}
}

// TestTheSentenceBudgetsFireAtTheWordAfterTheBudget walks both sides of each
// line, because the defect a budget dies of is an off-by-one nobody can see
// from either side alone: a rule comparing >= refuses the sentence the budget
// allows, and one comparing > budget+1 admits the sentence it forbids. Both
// look identical to a fixture written comfortably clear of the line.
// TestSentenceBudgetsAreThoseInTheCriteria holds the numbers themselves; this
// holds where the rule fires relative to them.
func TestTheSentenceBudgetsFireAtTheWordAfterTheBudget(t *testing.T) {
	cases := []struct {
		class string
		words int
		fires bool
	}{
		{"little", littleSentenceWords, false},
		{"little", littleSentenceWords + 1, true},
		{"big", bigSentenceWords, false},
		{"big", bigSentenceWords + 1, true},
	}
	for _, c := range cases {
		fsys := fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte(
				`<section class="fact" data-topic="purring"><p class="` + c.class + `">` +
					sentenceOf(c.words) + `</p></section>`)},
			"img/a.png": &fstest.MapFile{Data: pngUniform(t, 10, 10)},
		}
		got := runRule(t, "html.sentence-budget", fsys)
		if c.fires && len(got) == 0 {
			t.Errorf("p.%s with %d words did not fire", c.class, c.words)
		}
		if !c.fires && len(got) != 0 {
			t.Errorf("p.%s with %d words fired: %s", c.class, c.words, joinFindings(got))
		}
		if c.fires && len(got) > 0 && !strings.Contains(got[0].Detail, "purring") {
			t.Errorf("finding does not name the paragraph it fired on: %q", got[0].Detail)
		}
	}

	// The wording the on-disk fixture and the criterion both use, counted here
	// so a reworded fixture cannot quietly stop being the boundary case.
	if n := wordCount(strings.TrimSuffix(littleSentence13, ".")); n != 13 {
		t.Errorf("littleSentence13 has %d words, want 13", n)
	}
	if n := wordCount(strings.TrimSuffix(littleSentence12, ".")); n != 12 {
		t.Errorf("littleSentence12 has %d words, want 12", n)
	}
}

// TestImgSrcIsResolvedAgainstThePageThatCarriesIt covers what no fixture does:
// a page in a subdirectory, and a root-relative src. Resolving both against the
// tree root, or both against the page, is wrong in one of the two cases and
// silent in the other.
func TestImgSrcIsResolvedAgainstThePageThatCarriesIt(t *testing.T) {
	for _, c := range []struct{ page, ref, want string }{
		{"index.html", "img/cat-a.png", "img/cat-a.png"},
		{"pages/about.html", "img/cat-a.png", "pages/img/cat-a.png"},
		{"pages/about.html", "../img/cat-a.png", "img/cat-a.png"},
		{"pages/about.html", "/img/cat-a.png", "img/cat-a.png"},
		{"index.html", "img/cat-a.png?v=2", "img/cat-a.png"},
		{"index.html", "img/cat-a.png#top", "img/cat-a.png"},
	} {
		if got := resolveRef(c.page, c.ref); got != c.want {
			t.Errorf("resolveRef(%q, %q) = %q, want %q", c.page, c.ref, got, c.want)
		}
	}

	// A page one directory down, whose src is correct from the tree root and
	// wrong from the page. The whole point of resolving relative to the page is
	// that this is a finding.
	fsys := goodSite(t)
	fsys["pages/about.html"] = &fstest.MapFile{Data: []byte(
		`<img src="img/cat-a.png" alt="a cat">`)}
	got := runRule(t, "html.img-src", fsys)
	if len(got) != 1 {
		t.Fatalf("got %d findings (%s), want 1", len(got), joinFindings(got))
	}
	if !strings.Contains(got[0].Detail, "pages/img/cat-a.png") {
		t.Errorf("finding does not name what the src resolved to: %q", got[0].Detail)
	}

	// An off-site src is html.no-external-ref's finding, not this one's.
	external := swap(t, goodSite(t), "index.html", `src="img/cat-b.png"`, `src="https://cdn.example.com/cat-b.png"`)
	if got := runRule(t, "html.img-src", external); len(got) != 0 {
		t.Errorf("html.img-src also fired on an off-site src: %s", joinFindings(got))
	}
	if got := runRule(t, "html.no-external-ref", external); len(got) == 0 {
		t.Error("nothing reported an off-site img src")
	}
}
