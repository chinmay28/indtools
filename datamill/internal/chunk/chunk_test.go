package chunk

import (
	"strings"
	"testing"
	"unicode"
)

func para(n int, word string) string {
	// Build a paragraph of roughly n runes made of sentences.
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(word + " " + word + " " + word + ". ")
	}
	return strings.TrimSpace(b.String())
}

// stripWS returns s without any whitespace, for content-preservation checks.
func stripWS(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func checkInvariants(t *testing.T, in string, chunks []string, o Options) {
	t.Helper()
	o = o.withDefaults()
	var all strings.Builder
	for i, c := range chunks {
		if strings.TrimSpace(c) == "" {
			t.Fatalf("chunk %d is empty", i)
		}
		if n := runeLen(c); n > o.Max+o.Overlap+2 {
			t.Fatalf("chunk %d has %d runes, max %d", i, n, o.Max)
		}
		if o.Overlap == 0 {
			all.WriteString(c)
		}
	}
	if o.Overlap == 0 && stripWS(all.String()) != stripWS(in) {
		t.Fatalf("content not preserved\n got: %q\nwant: %q", stripWS(all.String()), stripWS(in))
	}
}

func TestSplitEmpty(t *testing.T) {
	if got := Split("", Options{}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := Split("\n\n  \n", Options{}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestSplitShortTextIsOneChunk(t *testing.T) {
	in := "Hello world.\n\nSecond paragraph."
	got := Split(in, Options{})
	if len(got) != 1 || got[0] != in {
		t.Fatalf("got %q", got)
	}
}

func TestSplitAccumulatesParagraphsToTarget(t *testing.T) {
	p := para(400, "alpha")
	in := strings.Join([]string{p, p, p, p, p}, "\n\n") // ~2000 runes
	o := Options{Target: 900, Min: 200, Max: 1000}
	got := Split(in, o)
	checkInvariants(t, in, got, o)
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks (2+2+1 paragraphs), got %d: %v", len(got), lens(got))
	}
	for _, c := range got[:2] {
		if strings.Count(c, "\n\n") != 1 {
			t.Fatalf("expected two paragraphs per chunk, got %q", c)
		}
	}
}

func TestSplitLongParagraphBySentence(t *testing.T) {
	in := para(5000, "beta")
	o := Options{Target: 500, Min: 100, Max: 600}
	got := Split(in, o)
	checkInvariants(t, in, got, o)
	if len(got) < 8 {
		t.Fatalf("expected many chunks, got %d", len(got))
	}
	for i, c := range got {
		if !strings.HasSuffix(c, ".") {
			t.Fatalf("chunk %d should end at a sentence boundary: %q", i, c)
		}
	}
}

func TestSplitGiantWordIsCut(t *testing.T) {
	in := strings.Repeat("x", 2500)
	o := Options{Target: 500, Min: 100, Max: 600}
	got := Split(in, o)
	checkInvariants(t, in, got, o)
	if len(got) != 5 {
		t.Fatalf("expected 5 pieces, got %d", len(got))
	}
}

func TestSplitUnicodeCountsRunes(t *testing.T) {
	in := strings.Repeat("日本語のテキスト。", 100) // 900 runes, 2700 bytes
	o := Options{Target: 450, Min: 100, Max: 500}
	got := Split(in, o)
	checkInvariants(t, in, got, o)
	// Counting bytes instead of runes would yield 6+ chunks.
	if len(got) < 2 || len(got) > 3 {
		t.Fatalf("expected 2-3 chunks, got %d: %v", len(got), lens(got))
	}
}

func TestSplitMergesTrailingRunt(t *testing.T) {
	in := para(900, "gamma") + "\n\nTiny."
	o := Options{Target: 900, Min: 200, Max: 1200}
	got := Split(in, o)
	checkInvariants(t, in, got, o)
	if len(got) != 1 {
		t.Fatalf("runt should be merged, got %d chunks: %v", len(got), lens(got))
	}
}

func TestSplitHeadingsAreBoundaries(t *testing.T) {
	in := "# One\n\nFirst section text that is long enough to matter here.\n\n# Two\n\nSecond section text."
	got := Split(in, Options{Headings: true, Min: 10, Target: 1000, Max: 1000})
	if len(got) != 2 || !strings.HasPrefix(got[1], "# Two") {
		t.Fatalf("got %q", got)
	}
	// Without Headings the whole thing fits in one chunk.
	if got := Split(in, Options{Min: 10, Target: 1000, Max: 1000}); len(got) != 1 {
		t.Fatalf("got %d chunks without headings", len(got))
	}
	// A "Chapter N" line is also a boundary.
	in2 := "Chapter 1\n\nText of chapter one that is reasonably long for the test.\n\nCHAPTER II\n\nText of chapter two."
	if got := Split(in2, Options{Headings: true, Min: 10, Target: 1000, Max: 1000}); len(got) != 2 {
		t.Fatalf("chapter boundaries: got %q", got)
	}
}

func TestSplitOverlap(t *testing.T) {
	p := para(400, "delta")
	in := strings.Join([]string{p, p, p}, "\n\n")
	o := Options{Target: 400, Min: 100, Max: 500, Overlap: 60}
	got := Split(in, o)
	checkInvariants(t, in, got, o)
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(got))
	}
	if !strings.HasPrefix(got[1], Tail(p, 60)+"\n\n") {
		t.Fatalf("chunk 1 should start with the tail of chunk 0: %q", got[1][:80])
	}
	if strings.HasPrefix(got[0], Tail(p, 60)+"\n\n") {
		t.Fatal("first chunk must not have overlap")
	}
}

func TestSentences(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"One. Two! Three?", []string{"One.", "Two!", "Three?"}},
		{`He said "stop." Then left.`, []string{`He said "stop."`, "Then left."}},
		{"No terminal punctuation", []string{"No terminal punctuation"}},
		{"v1.2 is out. Yes.", []string{"v1.2 is out.", "Yes."}},
		{"これは。それも！", []string{"これは。", "それも！"}},
		{"", nil},
	}
	for _, c := range cases {
		got := Sentences(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("Sentences(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTail(t *testing.T) {
	s := "the quick brown fox jumps"
	if got := Tail(s, 9); got != "fox jumps" {
		t.Fatalf("got %q", got)
	}
	if got := Tail(s, 11); got != "fox jumps" { // "n fox jumps" -> advance to boundary
		t.Fatalf("got %q", got)
	}
	if got := Tail(s, 100); got != s {
		t.Fatalf("got %q", got)
	}
	if got := Tail("supercalifragilistic", 5); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := Tail(s, 0); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.Target != DefaultTarget || o.Min != DefaultMin || o.Max != DefaultMax {
		t.Fatalf("unexpected defaults %+v", o)
	}
	o = Options{Target: 500, Max: 100, Min: 900, Overlap: 400}.withDefaults()
	if o.Max != 500 || o.Min != 500 || o.Overlap != 250 {
		t.Fatalf("inconsistent options not repaired: %+v", o)
	}
}

func lens(ss []string) []int {
	out := make([]int, len(ss))
	for i, s := range ss {
		out[i] = runeLen(s)
	}
	return out
}
