package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndStatus(t *testing.T) {
	p, err := Open(filepath.Join(t.TempDir(), "proj"))
	if err != nil {
		t.Fatal(err)
	}
	id, sha := SourceID([]byte("content"))
	if len(id) != 12 || len(sha) != 64 || sha[:12] != id {
		t.Fatalf("bad ids %q %q", id, sha)
	}
	if err := p.AppendSource(Source{ID: id, Name: "a.txt", Format: "text", Chars: 7, SHA256: sha}, "the text"); err != nil {
		t.Fatal(err)
	}
	if txt, err := p.Text(id); err != nil || txt != "the text" {
		t.Fatalf("text %q %v", txt, err)
	}
	c0, c1 := ChunkID(id, 0), ChunkID(id, 1)
	if err := p.AppendChunks([]Chunk{{ID: c0, SourceID: id, Index: 0, Text: "A"}, {ID: c1, SourceID: id, Index: 1, Text: "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.AppendQuestions([]Question{{ID: QuestionID(c0, 1), ChunkID: c0, Text: "q1"}, {ID: QuestionID(c0, 2), ChunkID: c0, Text: "q2"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.AppendAnswers([]Answer{{QuestionID: QuestionID(c0, 2), Text: "a2"}}); err != nil {
		t.Fatal(err)
	}

	st, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	want := Status{Sources: 1, Chunks: 2, Questions: 2, Answers: 1, ChunksWithoutQuestions: 1, QuestionsWithoutAnswers: 1}
	if st != want {
		t.Fatalf("status %+v want %+v", st, want)
	}

	pairs, err := p.Pairs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Question.Text != "q2" || pairs[0].Answer.Text != "a2" || pairs[0].Chunk.Text != "A" {
		t.Fatalf("pairs %+v", pairs)
	}

	// Reopen and verify persistence; appending again accumulates.
	p2, _ := Open(p.Dir)
	if err := p2.AppendAnswers([]Answer{{QuestionID: QuestionID(c0, 1), Text: "a1"}}); err != nil {
		t.Fatal(err)
	}
	pairs, _ = p2.Pairs()
	if len(pairs) != 2 || pairs[0].Question.ID != QuestionID(c0, 1) {
		t.Fatalf("pairs after reopen %+v", pairs)
	}
}

func TestMissingFilesAreEmpty(t *testing.T) {
	p, _ := Open(t.TempDir())
	for _, f := range []func() (int, error){
		func() (int, error) { s, err := p.Sources(); return len(s), err },
		func() (int, error) { s, err := p.Chunks(); return len(s), err },
		func() (int, error) { s, err := p.Questions(); return len(s), err },
		func() (int, error) { s, err := p.Answers(); return len(s), err },
	} {
		if n, err := f(); err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
}

func TestCorruptLineIsAnError(t *testing.T) {
	p, _ := Open(t.TempDir())
	os.WriteFile(filepath.Join(p.Dir, "chunks.jsonl"), []byte("{\"id\":\"x\"}\nnot json\n"), 0o644)
	if _, err := p.Chunks(); err == nil {
		t.Fatal("expected error")
	}
}
