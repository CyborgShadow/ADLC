package sitecheck

import "strings"

// How a sentence is counted, stated once so the criterion and the check cannot
// disagree about what they measure:
//
//	A sentence is a run of text delimited by '.', '!' or '?'. The text is split
//	on those three characters and nothing else; each piece with a non-space
//	character in it is one sentence, and its length is len(strings.Fields) —
//	whitespace-separated tokens, so "12-week-old" is one word and "Dr. Smith"
//	is two sentences.
//
// That last consequence is the point of writing it down rather than leaving it
// to the reader: the definition is crude, it will call an abbreviation a
// sentence boundary, and a budget argued about later should be argued about
// against this sentence rather than against whatever the arguer assumed. It is
// pinned by TestSentencesPinsHowASentenceIsCounted.
const sentenceDelimiters = ".!?"

// littleSentenceWords and bigSentenceWords are the budgets. They are per
// sentence, not per paragraph: a paragraph budget lets one unreadable
// forty-word sentence hide behind four short ones.
const (
	littleSentenceWords = 12
	bigSentenceWords    = 30
)

func sentences(text string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune(sentenceDelimiters, r)
	}) {
		if strings.TrimSpace(part) != "" {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}

func wordCount(sentence string) int { return len(strings.Fields(sentence)) }
