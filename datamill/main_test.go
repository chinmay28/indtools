package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	lf := &llmFlags{temperature: 0.2, lang: "de", reasoningEffort: "none"}
	g := lf.generator(c)
	if g.Temperature == nil || *g.Temperature != 0.2 || g.Language != "de" || g.ReasoningEffort != "none" {
		t.Fatalf("%+v", g)
	}
	if g := (&llmFlags{temperature: -1}).generator(c); g.Temperature != nil {
		t.Fatal("negative temperature should be omitted")
	}
}
