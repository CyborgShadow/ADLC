// Command sitecheck checks a static site directory against the sitecheck rules
// and reports what it found.
//
// Exit codes are the contract, so they are stated here and nowhere else:
//
//	0  every selected rule passed, and at least one image was examined
//	1  a rule fired, or the run examined nothing
//	2  the invocation itself was wrong — an unknown -rule family, an
//	   unreadable -dir
//
// 1 and 2 are kept apart because a caller that cannot tell them apart treats
// its own typo as a failing site, and fixes the site.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/CyborgShadow/ADLC/internal/sitecheck"
)

func main() {
	os.Exit(run())
}

func run() int {
	dir := flag.String("dir", ".", "the site directory to check")
	rule := flag.String("rule", "", "comma-separated rule families to run (default: all of "+familyList()+")")
	flag.Usage = usage
	flag.Parse()

	families, err := sitecheck.ParseFamilies(*rule)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sitecheck: "+err.Error())
		usage()
		return 2
	}

	info, err := os.Stat(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sitecheck: -dir %s: %v\n", *dir, err)
		usage()
		return 2
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "sitecheck: -dir %s is not a directory\n", *dir)
		usage()
		return 2
	}

	res, err := sitecheck.Check(os.DirFS(*dir), families)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sitecheck: %s: %v\n", *dir, err)
		return 2
	}

	// Findings are the result, so they go to stdout where a caller can grep
	// them; only the tally goes to stderr. A passing run has no findings, so
	// stdout still carries exactly the one count line.
	for _, f := range res.Findings {
		fmt.Println(f.String())
	}
	if !res.OK() {
		fmt.Fprintf(os.Stderr, "sitecheck: %d finding(s) in %s, %d image(s) examined\n",
			len(res.Findings), *dir, res.ImagesChecked)
		return 1
	}
	fmt.Printf("images checked: %d\n", res.ImagesChecked)
	return 0
}

func familyList() string {
	out := ""
	for i, f := range sitecheck.Families {
		if i > 0 {
			out += ","
		}
		out += f
	}
	return out
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage: sitecheck [-dir DIR] [-rule FAMILY[,FAMILY...]]

  -dir DIR     the site directory to check (default ".")
  -rule LIST   comma-separated subset of: %s
               omitted means every family

exit 0 nothing to report, 1 a rule fired or nothing was examined, 2 bad usage
`, familyList())
}
