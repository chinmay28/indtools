package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinmay28/indtools/datamill/internal/store"
)

func TestIngestAndExportText(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	body := "# One\n\n" + strings.Repeat("Sentence about one. ", 20) + "\n\n# Two\n\n" + strings.Repeat("Sentence about two. ", 20) + "\n"
	if err := os.WriteFile(md, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	wrapped := filepath.Join(dir, "wrapped.txt")
	if err := os.WriteFile(wrapped, []byte(strings.Repeat("A line of fixed width prose that is hard-wrapped at column seventy\n", 12)), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.bin")
	os.WriteFile(bad, []byte{0, 1, 2}, 0o644)

	p, err := store.Open(filepath.Join(dir, "proj"))
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	cf := chunkFlags{size: 300, min: 50, max: 400, headings: true}

	// One bad file -> error, but the good ones are still ingested.
	err = ingest(context.Background(), p, []string{md, wrapped, bad}, cf, &log)
	if err == nil || !strings.Contains(err.Error(), "1 of 3 files failed") {
		t.Fatalf("err=%v log=%s", err, log.String())
	}
	srcs, _ := p.Sources()
	if len(srcs) != 2 || srcs[0].Format != "markdown" || srcs[1].Format != "text" {
		t.Fatalf("sources %+v", srcs)
	}
	chunks, _ := p.Chunks()
	if len(chunks) < 3 || !strings.HasPrefix(chunks[0].Text, "# One") {
		t.Fatalf("chunks %d: %q", len(chunks), chunks[0].Text)
	}
	// Hard-wrapped text was reflowed into one line.
	txt, _ := p.Text(srcs[1].ID)
	if strings.Count(txt, "\n") != 0 {
		t.Fatalf("expected reflowed text, got %q", txt)
	}

	// Re-ingesting the same content is skipped.
	log.Reset()
	if err := ingest(context.Background(), p, []string{md}, cf, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "already ingested") {
		t.Fatalf("log %s", log.String())
	}
	if srcs2, _ := p.Sources(); len(srcs2) != 2 {
		t.Fatalf("duplicate ingested: %d sources", len(srcs2))
	}

	// Export chunks as a pretraining text dataset (.json -> array).
	out := filepath.Join(dir, "text.json")
	if err := doExport(p, exportFlags{format: "text", out: out}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	var arr []map[string]string
	if err := json.Unmarshal(data, &arr); err != nil || len(arr) != len(chunks) {
		t.Fatalf("%v len=%d", err, len(arr))
	}

	// With no answers, a pair export is empty but valid JSONL.
	out = filepath.Join(dir, "pairs.jsonl")
	if err := doExport(p, exportFlags{format: "alpaca", out: out}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(out); len(data) != 0 {
		t.Fatalf("expected empty output, got %q", data)
	}
	if err := doExport(p, exportFlags{format: "nope", out: out}); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestIngestUnits(t *testing.T) {
	dir := t.TempDir()
	story := filepath.Join(dir, "story.txt")
	body := "Once upon a time.\n\n" + strings.Repeat("The dragon flew over the hills. ", 20) + "\n\n" + strings.Repeat("Then it slept. ", 20) + "\n"
	if err := os.WriteFile(story, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Open(filepath.Join(dir, "doc"))
	if err := ingest(context.Background(), p, []string{story}, chunkFlags{unit: "document"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	chunks, _ := p.Chunks()
	if len(chunks) != 1 || !strings.HasPrefix(chunks[0].Text, "Once upon a time.\n\nThe dragon") {
		t.Fatalf("document unit: %d chunks %q", len(chunks), chunks[0].Text)
	}
	p, _ = store.Open(filepath.Join(dir, "para"))
	if err := ingest(context.Background(), p, []string{story}, chunkFlags{unit: "paragraph", min: 10, max: 2000}, io.Discard); err != nil {
		t.Fatal(err)
	}
	chunks, _ = p.Chunks()
	if len(chunks) != 3 || chunks[0].Text != "Once upon a time." {
		t.Fatalf("paragraph unit: %d chunks %q", len(chunks), chunks[0].Text)
	}
	if err := ingest(context.Background(), p, []string{story}, chunkFlags{unit: "sentence"}, io.Discard); err == nil {
		t.Fatal("bad unit accepted")
	}
}

func TestLLMFlagsClient(t *testing.T) {
	t.Setenv("DATAMILL_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := (&llmFlags{provider: "openai"}).client(); err == nil {
		t.Fatal("openai needs -model")
	}
	c, err := (&llmFlags{provider: "anthropic"}).client()
	if err != nil || c.Model() == "" {
		t.Fatalf("anthropic default model: %v %v", c, err)
	}
	if _, err := (&llmFlags{provider: "openai", model: "m", stage: "bogus"}).runOptions(); err == nil {
		t.Fatal("bad stage accepted")
	}
	if _, err := (&llmFlags{provider: "openai", model: "m", stage: "all", task: "bogus"}).runOptions(); err == nil {
		t.Fatal("bad task accepted")
	}
	if g := (&llmFlags{task: "reply"}).generator(c); g.Task != "reply" {
		t.Fatalf("task not passed: %+v", g)
	}
	lf := &llmFlags{temperature: 0.2, lang: "de", reasoningEffort: "none"}
	g := lf.generator(c)
	if g.Temperature == nil || *g.Temperature != 0.2 || g.Language != "de" || g.ReasoningEffort != "none" {
		t.Fatalf("%+v", g)
	}
	if g := (&llmFlags{temperature: -1}).generator(c); g.Temperature != nil {
		t.Fatal("negative temperature should be omitted")
	}
}

func TestIngestRecords(t *testing.T) {
	dir := t.TempDir()
	jl := filepath.Join(dir, "sections.jsonl")
	body := `{"id":"s-1","text":"Work   is worship.\n\n\nServe   all.","volume":1,"title":"Karma-Yoga"}
{"id":"s-2","text":"   "}
{"id":"s-3","text":"Second passage."}
`
	if err := os.WriteFile(jl, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Open(filepath.Join(dir, "proj"))
	var log bytes.Buffer
	if err := ingest(context.Background(), p, []string{jl}, chunkFlags{}, &log); err == nil {
		t.Fatal("a blank text record should fail the file")
	}
	body = strings.ReplaceAll(body, `{"id":"s-2","text":"   "}`+"\n", "")
	os.WriteFile(jl, []byte(body), 0o644)
	if err := ingest(context.Background(), p, []string{jl}, chunkFlags{}, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	srcs, _ := p.Sources()
	if len(srcs) != 1 || srcs[0].Format != FormatRecords || srcs[0].Chunks != 2 {
		t.Fatalf("sources %+v", srcs)
	}
	chunks, _ := p.Chunks()
	if len(chunks) != 2 || chunks[0].ID != srcs[0].ID+"-0000" || chunks[0].Ref != "s-1" || chunks[1].Ref != "s-3" {
		t.Fatalf("chunks %+v", chunks)
	}
	if chunks[0].Text != "Work is worship.\n\nServe all." {
		t.Fatalf("text not normalised: %q", chunks[0].Text)
	}
	if chunks[0].Meta["volume"] != "1" || chunks[0].Meta["title"] != "Karma-Yoga" || chunks[1].Meta != nil {
		t.Fatalf("meta %v %v", chunks[0].Meta, chunks[1].Meta)
	}
	// Meta and ref survive a round trip through the store.
	p2, _ := store.Open(p.Dir)
	again, _ := p2.Chunks()
	if again[0].Ref != "s-1" || again[0].Meta["title"] != "Karma-Yoga" {
		t.Fatalf("round trip %+v", again[0])
	}
}
