package sitecheck

import (
	"bytes"
	"fmt"
	"image"
	"strings"

	// Registered so image.DecodeConfig can measure a header. These three
	// imports and admittedImageFormats below are one declaration written twice:
	// the import registers a decoder, the list is what a finding tells the
	// reader, and they are stated here and nowhere else in the package. Split
	// across two files they drift, and the drift is silent in both directions —
	// a decoder registered but never named reads as unsupported, and a format
	// named but never registered reads as supported right up to the file that
	// arrives in it.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// admittedImageFormats is the whole set this site may ship, and it is exactly
// what the standard library decodes. A fourth format means a module dependency
// (golang.org/x/image), which is a decision about go.mod rather than about an
// image, so it is taken deliberately or not at all — a checker that quietly
// admitted a format it cannot measure would report green over a file nobody
// established anything about.
var admittedImageFormats = []string{"gif", "jpeg", "png"}

// imageFile is one discovered image. cfgErr is kept rather than dropped: an
// image nobody could measure is UNKNOWN, and UNKNOWN satisfies nothing.
type imageFile struct {
	path   string
	size   int64
	cfg    image.Config
	cfgErr error
}

// decodeImageHeader reads dimensions from the header rather than the pixels.
// It sits beside the decoder imports because it is the only caller that
// depends on which of them are present.
func decodeImageHeader(b []byte) (image.Config, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	return cfg, err
}

// maxImageBytes is 250 KB counted as 250 * 1024, the sense of KB that every
// file browser on the reviewer's machine will show them. Stating it here is
// what stops the check and the criterion disagreeing about a file at 250,500
// bytes.
const maxImageBytes = 250 * 1024

// maxImageEdge caps the longest edge. Anything larger is pixels the phone
// downloads and then throws away in the scaler.
const maxImageEdge = 1280

func checkImagesSize(s *site) []Finding {
	var out []Finding
	for _, img := range s.images {
		if img.size > maxImageBytes {
			out = append(out, Finding{RuleID: "images.size", Path: img.path,
				Detail: fmt.Sprintf("%d bytes exceeds the %d byte (250 KB) budget", img.size, maxImageBytes)})
		}
	}
	return out
}

// checkImagesDecodable refuses a file under an image extension whose header no
// admitted decoder can read. It is the failure that has to be named on its own,
// because the shape of the hole is that it looks like a pass: DecodeConfig
// returns a zero-valued Config alongside its error, and 0x0 is comfortably
// inside every dimension budget. An image nobody measured is UNKNOWN, and
// UNKNOWN is never a pass.
func checkImagesDecodable(s *site) []Finding {
	var out []Finding
	for _, img := range s.images {
		if img.cfgErr == nil {
			continue
		}
		out = append(out, Finding{RuleID: "images.decodable", Path: img.path,
			Detail: fmt.Sprintf("header could not be read by image.DecodeConfig (%v), so nothing about this file was established; the admitted formats are %s",
				img.cfgErr, strings.Join(admittedImageFormats, ", "))})
	}
	return out
}

// checkImagesDimensions measures with image.DecodeConfig, which reads the
// header rather than the pixels.
func checkImagesDimensions(s *site) []Finding {
	var out []Finding
	for _, img := range s.images {
		if img.cfgErr != nil {
			// images.decodable owns the unreadable header, and both rules are
			// in the images family, so no selection reaches this one without
			// reaching that one. Reporting here as well would put two findings
			// on one defect and hide which of them is the thing to fix.
			continue
		}
		edge := img.cfg.Width
		if img.cfg.Height > edge {
			edge = img.cfg.Height
		}
		if edge > maxImageEdge {
			out = append(out, Finding{RuleID: "images.dimensions", Path: img.path,
				Detail: fmt.Sprintf("%dx%d has a longest edge of %d px, over the %d px budget",
					img.cfg.Width, img.cfg.Height, edge, maxImageEdge)})
		}
	}
	return out
}
