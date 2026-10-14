// Package chunk splits cleaned text into pieces sized for an LLM prompt.
//
// Input contract: text as produced by package clean (paragraphs separated by
// a blank line). Sizes are measured in runes, which is a stable, tokenizer
// independent proxy for tokens (roughly 4 runes per token for English).
//
// Output contract:
//   - no chunk is empty or whitespace-only;
//   - every chunk is at most Max runes long, plus at most Overlap runes of
//     prefix carried over from the previous chunk (UnitDocument excepted:
//     it is the whole text, whatever its size);
//   - paragraph boundaries are preferred, then sentence boundaries, then
//     word boundaries; a single word longer than Max is cut at Max;
//   - the concatenation of all chunks (ignoring overlap) contains every
//     non-whitespace character of the input, in order.
//
// Three units are supported (Options.Unit): UnitChunk packs paragraphs up
// to a target size (the default, for Q&A generation); UnitParagraph keeps
// one paragraph per chunk, so each chunk is a citable passage; and
// UnitDocument keeps the whole text as one chunk, for inputs that are
// complete works on their own, such as a short story.
package chunk

import (
	"regexp"
	"strings"
	"unicode"
)

// Options tunes the splitter. Zero values take the defaults below.
type Options struct {
	// Target is the preferred chunk size. Paragraphs are accumulated until
	// adding the next would exceed Target.
	Target int
	// Min is the smallest chunk the splitter is content with. A chunk
	// shorter than Min keeps growing past Target (up to Max), and trailing
	// runts are merged into their predecessor when that fits within Max.
	Min int
	// Max is the hard upper bound on chunk size (overlap excluded).
	Max int
	// Overlap is the number of runes from the end of the previous chunk to
	// prepend to each chunk (cut at a word boundary). 0 disables overlap.
	Overlap int
	// Headings, when true, treats Markdown headings ("# Title") and
	// "Chapter N" / "Part N" lines as hard section boundaries that chunks
	// do not straddle, except when merging a runt.
	Headings bool
	// Unit selects the chunking unit; "" means UnitChunk.
	Unit Unit
}

// Unit names what one chunk corresponds to.
type Unit string

// Units.
const (
	// UnitChunk packs consecutive paragraphs up to Target runes.
	UnitChunk Unit = "chunk"
	// UnitParagraph emits one chunk per paragraph. A paragraph longer than
	// Max is split at sentence boundaries; a paragraph shorter than Min
	// (a heading, a one-line reply) is attached to the paragraph that
	// follows it, or to the previous one when it is the last. Overlap is
	// ignored.
	UnitParagraph Unit = "paragraph"
	// UnitDocument emits the whole text as a single chunk, ignoring every
	// size option.
	UnitDocument Unit = "document"
)

// Units lists the valid Unit values.
var Units = []Unit{UnitChunk, UnitParagraph, UnitDocument}

// ValidUnit reports whether u is one of Units or empty.
func ValidUnit(u Unit) bool {
	if u == "" {
		return true
	}
	for _, k := range Units {
		if k == u {
			return true
		}
	}
	return false
}

// Default sizes, chosen to be comfortably inside a single LLM prompt while
// giving enough context to ask several questions about.
const (
	DefaultTarget = 1500
	DefaultMin    = 300
	DefaultMax    = 2000
)

func (o Options) withDefaults() Options {
	if o.Target <= 0 {
		o.Target = DefaultTarget
	}
	if o.Max <= 0 {
		o.Max = DefaultMax
	}
	if o.Min <= 0 {
		o.Min = DefaultMin
	}
	if o.Max < o.Target {
		o.Max = o.Target
	}
	if o.Min > o.Target {
		o.Min = o.Target
	}
	if o.Overlap < 0 {
		o.Overlap = 0
	}
	if o.Overlap > o.Target/2 {
		o.Overlap = o.Target / 2
	}
	return o
}

var headingRe = regexp.MustCompile(`(?i)^(#{1,6}\s+\S|(chapter|part|book)\s+([0-9]+|[ivxlcdm]+)\b)`)

// Split chunks text according to o.
func Split(text string, o Options) []string {
	o = o.withDefaults()
	paras := paragraphs(text)
	if len(paras) == 0 {
		return nil
	}
	switch o.Unit {
	case UnitDocument:
		return []string{strings.Join(paras, "\n\n")}
	case UnitParagraph:
		return splitParagraphs(paras, o)
	}

	var chunks []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			chunks = append(chunks, cur.String())
		}
		cur.Reset()
		curLen = 0
	}
	add := func(piece string) {
		n := runeLen(piece)
		if curLen == 0 {
			cur.WriteString(piece)
			curLen = n
			return
		}
		joined := curLen + 2 + n
		if joined <= o.Target || (curLen < o.Min && joined <= o.Max) {
			cur.WriteString("\n\n")
			cur.WriteString(piece)
			curLen = joined
			return
		}
		flush()
		cur.WriteString(piece)
		curLen = n
	}

	for _, p := range paras {
		if o.Headings && headingRe.MatchString(p) {
			flush()
		}
		if runeLen(p) <= o.Max {
			add(p)
			continue
		}
		for _, piece := range splitLong(p, o) {
			add(piece)
		}
	}
	flush()

	chunks = mergeRunts(chunks, o)
	if o.Overlap > 0 {
		chunks = applyOverlap(chunks, o.Overlap)
	}
	return chunks
}

// splitParagraphs implements UnitParagraph: one chunk per paragraph,
// over-long paragraphs split, runts glued to the following paragraph.
func splitParagraphs(paras []string, o Options) []string {
	var pieces []string
	for _, p := range paras {
		if runeLen(p) <= o.Max {
			pieces = append(pieces, p)
			continue
		}
		pieces = append(pieces, splitLong(p, o)...)
	}
	var out []string
	var runt string // pending short pieces, joined, waiting for a host
	for _, p := range pieces {
		if runeLen(p) < o.Min {
			runt = joinPara(runt, p)
			continue
		}
		if runt != "" {
			if runeLen(runt)+2+runeLen(p) <= o.Max {
				p = joinPara(runt, p)
			} else {
				out = append(out, runt)
			}
			runt = ""
		}
		out = append(out, p)
	}
	if runt != "" {
		if n := len(out); n > 0 && runeLen(out[n-1])+2+runeLen(runt) <= o.Max {
			out[n-1] = joinPara(out[n-1], runt)
		} else {
			out = append(out, runt)
		}
	}
	return out
}

func joinPara(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n\n" + b
}

// paragraphs splits on blank lines and drops empties.
func paragraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitLong breaks a single over-long paragraph into pieces of at most
// o.Max runes, preferring sentence boundaries and then word boundaries.
func splitLong(p string, o Options) []string {
	var pieces []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			pieces = append(pieces, strings.TrimSpace(cur.String()))
		}
		cur.Reset()
		curLen = 0
	}
	for _, s := range Sentences(p) {
		n := runeLen(s)
		if n > o.Max {
			flush()
			pieces = append(pieces, splitWords(s, o.Max)...)
			continue
		}
		if curLen > 0 && curLen+1+n > o.Target {
			flush()
		}
		if curLen > 0 {
			cur.WriteByte(' ')
			curLen++
		}
		cur.WriteString(s)
		curLen += n
	}
	flush()
	return pieces
}

// splitWords breaks s into pieces of at most max runes at word boundaries;
// a single word longer than max is cut.
func splitWords(s string, max int) []string {
	var pieces []string
	var cur []rune
	for _, w := range strings.Fields(s) {
		wr := []rune(w)
		for len(wr) > max {
			if len(cur) > 0 {
				pieces = append(pieces, string(cur))
				cur = cur[:0]
			}
			pieces = append(pieces, string(wr[:max]))
			wr = wr[max:]
		}
		if len(cur) > 0 && len(cur)+1+len(wr) > max {
			pieces = append(pieces, string(cur))
			cur = cur[:0]
		}
		if len(cur) > 0 {
			cur = append(cur, ' ')
		}
		cur = append(cur, wr...)
	}
	if len(cur) > 0 {
		pieces = append(pieces, string(cur))
	}
	return pieces
}

// Sentences splits a paragraph into sentences. A boundary is a terminal
// punctuation mark (. ! ? and their CJK equivalents), optionally followed by
// closing quotes or brackets, followed by whitespace or end of text. No
// attempt is made to recognise abbreviations; over-splitting is harmless
// for chunking because sentences are regrouped afterwards.
func Sentences(p string) []string {
	rs := []rune(p)
	var out []string
	start := 0
	for i := 0; i < len(rs); i++ {
		if !isTerminal(rs[i]) {
			continue
		}
		j := i + 1
		for j < len(rs) && isCloser(rs[j]) {
			j++
		}
		if j < len(rs) && !unicode.IsSpace(rs[j]) && !isCJKTerminal(rs[i]) {
			continue
		}
		s := strings.TrimSpace(string(rs[start:j]))
		if s != "" {
			out = append(out, s)
		}
		start = j
		i = j - 1
	}
	if rest := strings.TrimSpace(string(rs[start:])); rest != "" {
		out = append(out, rest)
	}
	return out
}

func isTerminal(r rune) bool {
	switch r {
	case '.', '!', '?', 0x3002, 0xff01, 0xff1f: // 。 ！ ？
		return true
	}
	return false
}

// isCJKTerminal reports full-width terminals, after which no space is expected.
func isCJKTerminal(r rune) bool {
	return r == 0x3002 || r == 0xff01 || r == 0xff1f
}

func isCloser(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '}', 0x201d, 0x2019, 0x300d, 0x300f: // ” ’ 」 』
		return true
	}
	return false
}

// mergeRunts folds chunks shorter than Min into their predecessor when the
// result stays within Max.
func mergeRunts(chunks []string, o Options) []string {
	if len(chunks) < 2 {
		return chunks
	}
	out := []string{chunks[0]}
	for _, c := range chunks[1:] {
		prev := out[len(out)-1]
		if runeLen(c) < o.Min && runeLen(prev)+2+runeLen(c) <= o.Max {
			out[len(out)-1] = prev + "\n\n" + c
			continue
		}
		out = append(out, c)
	}
	return out
}

// applyOverlap prepends the tail of the previous chunk to each chunk.
func applyOverlap(chunks []string, overlap int) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		if i == 0 {
			out[i] = c
			continue
		}
		out[i] = Tail(chunks[i-1], overlap) + "\n\n" + c
	}
	return out
}

// Tail returns at most n runes from the end of s, advanced to the next word
// boundary so no word is split. The result may therefore be shorter than n,
// and is empty when the last word alone is longer than n.
func Tail(s string, n int) string {
	rs := []rune(s)
	if n <= 0 || len(rs) == 0 {
		return ""
	}
	if len(rs) <= n {
		return s
	}
	cut := len(rs) - n
	// Advance to the next whitespace so we start on a word boundary,
	// unless we already are on one.
	if !unicode.IsSpace(rs[cut-1]) {
		for cut < len(rs) && !unicode.IsSpace(rs[cut]) {
			cut++
		}
	}
	return strings.TrimSpace(string(rs[cut:]))
}

func runeLen(s string) int { return len([]rune(s)) }
