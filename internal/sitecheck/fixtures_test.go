package sitecheck

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"strings"
	"testing"
	"testing/fstest"
)

// Every fixture in this file is built in memory. The suite reads no file and
// opens no socket, so it gives the same answer on a build host with no egress
// and no checkout as it does on a laptop — and a green run here can never mean
// "the fixture directory happened to be lying around".

const goodHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Five things about cats</title>
<link rel="stylesheet" href="style.css">
</head>
<body>
<h1>Five things about cats</h1>

<section class="fact" data-topic="baby-faces" data-source="s1">
<p class="little">Cats keep a baby face all their lives.</p>
<p class="big">Round heads, big eyes and small noses read to us as an infant face. That shape pulls the same care response a human baby does.</p>
<img src="img/cat-a.png" alt="A cat with a round face and wide eyes">
</section>

<section class="fact uncertain" data-topic="purring" data-source="s2">
<p class="little">A purr is not only a happy sound.</p>
<p class="big">Cats purr when they are content and also when they are hurt or frightened. Whether the sound is driven by muscle or by the larynx alone is a maybe, not a finding.</p>
</section>

<section class="fact" data-topic="oxytocin-touch" data-source="s3">
<p class="little">Stroking a cat can raise oxytocin in both of you.</p>
<p class="big">Calm, wanted contact is linked with a rise in oxytocin in the person. Wanted is the load bearing word here.</p>
<img src="img/cat-b.png" alt="A hand resting on a cat that is leaning into it">
</section>

<section class="fact" data-topic="self-domestication" data-source="s4">
<p class="little">Cats moved in on us before we invited them.</p>
<p class="big">Grain stores drew rodents, rodents drew wildcats, and the tamest of those bred nearest to people. Nobody set out to make a house cat.</p>
</section>

<section class="fact" data-topic="choosing-a-person" data-source="s5">
<p class="little">A cat gives more time to whoever lets it start things.</p>
<p class="big">Who feeds a cat matters less than who reads it well and leaves it alone when it asks. Being chosen is mostly about being restful.</p>
</section>

<section id="sources">
<h2>Sources</h2>
<ul>
<li id="s1">Borgi and Cirulli, 2016. <a href="https://pmc.ncbi.nlm.nih.gov/articles/PMC4782005/">Pet Face: Mechanisms Underlying Human-Animal Relationships</a></li>
<li id="s2">Russo, Schild and Knörnschild, 2025. <a href="https://pmc.ncbi.nlm.nih.gov/articles/PMC12695941/">Meows encode less individual information than purrs</a></li>
<li id="s3">Nagasawa, Kimura, Masuda and Uchiyama, 2023. <a href="https://pmc.ncbi.nlm.nih.gov/articles/PMC10340037/">Effects of interactions with cats on the state of their owners</a></li>
<li id="s4">Hu and others, 2014. <a href="https://pmc.ncbi.nlm.nih.gov/articles/PMC3890806/">Earliest evidence for commensal processes of cat domestication</a></li>
<li id="s5">Turner, 2021. <a href="https://pmc.ncbi.nlm.nih.gov/articles/PMC8044293/">The Mechanics of Social Interactions Between Cats and Their Owners</a></li>
</ul>
</section>
</body>
</html>
`

const goodCSS = `:root { --ink: #1a1a1a; }

body { font-size: 18px; margin: 0; color: var(--ink); }
h1 { font-size: 32px; }
p.little { font-size: 22px; }
p.big { font-size: 18px; }
img { max-width: 100%; height: auto; }
.fact { max-width: 40rem; padding: 1rem; }

a { min-height: 44px; min-width: 44px; display: inline-block; }
button { min-height: 48px; min-width: 48px; }

@media (orientation: landscape) {
  .fact { max-width: 60rem; }
}
`

const goodCredits = "file\tlicence_id\tsource_url\tcredit\n" +
	"cat-a.png\tCC0-1.0\thttps://example.org/cat-a\tAnon\n" +
	"cat-b.png\tpublic-domain\thttps://example.org/cat-b\tAnon\n"

// pngUniform is a small, well-compressing image: a few hundred bytes whatever
// its dimensions, so a dimensions fixture can be 1400px wide without also
// tripping the size budget and making the test prove two things at once.
func pngUniform(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 0x99, G: 0x88, B: 0x77, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding fixture png: %v", err)
	}
	return buf.Bytes()
}

// pngNoise is deliberately incompressible, so its byte count is a function of
// its pixel count and the size fixture stays over budget without a magic blob
// of padding checked into the tree.
func pngNoise(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)), A: 0xff,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding fixture png: %v", err)
	}
	return buf.Bytes()
}

// littleSentence12 and littleSentence13 straddle the p.little budget by one
// word each. They are named here because the on-disk fixture under
// testdata/bad-html.sentence-budget carries the same thirteen-word sentence,
// and a criterion demonstrated with one wording and tested with another is a
// criterion demonstrated twice about two different things.
const (
	littleSentence12 = "Cats moved in on us before anybody in the world invited them."
	littleSentence13 = "Cats moved in on us long before anybody in the world invited them."
)

// riffWebP is a WEBP header and nothing else: enough bytes for a decoder to
// identify the format and refuse it, which is exactly the case the images
// family has to call a failure. It is built here rather than checked in
// because a .webp in the tree is a file the site may not ship — the admitted
// formats are gif, jpeg and png, and the *.webp attribute in .gitattributes
// records how one would be stored if it arrived, not permission to add one.
func riffWebP() []byte {
	b := []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00")
	return append(b, make([]byte, 0x18)...)
}

// goodSite is the conforming fixture. It is rebuilt on every call so that a
// mutation made for one firing case cannot leak into another case's clean run.
func goodSite(t *testing.T) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte(goodHTML)},
		"style.css":       &fstest.MapFile{Data: []byte(goodCSS)},
		"img/CREDITS.tsv": &fstest.MapFile{Data: []byte(goodCredits)},
		"img/cat-a.png":   &fstest.MapFile{Data: pngUniform(t, 800, 600)},
		"img/cat-b.png":   &fstest.MapFile{Data: pngUniform(t, 640, 480)},
		"README.txt":      &fstest.MapFile{Data: []byte("a file the checker has no rules about\n")},
	}
}

// emptySite holds no image at all: the case core.zero-work exists to refuse.
func emptySite(t *testing.T) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(goodHTML)},
		"style.css":  &fstest.MapFile{Data: []byte(goodCSS)},
	}
}

// swap edits one fixture file. It fails the test when old is absent, because a
// firing fixture whose mutation quietly stopped applying is a firing test that
// still passes while proving nothing.
func swap(t *testing.T, m fstest.MapFS, path, old, new string) fstest.MapFS {
	t.Helper()
	f, ok := m[path]
	if !ok {
		t.Fatalf("fixture has no %s to edit", path)
	}
	body := string(f.Data)
	if !strings.Contains(body, old) {
		t.Fatalf("fixture %s does not contain %q, so this mutation changes nothing", path, old)
	}
	m[path] = &fstest.MapFile{Data: []byte(strings.Replace(body, old, new, 1))}
	return m
}

func setFile(m fstest.MapFS, path string, data []byte) fstest.MapFS {
	m[path] = &fstest.MapFile{Data: data}
	return m
}

// sentenceOf builds a sentence of exactly n whitespace-separated words, the
// unit text.go counts in.
func sentenceOf(n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = "word"
	}
	return strings.Join(words, " ") + "."
}
