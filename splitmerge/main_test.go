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
	got := partName("data.bin", 7)
	want := "data.bin.part000007"
	if got != want {
		t.Errorf("partName=%q want %q", got, want)
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
