package sitecheck

import "fmt"

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

// checkImagesDimensions measures with image.DecodeConfig, which reads the
// header rather than the pixels. A file it cannot decode is reported, not
// skipped: an image nobody measured is UNKNOWN, and UNKNOWN is never a pass.
func checkImagesDimensions(s *site) []Finding {
	var out []Finding
	for _, img := range s.images {
		if img.cfgErr != nil {
			out = append(out, Finding{RuleID: "images.dimensions", Path: img.path,
				Detail: "could not be measured with image.DecodeConfig (" + img.cfgErr.Error() + "): unmeasured is not within budget"})
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
