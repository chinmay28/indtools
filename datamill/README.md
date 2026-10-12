# datamill

Turn books and documents into LLM training datasets. Feed it PDF, EPUB,
MOBI, DOCX, HTML, Markdown or plain-text files; it extracts and cleans the
text, splits it into chunks, asks an LLM to write questions and answers
about each chunk, and exports the result as Alpaca, ShareGPT or
chat-messages JSON(L). Modelled on
[easy-dataset](https://github.com/ConardLi/easy-dataset), as a single Go
binary with no web UI and no database.

## Install

```sh
cd datamill
go build -o datamill .
# or: go install .
```

Requires Go 1.24+. For PDF input, install poppler's `pdftotext`
(`apt install poppler-utils`, `brew install poppler`). Everything else is
pure Go. Calibre's `ebook-convert` is used only as a fallback for the rare
MOBI files that use HUFF/CDIC compression.

## Quick start

```sh
# Ollama running locally
datamill run -project ./mybook \
  -provider openai -base-url http://localhost:11434/v1 -model llama3.1 \
  -format alpaca -out mybook.json \
  book.epub notes.pdf chapter.md

# Anthropic
export ANTHROPIC_API_KEY=sk-ant-...
datamill run -project ./mybook -provider anthropic -format messages -out mybook.jsonl book.epub

# OpenAI or any OpenAI-compatible host (OpenRouter, vLLM, LM Studio, DeepSeek, ...)
export OPENAI_API_KEY=sk-...
datamill run -project ./mybook -provider openai -model gpt-4.1 -format sharegpt -out mybook.json book.pdf
```

`run` is just `ingest` + `generate` + `export`. Use the stages separately
when you want to inspect chunks before spending tokens, or re-run a
stage with different settings.

## Pipeline

### 1. `ingest` — extract, clean, chunk

```sh
datamill ingest -project DIR [flags] FILE...
```

- Extraction per format: `pdftotext` for PDF; pure-Go readers for EPUB
  (zip + OPF spine), MOBI/AZW (PalmDOC), DOCX, HTML, Markdown and text.
  Files with an unknown extension are accepted if they are plain text.
- Cleaning: normalises whitespace and line endings, drops control
  characters, strips page-number-only lines from PDFs, and reflows
  hard-wrapped lines (joining `exam-` / `ple` hyphenation) for PDFs and
  fixed-column text files. Markdown and HTML-derived text is never
  reflowed. `-no-reflow` turns reflow off entirely.
- Chunking: paragraphs are packed up to `-chunk-size` characters (1500
  by default) without exceeding `-chunk-max` (2000). Over-long paragraphs
  are split at sentence boundaries, then at word boundaries. Pieces under
  `-chunk-min` (300) are merged into their neighbour. Markdown headings
  and `Chapter N` lines start a new chunk (`-headings=false` to disable).
  `-chunk-overlap N` repeats the last N characters of the previous chunk.
- Each file is identified by the SHA-256 of its bytes; ingesting the same
  content twice is a no-op.

### 2. `generate` — questions, then answers

```sh
datamill generate -project DIR -provider openai|anthropic -model NAME [flags]
```

For every chunk without questions, the model is asked for N
self-contained questions answerable from the chunk (`-questions N`, or
automatic: about one per 300 characters, 1–10). Then every question
without an answer is answered using only its chunk. With `-cot` the model
is asked to reason first; the reasoning is stored separately and can be
exported inside `<think>` tags.

Results are appended to the project as they arrive, so you can interrupt
with Ctrl-C and re-run the same command to continue. Items that fail after
retries stay pending and are reported; re-running picks them up.

Useful flags: `-workers 4` (concurrent requests), `-lang en` (language of
the questions and answers), `-stage questions|answers|all`, `-max-tokens`,
`-temperature` (omitted unless set; recent Claude models reject sampling
parameters), `-timeout`, `-retries`.

API key resolution: `-api-key`, then `DATAMILL_API_KEY`, then
`OPENAI_API_KEY` or `ANTHROPIC_API_KEY` by provider. Ollama needs none.
The Anthropic provider defaults to `claude-opus-5-5` when `-model` is
omitted.

### 3. `export` — write the dataset

```sh
datamill export -project DIR -format alpaca|sharegpt|messages|text -out FILE [flags]
```

| Format     | Shape |
|------------|-------|
| `alpaca`   | `{"instruction", "input", "output"}`; the chunk goes in `input` with `-include-context` |
| `sharegpt` | `{"conversations": [{"from": "human"/"gpt"/"system", "value"}]}` |
| `messages` | `{"messages": [{"role": "user"/"assistant"/"system", "content"}]}` (OpenAI fine-tuning / chat template) |
| `text`     | `{"text": chunk}` per chunk, for continued pretraining; needs no LLM |

A `.json` output file gets one JSON array; any other extension gets JSON
Lines. `-system "..."` adds a system turn to the chat formats.
`-include-context` puts the source chunk in the example (Alpaca `input`,
or prepended to the user turn). `-include-reasoning` emits the `-cot`
reasoning as `<think>...</think>` before the answer.

### Inspecting

```sh
datamill extract book.pdf            # print the cleaned text (add -raw for pre-cleaning text)
datamill status -project DIR         # counts of sources, chunks, pending questions/answers
```

## Project directory

```
DIR/
  sources.jsonl    one record per input file (id, format, chars, chunks, sha256)
  text/<id>.txt    cleaned text per source
  chunks.jsonl     {id, source_id, index, text}
  questions.jsonl  {id, chunk_id, text}
  answers.jsonl    {question_id, text, reasoning, model}
```

All files are append-only JSON Lines, readable with any tool. IDs are
deterministic (`<sha-prefix>-<chunk index>-q<n>`), which is what makes
resuming and de-duplication work. Delete `questions.jsonl` and
`answers.jsonl` to regenerate from scratch with a different model.

## Testing

```sh
cd datamill
go test ./...
```

Every package has unit tests with no network access: the extractors are
tested against EPUB/DOCX/MOBI files built in the test (including a
PalmDOC round-trip), the LLM clients against `httptest` servers, and the
generation pipeline against a fake client, including failure and resume
paths.
