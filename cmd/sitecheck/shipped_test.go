package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The acceptance criteria for the cat page are assertions about the tree this
// repository actually ships, and until this file existed nothing in `go test`
// made any of them. main_test.go holds the command to its exit-code contract
// over fixtures; the fixtures are conforming by construction, so the suite
// stayed green over a real site/cats carrying a CC-BY-NC-4.0 licence, a .webp
// no registered decoder can measure, and an @import of Google Fonts — all three
// at once. The criteria were met only for as long as somebody kept retyping the
// shell commands by hand.
//
// Three of the criteria have no rule behind them at all, so `sitecheck` exiting
// 0 does not establish them and these tests are the only thing that does:
//
//   - parseCredits reads file, licence_id and source_url. title, author and
//     licence_url are never read, so AC-3's non-empty requirement on those three
//     is asserted here.
//   - images.size caps each file at 250 KB; nothing caps the directory, so a
//     hundred conforming files would pass. AC-4's total budget is asserted here.
//   - no css rule looks for a third-party reference, so AC-5 is asserted here.
//
// AC-7 is deliberately absent: it is a claim about what the builder's envelope
// recorded, not about this tree, and a test that read the ledger would be
// checking a different artefact than the one it is compiled beside.

// shippedSite is the tree the criteria are about, relative to this package.
const shippedSite = "../../site/cats"

// The budgets are restated from the criteria rather than imported from
// internal/sitecheck, because a test that reads the same constant as the code
// it checks agrees with it by construction: raise maxImageBytes and a test
// sharing it moves silently. These numbers are what the item was accepted
// against.
const (
	maxShippedImageBytes = 250 * 1024
	maxShippedImgDirKB   = 1536
)

// admittedExtensions is AC-8's whitelist, lowercased.
var admittedExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true}

// thirdParty is AC-5's grep. url() with a scheme or a protocol-relative host is
// a fetch off the machine; a relative url() is the page's own image and is
// fine.
var thirdParty = regexp.MustCompile(`@import|@font-face|url\((https?:|//)`)

// criterion is one acceptance criterion expressed twice: as a check that must
// find nothing over a conforming tree, and as the mutation that must make it
// complain. Both halves are required — a guard exercised only where it fires
// passes vacuously the day it starts flagging everything, and one exercised
// only over the shipped tree passes vacuously the day it stops asserting
// anything at all.
type criterion struct {
	id string
	// check returns one string per problem; empty means the criterion is met.
	check func(t *testing.T, dir string) []string
	// breakIt mutates a throwaway copy of the tree so that check must complain.
	breakIt func(t *testing.T, dir string)
}

func criteria() []criterion {
	return []criterion{
		{
			id: "AC-1 credits,images,css exits 0 and reports six images",
			check: func(t *testing.T, dir string) []string {
				return checkCLI(t, dir, "credits,images,css", 6)
			},
			breakIt: func(t *testing.T, dir string) {
				// Over budget by a wide margin, so the mutation cannot land
				// inside a rounding argument.
				grow(t, filepath.Join(dir, "img", "orange-tabby.jpg"), maxShippedImageBytes)
			},
		},
		{
			id: "AC-2 every licence_id is one the site may ship, one row per image",
			check: func(t *testing.T, dir string) []string {
				var out []string
				rows, header := readCredits(t, dir)
				allowed := map[string]bool{"CC0-1.0": true, "PDM-1.0": true, "public-domain": true}
				for _, r := range rows {
					if !allowed[r["licence_id"]] {
						out = append(out, r["file"]+": licence_id "+r["licence_id"]+" is not CC0-1.0, PDM-1.0 or public-domain")
					}
				}
				if header != 1 {
					out = append(out, "CREDITS.tsv does not have exactly one header row")
				}
				// The bijection, stated as the criterion states it: one row per
				// image file, one image file per row.
				credited := map[string]bool{}
				for _, r := range rows {
					if credited[r["file"]] {
						out = append(out, r["file"]+": credited twice, and two credits for one file cannot both be the credit")
					}
					credited[r["file"]] = true
				}
				for _, name := range imageFiles(t, dir) {
					if !credited[name] {
						out = append(out, name+": no row in CREDITS.tsv")
					}
					delete(credited, name)
				}
				for name := range credited {
					out = append(out, name+": credited but not present under img/")
				}
				return out
			},
			breakIt: func(t *testing.T, dir string) {
				replaceInFile(t, filepath.Join(dir, "img", "CREDITS.tsv"), "\tCC0-1.0\t", "\tCC-BY-NC-4.0\t")
			},
		},
		{
			id: "AC-3 every row carries filename, title, author, source_url and licence_url, https only",
			check: func(t *testing.T, dir string) []string {
				var out []string
				rows, _ := readCredits(t, dir)
				for _, col := range []string{"file", "title", "author", "source_url", "licence_url"} {
					for _, r := range rows {
						if r[col] == "" {
							out = append(out, r["file"]+": "+col+" is empty")
						}
					}
				}
				for _, r := range rows {
					if !strings.HasPrefix(r["source_url"], "https://") {
						out = append(out, r["file"]+": source_url "+r["source_url"]+" does not begin https://")
					}
				}
				return out
			},
			breakIt: func(t *testing.T, dir string) {
				// author, not source_url: source_url has a rule behind it and
				// author has none, so blanking author is the mutation that
				// distinguishes this test from the checker.
				replaceInFile(t, filepath.Join(dir, "img", "CREDITS.tsv"), "\tAdinaVoicu\t", "\t\t")
			},
		},
		{
			id: "AC-4 no image over 250 KB",
			check: func(t *testing.T, dir string) []string {
				var out []string
				for _, name := range imageFiles(t, dir) {
					if n := sizeOf(t, filepath.Join(dir, "img", name)); n > maxShippedImageBytes {
						out = append(out, name+": over the 250 KB per-file budget")
					}
				}
				return out
			},
			breakIt: func(t *testing.T, dir string) {
				grow(t, filepath.Join(dir, "img", "tabby-kitten.jpg"), maxShippedImageBytes)
			},
		},
		{
			id: "AC-4 img/ totals at most 1536 KB",
			check: func(t *testing.T, dir string) []string {
				// The criterion says du -k -s, which reports blocks allocated
				// and so answers differently on different filesystems. What the
				// budget is actually protecting is the bytes a phone downloads,
				// which is what this measures — portable, and never looser than
				// the du reading.
				var total int64
				entries, err := os.ReadDir(filepath.Join(dir, "img"))
				if err != nil {
					t.Fatalf("read img/: %v", err)
				}
				for _, e := range entries {
					if !e.IsDir() {
						total += sizeOf(t, filepath.Join(dir, "img", e.Name()))
					}
				}
				if total > maxShippedImgDirKB*1024 {
					return []string{"img/ holds " + kb(total) + " KB, over the 1536 KB budget"}
				}
				return nil
			},
			breakIt: func(t *testing.T, dir string) {
				// Every copy is a real, conforming, under-budget jpeg: only the
				// total is wrong, so the per-file guard cannot be what fires.
				src := filepath.Join(dir, "img", "orange-tabby.jpg")
				body, err := os.ReadFile(src)
				if err != nil {
					t.Fatalf("read %s: %v", src, err)
				}
				for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
					write(t, filepath.Join(dir, "img", "copy-"+n+".jpg"), body)
				}
			},
		},
		{
			id: "AC-5 the stylesheet fetches nothing from a third party",
			check: func(t *testing.T, dir string) []string {
				var out []string
				body := read(t, filepath.Join(dir, "style.css"))
				for i, line := range strings.Split(body, "\n") {
					if m := thirdParty.FindString(line); m != "" {
						out = append(out, "style.css:"+strconv.Itoa(i+1)+": "+m)
					}
				}
				return out
			},
			breakIt: func(t *testing.T, dir string) {
				appendTo(t, filepath.Join(dir, "style.css"),
					"\n@import url(https://fonts.googleapis.com/css?family=Comic);\n")
			},
		},
		{
			id: "AC-6 -rule css exits 0",
			check: func(t *testing.T, dir string) []string {
				return checkCLI(t, dir, "css", 6)
			},
			breakIt: func(t *testing.T, dir string) {
				appendTo(t, filepath.Join(dir, "style.css"), "\n.fine-print { font-size: 11px; }\n")
			},
		},
		{
			id: "AC-8 every committed image carries an admitted extension",
			check: func(t *testing.T, dir string) []string {
				var out []string
				entries, err := os.ReadDir(filepath.Join(dir, "img"))
				if err != nil {
					t.Fatalf("read img/: %v", err)
				}
				for _, e := range entries {
					name := e.Name()
					if name == "CREDITS.tsv" {
						continue
					}
					if !admittedExtensions[strings.ToLower(filepath.Ext(name))] {
						out = append(out, name+": extension is not .jpg, .jpeg, .png or .gif")
					}
				}
				return out
			},
			breakIt: func(t *testing.T, dir string) {
				write(t, filepath.Join(dir, "img", "sneaky.webp"), riffWebPHeader)
			},
		},
		{
			id: "AC-8 -rule images exits 0 over format as well as size and dimensions",
			check: func(t *testing.T, dir string) []string {
				return checkCLI(t, dir, "images", 6)
			},
			breakIt: func(t *testing.T, dir string) {
				// The exact hole the item's dependency on S1-006 names: with
				// only gif, jpeg and png registered, DecodeConfig over a
				// RIFF/WEBP header returns "unknown format" with a zero-valued
				// config, and 0x0 is inside every dimension budget. Under a
				// .jpg extension so the extension guard above is not what
				// fires.
				write(t, filepath.Join(dir, "img", "sneaky.jpg"), riffWebPHeader)
			},
		},
	}
}

// riffWebPHeader is enough of a WebP container for image.DecodeConfig to
// recognise that it cannot read it.
var riffWebPHeader = []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00")

// TestShippedSiteMeetsItsCriteria is the clean case: every criterion, over the
// tree the repository actually ships.
func TestShippedSiteMeetsItsCriteria(t *testing.T) {
	cs := criteria()
	if len(cs) == 0 {
		t.Fatal("no criteria declared: a run that discovered zero units of work has failed, not passed")
	}
	for _, c := range cs {
		t.Run(c.id, func(t *testing.T) {
			if problems := c.check(t, shippedSite); len(problems) > 0 {
				t.Errorf("%s is not met by %s:\n\t%s", c.id, shippedSite, strings.Join(problems, "\n\t"))
			}
		})
	}
}

// TestShippedSiteCriteriaFireWhenBroken is the firing case. Each criterion is
// run over a throwaway copy of the shipped tree with one thing wrong in it, and
// has to complain — otherwise the clean case above is an assertion about
// nothing, which is how the whole suite came to pass over a site carrying a
// non-commercial licence and a Google Fonts import.
func TestShippedSiteCriteriaFireWhenBroken(t *testing.T) {
	for _, c := range criteria() {
		t.Run(c.id, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cats")
			copyTree(t, shippedSite, dir)
			// The copy has to start clean, or the mutation below proves
			// nothing about what made the check complain.
			if problems := c.check(t, dir); len(problems) > 0 {
				t.Fatalf("the unmutated copy already fails %s:\n\t%s", c.id, strings.Join(problems, "\n\t"))
			}
			c.breakIt(t, dir)
			if problems := c.check(t, dir); len(problems) == 0 {
				t.Errorf("%s reported nothing over a tree deliberately broken for it", c.id)
			}
		})
	}
}

// checkCLI runs the command the criterion names and reports what a caller
// would see go wrong. wantImages is asserted because "images checked: 0" and a
// passing run are otherwise the same output.
func checkCLI(t *testing.T, dir, rule string, wantImages int) []string {
	t.Helper()
	code, out, errOut := runCLI(t, "-dir", dir, "-rule", rule)
	var problems []string
	if code != 0 {
		problems = append(problems, "-rule "+rule+" exited "+strconv.Itoa(code)+"; stdout "+strings.TrimSpace(out)+" stderr "+strings.TrimSpace(errOut))
	}
	// Exactly one line: a run that printed findings and still exited 0 is the
	// failure this is watching for.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := "images checked: " + strconv.Itoa(wantImages)
	if len(lines) != 1 || lines[0] != want {
		problems = append(problems, "-rule "+rule+" printed "+strings.Join(lines, " | ")+", want exactly "+want)
	}
	return problems
}

// readCredits returns the data rows keyed by column name, plus how many header
// rows the file has. Columns are located by name so that a column inserted
// upstream cannot shift every licence into a different assertion.
func readCredits(t *testing.T, dir string) (rows []map[string]string, headers int) {
	t.Helper()
	body := read(t, filepath.Join(dir, "img", "CREDITS.tsv"))
	var cols []string
	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := strings.Split(raw, "\t")
		if cols == nil {
			cols = fields
			headers++
			continue
		}
		row := map[string]string{}
		for i, name := range cols {
			if i < len(fields) {
				row[strings.TrimSpace(name)] = strings.TrimSpace(fields[i])
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatal("CREDITS.tsv has no data rows: a check over zero rows establishes nothing")
	}
	return rows, headers
}

// imageFiles lists everything under img/ that is not the credits file.
func imageFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "img"))
	if err != nil {
		t.Fatalf("read img/: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && e.Name() != "CREDITS.tsv" {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("no image files under img/: a run that discovered zero units of work has failed")
	}
	return out
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, s, d)
			continue
		}
		body, err := os.ReadFile(s)
		if err != nil {
			t.Fatalf("read %s: %v", s, err)
		}
		write(t, d, body)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func write(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	write(t, path, []byte(read(t, path)+text))
}

// replaceInFile fails when old is absent, so a mutation that has stopped
// matching the file reports itself rather than leaving the firing case
// silently unbroken.
func replaceInFile(t *testing.T, path, old, new string) {
	t.Helper()
	body := read(t, path)
	if !strings.Contains(body, old) {
		t.Fatalf("%s does not contain %q, so this mutation would break nothing", path, old)
	}
	write(t, path, []byte(strings.Replace(body, old, new, 1)))
}

// grow appends zero bytes to push a file past a byte budget. The bytes land
// after the image header, so the file still decodes and only the size guard
// has anything to say about it.
func grow(t *testing.T, path string, by int) {
	t.Helper()
	write(t, path, append([]byte(read(t, path)), make([]byte, by)...))
}

func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func kb(n int64) string { return strconv.Itoa(int((n + 1023) / 1024)) }
