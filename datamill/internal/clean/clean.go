// Package clean normalizes extracted text into tidy, paragraph-separated
// prose suitable for chunking.
//
// Contract: output uses "\n" line endings, paragraphs are separated by
// exactly one blank line, there is no leading/trailing whitespace, and no
// control characters other than "\n" survive. When Reflow is set, every
// paragraph is a single line.
package clean

import (
	"regexp"
	"strings"
	"unicode"
)

// Options controls the normalization passes.
type Options struct {
	// Reflow joins hard-wrapped lines inside a paragraph into one line and
	// removes end-of-line hyphenation ("exam-\nple" -> "example").
	Reflow bool
	// DropPageNumbers removes lines that consist solely of a page number
	// ("12", "Page 12", "xiv"). Intended for PDF output.
	DropPageNumbers bool
}

var (
	pageNumRe = regexp.MustCompile(`(?i)^(page\s*|p\.\s*)?\d{1,4}$`)
	romanRe   = regexp.MustCompile(`(?i)^[ivxlcdm]{1,8}$`)
	spacesRe  = regexp.MustCompile(`[ \t]{2,}`)
)

// Text applies the normalization passes described in Options.
func Text(s string, o Options) string {
	s = normalizeChars(s)
	lines := strings.Split(s, "\n")

	var paras []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		if o.Reflow {
			paras = append(paras, joinWrapped(cur))
		} else {
			paras = append(paras, strings.Join(cur, "\n"))
		}
		cur = cur[:0]
	}

	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if o.Reflow {
			ln = strings.TrimLeft(ln, " \t")
		}
		ln = collapseSpaces(ln)
		if strings.TrimSpace(ln) == "" {
			flush()
			continue
		}
		if o.DropPageNumbers && isPageNumber(strings.TrimSpace(ln)) {
			continue
		}
		cur = append(cur, ln)
	}
	flush()
	return strings.Join(paras, "\n\n")
}

// normalizeChars maps line endings and odd whitespace to a canonical form
// and strips control characters other than "\n".
func normalizeChars(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\f", "\n\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == 0x00a0 || r == 0x2007 || r == 0x202f || r == 0x3000:
			b.WriteByte(' ')
		case r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff || r == 0x00ad:
			// zero-width / BOM / soft hyphen: drop
		case r == 0xfffd:
			// replacement char from a bad decode: drop
		case unicode.IsControl(r):
			// drop other controls
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// joinWrapped joins hard-wrapped lines of one paragraph, undoing
// end-of-line hyphenation when the next line starts with a lowercase letter.
func joinWrapped(lines []string) string {
	var b strings.Builder
	for i, ln := range lines {
		ln = strings.TrimSpace(ln)
		if i == 0 {
			b.WriteString(ln)
			continue
		}
		prev := b.String()
		if strings.HasSuffix(prev, "-") && startsLower(ln) && len(prev) >= 2 && isLetterByte(prev[len(prev)-2]) {
			// "exam-" + "ple" -> "example"
			b.Reset()
			b.WriteString(prev[:len(prev)-1])
			b.WriteString(ln)
			continue
		}
		b.WriteByte(' ')
		b.WriteString(ln)
	}
	return b.String()
}

func startsLower(s string) bool {
	for _, r := range s {
		return unicode.IsLower(r)
	}
	return false
}

func isLetterByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isPageNumber(s string) bool {
	return pageNumRe.MatchString(s) || romanRe.MatchString(s)
}

// LooksHardWrapped reports whether a text appears to be hard-wrapped at a
// fixed column (e.g. a Project Gutenberg file), meaning it should be
// reflowed. The heuristic: among non-blank lines, most are between 40 and
// 100 characters and a good share of them do not end in sentence
// punctuation.
func LooksHardWrapped(s string) bool {
	lines := strings.Split(normalizeChars(s), "\n")
	var total, midLen, noPunct int
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		total++
		n := len([]rune(ln))
		if n >= 40 && n <= 100 {
			midLen++
		}
		last := ln[len(ln)-1]
		if last != '.' && last != '!' && last != '?' && last != '"' && last != ':' && last != ')' {
			noPunct++
		}
	}
	if total < 8 {
		return false
	}
	return midLen*100/total >= 60 && noPunct*100/total >= 40
}

// collapseSpaces squeezes runs of spaces/tabs inside a line to one space,
// leaving any leading indentation intact.
func collapseSpaces(ln string) string {
	trimmed := strings.TrimLeft(ln, " \t")
	indent := ln[:len(ln)-len(trimmed)]
	return indent + spacesRe.ReplaceAllString(trimmed, " ")
}
