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

### Reverse prompting: the text is the answer

Sometimes the document already *is* the assistant turn and what is
missing is the user turn. Three cases are built in:

```sh
# A folder of short stories: each file is one chunk; the model writes the
# request a user would give to get that story. The story is the answer.
datamill ingest -project ./stories -unit document stories/*.md
datamill generate -project ./stories -provider openai -base-url http://localhost:11434/v1 \
  -model qwen3:14b -reasoning-effort none -task prompt
datamill export -project ./stories -format messages -answer-source chunk -out stories.jsonl

# Letters: the model writes the letter that was being replied to.
datamill ingest -project ./letters letters.jsonl       # {"id","text","recipient","date"} per line
datamill generate -project ./letters ... -task reply
datamill export -project ./letters -format pairs -answer-source chunk -out letters.jsonl

# Question-for-passage: one question per paragraph, the paragraph is the
# answer. Doubles as a retrieval evaluation set (each question has its
# gold passage id in "ref").
datamill ingest -project ./passages -unit paragraph book.epub   # or paragraphs.jsonl
datamill generate -project ./passages ... -task question -questions 1
datamill export -project ./passages -format pairs -answer-source chunk -out eval.jsonl
```

`-task prompt` and `-task reply` have no answer stage: `-stage all` runs
only the question stage and `-stage answers` is an error.

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
- `-unit` changes what a chunk is: `chunk` (the packing above, default),
  `paragraph` (one chunk per paragraph, so every chunk is a citable
  passage; headings and one-liners shorter than `-chunk-min` are attached
  to the paragraph after them; paragraphs over `-chunk-max` are split) or
  `document` (the whole file is one chunk, whatever its size, for inputs
  that are complete works such as short stories).
- A `.jsonl` input is treated as passages split by another tool: each
  line is an object with a required `"text"`, an optional `"id"` (kept
  as the chunk's `ref`) and any other string, number or boolean fields,
  or a `"meta"` object, kept as the chunk's `meta` (shown to the model as
  context and copied into the `pairs` export). The text is whitespace
  normalised but never re-chunked.
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

`-task` picks what the first stage writes for each chunk:

| Task       | The chunk is…                 | The model writes…                                   |
|------------|-------------------------------|-----------------------------------------------------|
| `question` | a passage (default)           | questions the passage answers                       |
| `prompt`   | a finished story, essay, poem | the request a user would give to get that text      |
| `reply`    | a letter or an answer         | the message the correspondent sent to receive it    |

For `prompt` and `reply` the chunk is the answer, so there is no answer
stage (automatic count: one per chunk). Chunk metadata from JSONL input
(a letter's recipient and date, a story's title) is shown to the model
as context.

Results are appended to the project as they arrive, so you can interrupt
with Ctrl-C and re-run the same command to continue. Items that fail after
retries stay pending and are reported; re-running picks them up.

Useful flags: `-workers 4` (concurrent requests), `-lang en` (language of
the questions and answers), `-stage questions|answers|all`, `-max-tokens`,
`-temperature` (omitted unless set; recent Claude models reject sampling
parameters), `-reasoning-effort none|low|medium|high` (OpenAI-compatible
servers only; on Ollama `none` switches off Qwen3 thinking, which is
several times faster for this kind of extractive work), `-timeout`,
`-retries`.

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
| `pairs`    | `{"id", "chunk_id", "ref", "meta", "question", "answer", "reasoning", "context"}`: the raw pair with its identifiers, for evaluation sets and custom record builders |
| `text`     | `{"text": chunk}` per chunk, for continued pretraining; needs no LLM |

A `.json` output file gets one JSON array; any other extension gets JSON
Lines. `-system "..."` adds a system turn to the chat formats.
`-include-context` puts the source chunk in the example (Alpaca `input`,
or prepended to the user turn). `-include-reasoning` emits the `-cot`
reasoning as `<think>...</think>` before the answer.
`-answer-source chunk` makes the chunk's own text the answer and exports
every question, answered or not (the default, `model`, exports generated
answers and skips unanswered questions).

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
  chunks.jsonl     {id, source_id, index, text, ref?, meta?}
  questions.jsonl  {id, chunk_id, text}   (the user turn, whatever -task)
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
