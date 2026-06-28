package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"0", 0, false},
		{"1024", 1024, false},
		{"1K", 1024, false},
		{"10M", 10 << 20, false},
		{"2G", 2 << 30, false},
		{"1T", 1 << 40, false},
		{"10MB", 10 << 20, false},
		{"10MiB", 10 << 20, false},
		{" 4k ", 4 << 10, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-1", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseSize(%q) err=%v want err=%v", c.in, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("parseSize(%q)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestPartName(t *testing.T) {
	if got, want := partName("data.bin", 7, 6), "data.bin.part000007"; got != want {
		t.Errorf("partName=%q want %q", got, want)
	}
	if got, want := partName("data.bin", 12345, 4), "data.bin.part12345"; got != want {
		t.Errorf("partName overflow=%q want %q", got, want)
	}
}

func TestDigitsFor(t *testing.T) {
	cases := []struct {
		fileSize, chunkSize int64
		want                int
	}{
		{0, 1024, 6},
		{1, 1024, 6},
		{1024 * 1024, 1024, 6},                       // 1024 parts, but floor is 6
		{int64(1024) * 1024 * 1024 * 1024, 1024, 10}, // 1G of 1K parts -> 10 digits
	}
	for _, c := range cases {
		if got := digitsFor(c.fileSize, c.chunkSize); got != c.want {
			t.Errorf("digitsFor(%d,%d)=%d want %d", c.fileSize, c.chunkSize, got, c.want)
		}
	}
}

func TestMergeSortsNumerically(t *testing.T) {
	// Mixed-width chunks must still be ordered by numeric index, not lex order.
	dir := t.TempDir()
	files := map[string]string{
		"x.bin.part0":   "A",
		"x.bin.part1":   "B",
		"x.bin.part10":  "K",
		"x.bin.part2":   "C",
		"x.bin.part100": "Z",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	parts, err := findParts(dir, "x.bin")
	if err != nil {
		t.Fatal(err)
	}
	gotOrder := make([]string, len(parts))
	for i, p := range parts {
		gotOrder[i] = filepath.Base(p)
	}
	want := []string{"x.bin.part0", "x.bin.part1", "x.bin.part2", "x.bin.part10", "x.bin.part100"}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Fatalf("part order=%v want %v", gotOrder, want)
		}
	}
}

func TestSplitMergeRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024, 1025, 10_000} {
		t.Run("", func(t *testing.T) {
			dir := t.TempDir()
			payload := make([]byte, size)
			if _, err := rand.Read(payload); err != nil {
				t.Fatal(err)
			}

			input := filepath.Join(dir, "input.bin")
			if err := os.WriteFile(input, payload, 0o644); err != nil {
				t.Fatal(err)
			}

			chunks := filepath.Join(dir, "chunks")
			if err := runSplit([]string{"-input", input, "-size", "256", "-output", chunks}); err != nil {
				t.Fatalf("split: %v", err)
			}

			out := filepath.Join(dir, "out.bin")
			if err := runMerge([]string{"-input", chunks, "-output", out}); err != nil {
				t.Fatalf("merge: %v", err)
			}

			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("round-trip mismatch for size=%d: got %d bytes", size, len(got))
			}
		})
	}
}

func TestMergeAmbiguousFamilies(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.bin.part000000", "b.bin.part000000"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "out.bin")
	err := runMerge([]string{"-input", dir, "-output", out})
	if err == nil {
		t.Fatal("expected error for ambiguous chunk families")
	}
}
