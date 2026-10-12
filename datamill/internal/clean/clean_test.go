package clean

import (
	"strings"
	"testing"
)

func TestTextNormalizesLineEndingsAndBlankRuns(t *testing.T) {
	in := "a\r\nb\r\n\r\n\r\n\r\nc\n"
	got := Text(in, Options{})
	want := "a\nb\n\nc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTextReflowJoinsAndDehyphenates(t *testing.T) {
	in := "This is an exam-\nple of a wrapped para-\ngraph.\n\nSecond one\nhere."
	got := Text(in, Options{Reflow: true})
	want := "This is an example of a wrapped paragraph.\n\nSecond one here."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTextReflowKeepsHyphenBeforeCapital(t *testing.T) {
	in := "the well-\nKnown"
	got := Text(in, Options{Reflow: true})
	if got != "the well- Known" {
		t.Fatalf("got %q", got)
	}
}

func TestTextDropsPageNumbers(t *testing.T) {
	in := "Intro line.\n12\nPage 13\nxiv\nMore text."
	got := Text(in, Options{DropPageNumbers: true})
	if got != "Intro line.\nMore text." {
		t.Fatalf("got %q", got)
	}
	// Without the option, numbers are kept.
	if !strings.Contains(Text(in, Options{}), "12") {
		t.Fatal("page number should be kept when option is off")
	}
}

func TestTextFormFeedBecomesParagraphBreak(t *testing.T) {
	got := Text("end of page one.\fstart of page two.", Options{})
	if got != "end of page one.\n\nstart of page two." {
		t.Fatalf("got %q", got)
	}
}

func TestTextStripsControlsAndOddSpaces(t *testing.T) {
	got := Text("a"+string(rune(0xa0))+"b"+string(rune(0x200b))+"c\x01d"+string(rune(0xfeff)), Options{})
	if got != "a bcd" {
		t.Fatalf("got %q", got)
	}
}

func TestTextPreservesIndentWithoutReflow(t *testing.T) {
	got := Text("    code\n    more", Options{})
	if got != "    code\n    more" {
		t.Fatalf("got %q", got)
	}
}

func TestLooksHardWrapped(t *testing.T) {
	wrapped := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit sed do\n", 20)
	if !LooksHardWrapped(wrapped) {
		t.Error("expected wrapped text to be detected")
	}
	flowed := strings.Repeat("This is a paragraph that is quite long and goes on for a while without any line break at all until the end.\n\n", 20)
	if LooksHardWrapped(flowed) {
		t.Error("expected flowed text not to be detected")
	}
	if LooksHardWrapped("too\nshort\n") {
		t.Error("tiny inputs are never wrapped")
	}
}
