package sitecheck

import (
	"bytes"
	"image"
	"io/fs"
	"path"
	"sort"
	"strings"

	// Registered so image.DecodeConfig can measure the formats a static site
	// actually ships. A format with no decoder here is reported as unmeasurable
	// rather than skipped — see checkImagesDimensions.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// imageFile is one discovered image. cfgErr is kept rather than dropped: an
// image nobody could measure is UNKNOWN, and UNKNOWN satisfies nothing.
type imageFile struct {
	path   string
	size   int64
	cfg    image.Config
	cfgErr error
}

type textFile struct {
	path string
	body string
}

// site is everything the rules read, parsed once. Rules take a *site rather
// than an fs.FS so that a rule cannot reach for a file the loader did not
// account for, and so twenty rules do not re-read the same page twenty times.
type site struct {
	images  []imageFile
	credits *creditsFile
	htmls   []*htmlFile
	csss    []*cssFile
}

// imageExts is what counts as an image file for discovery and for the credits
// bijection. It is deliberately wider than the set of formats we can decode:
// shipping a .webp with no credit row should be a bijection failure, not an
// invisible file.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".avif": true, ".svg": true,
}

const creditsPath = "img/CREDITS.tsv"

func loadSite(fsys fs.FS) (*site, error) {
	s := &site{}
	var creditsBody string
	creditsFound := false

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
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
			img.cfg, _, img.cfgErr = image.DecodeConfig(bytes.NewReader(b))
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
