// datamill turns documents (PDF, EPUB, MOBI, DOCX, HTML, Markdown, plain
// text) into LLM fine-tuning datasets, in the spirit of easy-dataset.
//
// Pipeline (each stage is a subcommand and is resumable):
//
//	ingest    extract text, clean it, split into chunks  -> project dir
//	generate  ask an LLM for questions per chunk, then answers
//	export    write Alpaca / ShareGPT / messages / text JSON(L)
//	run       ingest + generate + export in one go
//
// State lives in a project directory as append-only JSONL files, so an
// interrupted generate can be re-run and continues where it stopped.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chinmay28/indtools/datamill/internal/chunk"
	"github.com/chinmay28/indtools/datamill/internal/clean"
	"github.com/chinmay28/indtools/datamill/internal/export"
	"github.com/chinmay28/indtools/datamill/internal/extract"
	"github.com/chinmay28/indtools/datamill/internal/gen"
	"github.com/chinmay28/indtools/datamill/internal/llm"
	"github.com/chinmay28/indtools/datamill/internal/records"
	"github.com/chinmay28/indtools/datamill/internal/store"
)

// FormatRecords marks a source ingested from JSON Lines records, each of
// which became one chunk unchanged.
const FormatRecords = "jsonl"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "ingest":
		err = runIngest(ctx, os.Args[2:])
	case "generate":
		err = runGenerate(ctx, os.Args[2:])
	case "export":
		err = runExport(os.Args[2:])
	case "run":
		err = runAll(ctx, os.Args[2:])
	case "extract":
		err = runExtract(ctx, os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted; re-run the same command to resume")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `datamill - build LLM training datasets from documents

Commands:
  ingest    Extract, clean and chunk input files into a project directory
  generate  Generate questions and answers for the project's chunks with an LLM
  export    Write the project's Q/A pairs (or chunks) as a dataset file
  run       ingest + generate + export in one command
  extract   Print the cleaned text of one file (for inspection)
  status    Show what is done and what is pending in a project

Usage:
  datamill ingest   -project DIR [chunk flags] FILE...
  datamill generate -project DIR -provider openai|anthropic -model NAME [llm flags]
  datamill export   -project DIR -format alpaca|sharegpt|messages|text -out FILE [export flags]
  datamill run      -project DIR -format F -out FILE [all flags] FILE...
  datamill extract  [-pdftotext PATH] FILE
  datamill status   -project DIR

Run "datamill <command> -h" for the flags of each command.

Inputs: .pdf (needs pdftotext from poppler), .epub, .mobi/.azw/.prc, .docx,
.html, .md, .txt and any other UTF-8 text file. A .jsonl file is read as
pre-made passages: one {"id", "text", ...} object per line becomes one
chunk, keeping "id" as its ref and the other fields as metadata.

Environment: the API key is read from -api-key, else DATAMILL_API_KEY, else
OPENAI_API_KEY / ANTHROPIC_API_KEY depending on the provider.
`)
}

// ---- shared flag groups ----

type chunkFlags struct {
	size, min, max, overlap int
	unit                    string
	headings                bool
	noReflow                bool
	pdftotext, ebookConvert string
}

func (c *chunkFlags) bind(fs *flag.FlagSet) {
	fs.IntVar(&c.size, "chunk-size", chunk.DefaultTarget, "target chunk size in characters")
	fs.IntVar(&c.min, "chunk-min", chunk.DefaultMin, "minimum chunk size; smaller trailing pieces are merged")
	fs.IntVar(&c.max, "chunk-max", chunk.DefaultMax, "hard maximum chunk size")
	fs.IntVar(&c.overlap, "chunk-overlap", 0, "characters of the previous chunk to repeat at the start of each chunk")
	fs.StringVar(&c.unit, "unit", string(chunk.UnitChunk), "what one chunk is: chunk (paragraphs packed to -chunk-size), paragraph (one per paragraph) or document (the whole file)")
	fs.BoolVar(&c.headings, "headings", true, "treat Markdown headings and 'Chapter N' lines as chunk boundaries")
	fs.BoolVar(&c.noReflow, "no-reflow", false, "never join hard-wrapped lines (default: reflow PDFs and fixed-width text)")
	fs.StringVar(&c.pdftotext, "pdftotext", "", "path to the pdftotext binary (default: look up on PATH)")
	fs.StringVar(&c.ebookConvert, "ebook-convert", "", "path to calibre's ebook-convert, used for HUFF/CDIC MOBI files")
}

func (c *chunkFlags) options() chunk.Options {
	return chunk.Options{Target: c.size, Min: c.min, Max: c.max, Overlap: c.overlap, Headings: c.headings, Unit: chunk.Unit(c.unit)}
}

func (c *chunkFlags) validate() error {
	if !chunk.ValidUnit(chunk.Unit(c.unit)) {
		return fmt.Errorf("-unit must be chunk, paragraph or document, got %q", c.unit)
	}
	return nil
}

func (c *chunkFlags) extractOptions() extract.Options {
	return extract.Options{PDFToText: c.pdftotext, EbookConvert: c.ebookConvert}
}

type llmFlags struct {
	provider, model, baseURL, apiKey string
	questions, workers, maxTokens    int
	temperature                      float64
	cot                              bool
	reasoningEffort                  string
	lang, stage                      string
	timeout                          time.Duration
	retries                          int
}

func (l *llmFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&l.provider, "provider", "openai", "LLM provider: openai (any OpenAI-compatible server, incl. Ollama) or anthropic")
	fs.StringVar(&l.model, "model", "", "model name (required for openai; defaults to "+llm.DefaultAnthropicModel+" for anthropic)")
	fs.StringVar(&l.baseURL, "base-url", "", "API base URL (e.g. http://localhost:11434/v1 for Ollama)")
	fs.StringVar(&l.apiKey, "api-key", "", "API key (default: DATAMILL_API_KEY, then the provider's usual env var)")
	fs.IntVar(&l.questions, "questions", 0, "questions per chunk (0 = automatic, about one per 300 characters)")
	fs.IntVar(&l.workers, "workers", 4, "concurrent LLM requests")
	fs.IntVar(&l.maxTokens, "max-tokens", 4096, "max completion tokens per request")
	fs.Float64Var(&l.temperature, "temperature", -1, "sampling temperature; negative leaves the provider default")
	fs.BoolVar(&l.cot, "cot", false, "ask for chain-of-thought reasoning with each answer")
	fs.StringVar(&l.reasoningEffort, "reasoning-effort", "", "cap the model's built-in thinking: none, low, medium or high (OpenAI-compatible servers only; \"none\" disables Qwen3 thinking on Ollama)")
	fs.StringVar(&l.lang, "lang", "en", "language for questions and answers")
	fs.StringVar(&l.stage, "stage", "all", "which stage to run: questions, answers or all")
	fs.DurationVar(&l.timeout, "timeout", 5*time.Minute, "per-request timeout")
	fs.IntVar(&l.retries, "retries", 3, "retries per request on transient failures")
}

func (l *llmFlags) client() (llm.Client, error) {
	provider := strings.ToLower(l.provider)
	model := l.model
	if model == "" && (provider == "anthropic" || provider == "claude") {
		model = llm.DefaultAnthropicModel
	}
	if model == "" {
		return nil, errors.New("-model is required")
	}
	key := l.apiKey
	if key == "" {
		key = os.Getenv("DATAMILL_API_KEY")
	}
	if key == "" {
		switch provider {
		case "anthropic", "claude":
			key = os.Getenv("ANTHROPIC_API_KEY")
		default:
			key = os.Getenv("OPENAI_API_KEY")
		}
	}
	return llm.New(llm.Config{Provider: provider, BaseURL: l.baseURL, APIKey: key, Model: model, Timeout: l.timeout, Retries: l.retries})
}

func (l *llmFlags) generator(c llm.Client) *gen.Generator {
	g := &gen.Generator{Client: c, Language: l.lang, MaxTokens: l.maxTokens, ReasoningEffort: l.reasoningEffort}
	if l.temperature >= 0 {
		t := l.temperature
		g.Temperature = &t
	}
	return g
}

func (l *llmFlags) runOptions() (gen.RunOptions, error) {
	stage := gen.Stage(l.stage)
	switch stage {
	case gen.StageQuestions, gen.StageAnswers, gen.StageAll:
	default:
		return gen.RunOptions{}, fmt.Errorf("-stage must be questions, answers or all, got %q", l.stage)
	}
	return gen.RunOptions{Stage: stage, Workers: l.workers, QuestionsPerChunk: l.questions, CoT: l.cot, Log: os.Stderr}, nil
}

type exportFlags struct {
	format, out, system       string
	context, reasoning, jsonl bool
}

func (e *exportFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&e.format, "format", export.Alpaca, "output format: "+strings.Join(export.Formats, ", "))
	fs.StringVar(&e.out, "out", "", "output file; a .json extension writes one JSON array, anything else JSON Lines")
	fs.StringVar(&e.system, "system", "", "system prompt to include in chat formats")
	fs.BoolVar(&e.context, "include-context", false, "include the source chunk in each example")
	fs.BoolVar(&e.reasoning, "include-reasoning", false, "include chain-of-thought (from -cot) inside <think> tags before the answer")
}

// ---- commands ----

func runIngest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	project := fs.String("project", "", "project directory (created if missing)")
	var cf chunkFlags
	cf.bind(fs)
	fs.Parse(args)
	if *project == "" || fs.NArg() == 0 {
		return errors.New("usage: datamill ingest -project DIR [flags] FILE...")
	}
	p, err := store.Open(*project)
	if err != nil {
		return err
	}
	return ingest(ctx, p, fs.Args(), cf, os.Stderr)
}

// ingest extracts, cleans and chunks each file into the project. Files
// whose content is already in the project are skipped.
func ingest(ctx context.Context, p *store.Project, files []string, cf chunkFlags, log io.Writer) error {
	if err := cf.validate(); err != nil {
		return err
	}
	existing, err := p.Sources()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(existing))
	for _, s := range existing {
		seen[s.ID] = true
	}
	var failed int
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ingestOne(ctx, p, f, cf, seen, log); err != nil {
			failed++
			fmt.Fprintf(log, "%s: %v\n", f, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files failed", failed, len(files))
	}
	return nil
}

func ingestOne(ctx context.Context, p *store.Project, path string, cf chunkFlags, seen map[string]bool, log io.Writer) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	id, sha := store.SourceID(raw)
	if seen[id] {
		fmt.Fprintf(log, "%s: already ingested as %s, skipping\n", path, id)
		return nil
	}
	if strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return ingestRecords(p, path, raw, id, sha, seen, log)
	}
	doc, err := extract.File(ctx, path, cf.extractOptions())
	if err != nil {
		return err
	}
	text := clean.Text(doc.Text, clean.Options{
		Reflow:          doc.Wrapped && !cf.noReflow,
		DropPageNumbers: doc.Format == extract.FormatPDF,
	})
	chunks := chunk.Split(text, cf.options())
	if len(chunks) == 0 {
		return errors.New("no text after cleaning")
	}
	src := store.Source{
		ID: id, Name: filepath.Base(path), Path: path, Title: doc.Title, Format: doc.Format,
		Chars: len([]rune(text)), Chunks: len(chunks), SHA256: sha,
	}
	if err := p.AppendSource(src, text); err != nil {
		return err
	}
	recs := make([]store.Chunk, len(chunks))
	for i, c := range chunks {
		recs[i] = store.Chunk{ID: store.ChunkID(id, i), SourceID: id, Index: i, Text: c}
	}
	if err := p.AppendChunks(recs); err != nil {
		return err
	}
	seen[id] = true
	fmt.Fprintf(log, "%s: %s, %d chars, %d chunks (id %s)\n", path, doc.Format, src.Chars, len(chunks), id)
	return nil
}

// ingestRecords turns each JSONL record into one chunk, keeping the
// record's id as Chunk.Ref and its other fields as Chunk.Meta. Text is
// whitespace-normalised but never reflowed or re-chunked.
func ingestRecords(p *store.Project, path string, raw []byte, id, sha string, seen map[string]bool, log io.Writer) error {
	recs, err := records.Parse(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return errors.New("no records")
	}
	chunks := make([]store.Chunk, 0, len(recs))
	texts := make([]string, 0, len(recs))
	for _, r := range recs {
		text := clean.Text(r.Text, clean.Options{})
		if text == "" {
			continue
		}
		chunks = append(chunks, store.Chunk{ID: store.ChunkID(id, len(chunks)), SourceID: id, Index: len(chunks), Text: text, Ref: r.Ref, Meta: r.Meta})
		texts = append(texts, text)
	}
	if len(chunks) == 0 {
		return errors.New("no text after cleaning")
	}
	full := strings.Join(texts, "\n\n")
	src := store.Source{
		ID: id, Name: filepath.Base(path), Path: path, Format: FormatRecords,
		Chars: len([]rune(full)), Chunks: len(chunks), SHA256: sha,
	}
	if err := p.AppendSource(src, full); err != nil {
		return err
	}
	if err := p.AppendChunks(chunks); err != nil {
		return err
	}
	seen[id] = true
	fmt.Fprintf(log, "%s: %s, %d chars, %d records (id %s)\n", path, FormatRecords, src.Chars, len(chunks), id)
	return nil
}

func runGenerate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	project := fs.String("project", "", "project directory")
	var lf llmFlags
	lf.bind(fs)
	fs.Parse(args)
	if *project == "" {
		return errors.New("usage: datamill generate -project DIR -provider P -model M [flags]")
	}
	p, err := store.Open(*project)
	if err != nil {
		return err
	}
	return generate(ctx, p, lf)
}

func generate(ctx context.Context, p *store.Project, lf llmFlags) error {
	c, err := lf.client()
	if err != nil {
		return err
	}
	opts, err := lf.runOptions()
	if err != nil {
		return err
	}
	st, err := gen.Run(ctx, p, lf.generator(c), opts)
	fmt.Fprintf(os.Stderr, "generate: %d chunks -> %d questions, %d answers, %d failed\n",
		st.ChunksProcessed, st.QuestionsGenerated, st.AnswersGenerated, st.Failed)
	if err != nil {
		return err
	}
	if st.Failed > 0 {
		return fmt.Errorf("%d items failed; re-run to retry them", st.Failed)
	}
	return nil
}

func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	project := fs.String("project", "", "project directory")
	var ef exportFlags
	ef.bind(fs)
	fs.Parse(args)
	if *project == "" || ef.out == "" {
		return errors.New("usage: datamill export -project DIR -format F -out FILE [flags]")
	}
	p, err := store.Open(*project)
	if err != nil {
		return err
	}
	return doExport(p, ef)
}

func doExport(p *store.Project, ef exportFlags) error {
	if !export.ValidFormat(ef.format) {
		return fmt.Errorf("-format must be one of %s, got %q", strings.Join(export.Formats, ", "), ef.format)
	}
	jsonArray := strings.EqualFold(filepath.Ext(ef.out), ".json")
	f, err := os.Create(ef.out)
	if err != nil {
		return err
	}
	defer f.Close()

	var n int
	if ef.format == export.Text {
		chunks, err := p.Chunks()
		if err != nil {
			return err
		}
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		n = len(texts)
		err = export.WriteText(f, texts, jsonArray)
		if err != nil {
			return err
		}
	} else {
		pairs, err := p.Pairs()
		if err != nil {
			return err
		}
		recs := make([]export.Pair, len(pairs))
		for i, pr := range pairs {
			recs[i] = export.Pair{Question: pr.Question.Text, Answer: pr.Answer.Text, Reasoning: pr.Answer.Reasoning, Context: pr.Chunk.Text}
		}
		n = len(recs)
		if err := export.WritePairs(f, recs, export.Options{
			Format: ef.format, JSONArray: jsonArray, IncludeContext: ef.context,
			IncludeReasoning: ef.reasoning, System: ef.system,
		}); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "export: wrote %d records to %s (%s)\n", n, ef.out, ef.format)
	return nil
}

func runAll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	project := fs.String("project", "", "project directory (created if missing)")
	var cf chunkFlags
	var lf llmFlags
	var ef exportFlags
	cf.bind(fs)
	lf.bind(fs)
	ef.bind(fs)
	fs.Parse(args)
	if *project == "" || ef.out == "" || fs.NArg() == 0 {
		return errors.New("usage: datamill run -project DIR -provider P -model M -format F -out FILE [flags] FILE...")
	}
	p, err := store.Open(*project)
	if err != nil {
		return err
	}
	if err := ingest(ctx, p, fs.Args(), cf, os.Stderr); err != nil {
		return err
	}
	if ef.format != export.Text {
		if err := generate(ctx, p, lf); err != nil {
			return err
		}
	}
	return doExport(p, ef)
}

func runExtract(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	var cf chunkFlags
	cf.bind(fs)
	raw := fs.Bool("raw", false, "print the extracted text before cleaning")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: datamill extract [flags] FILE")
	}
	doc, err := extract.File(ctx, fs.Arg(0), cf.extractOptions())
	if err != nil {
		return err
	}
	text := doc.Text
	if !*raw {
		text = clean.Text(doc.Text, clean.Options{Reflow: doc.Wrapped && !cf.noReflow, DropPageNumbers: doc.Format == extract.FormatPDF})
	}
	fmt.Fprintf(os.Stderr, "title: %s\nformat: %s\nwrapped: %v\nchars: %d\n", doc.Title, doc.Format, doc.Wrapped, len([]rune(text)))
	_, err = io.WriteString(os.Stdout, text+"\n")
	return err
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	project := fs.String("project", "", "project directory")
	fs.Parse(args)
	if *project == "" {
		return errors.New("usage: datamill status -project DIR")
	}
	p, err := store.Open(*project)
	if err != nil {
		return err
	}
	srcs, err := p.Sources()
	if err != nil {
		return err
	}
	for _, s := range srcs {
		fmt.Printf("source %s  %-8s %7d chars %5d chunks  %s\n", s.ID, s.Format, s.Chars, s.Chunks, s.Name)
	}
	st, err := p.Status()
	if err != nil {
		return err
	}
	fmt.Printf("sources: %d\nchunks: %d (%d without questions)\nquestions: %d (%d without answers)\nanswers: %d\n",
		st.Sources, st.Chunks, st.ChunksWithoutQuestions, st.Questions, st.QuestionsWithoutAnswers, st.Answers)
	return nil
}
