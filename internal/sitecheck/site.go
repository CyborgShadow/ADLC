package sitecheck

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

type textFile struct {
	path string
	body string
}

// site is everything the rules read, parsed once. Rules take a *site rather
// than an fs.FS so that a rule cannot reach for a file the loader did not
// account for, and so twenty rules do not re-read the same page twenty times.
type site struct {
	// files is every path the walk saw, image or not. It is what a reference on
	// a page is resolved against: a rule that answered "does this file exist"
	// by reaching back into the fs.FS could resolve a path the loader never
	// walked, and would then disagree with the rest of the package about what
	// the checked tree contains.
	files   map[string]bool
	images  []imageFile
	credits *creditsFile
	htmls   []*htmlFile
	csss    []*cssFile
}

// imageExts is what counts as an image file for discovery and for the credits
// bijection. It is deliberately wider than admittedImageFormats, and is not a
// second statement of that set: shipping a .webp with no credit row should be a
// bijection failure and an images.decodable failure naming the format, not an
// invisible file no rule ever mentions.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".avif": true, ".svg": true,
}

const creditsPath = "img/CREDITS.tsv"

func loadSite(fsys fs.FS) (*site, error) {
	s := &site{files: map[string]bool{}}
	var creditsBody string
	creditsFound := false

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		s.files[p] = true
		ext := strings.ToLower(path.Ext(p))
		switch {
		case p == creditsPath:
			creditsFound = true
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			creditsBody = string(b)
		case imageExts[ext]:
			img := imageFile{path: p}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			img.size = int64(len(b))
			img.cfg, img.cfgErr = decodeImageHeader(b)
			s.images = append(s.images, img)
		case ext == ".html" || ext == ".htm":
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			s.htmls = append(s.htmls, &htmlFile{textFile: textFile{path: p, body: string(b)}, root: parseHTML(string(b))})
		case ext == ".css":
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			s.csss = append(s.csss, &cssFile{textFile: textFile{path: p, body: string(b)}, rules: parseCSS(string(b))})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(s.images, func(i, j int) bool { return s.images[i].path < s.images[j].path })
	sort.Slice(s.htmls, func(i, j int) bool { return s.htmls[i].path < s.htmls[j].path })
	sort.Slice(s.csss, func(i, j int) bool { return s.csss[i].path < s.csss[j].path })

	s.credits = parseCredits(creditsPath, creditsBody, creditsFound)
	return s, nil
}
