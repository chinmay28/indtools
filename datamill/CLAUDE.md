# CLAUDE.md — datamill

Agent-facing notes. Read before editing.

## Shape

One Go module, one binary, internal packages with one job each:

```
main.go            CLI: flag groups + subcommands; thin glue only
internal/extract   file -> Document{Text, Title, Format, Wrapped}
internal/clean     whitespace/reflow/page-number normalisation
internal/chunk     paragraph -> sentence -> word splitter (rune sizes);
                   units: chunk (packed), paragraph, document
internal/records   JSONL {"id","text",...} -> Record{Ref, Text, Meta}
internal/store     project dir, append-only JSONL, deterministic IDs
internal/llm       Client interface; openai + anthropic raw-HTTP backends; Retry
internal/gen       prompts per task (question/prompt/reply), output
                   parsing, resumable worker pipeline
internal/export    alpaca / sharegpt / messages / pairs / text writers
```

Dependency direction: `main -> gen -> {llm, store, records}`, `main ->
{extract -> clean, chunk, records, export}`. Nothing imports `main`;
`llm`, `store`, `chunk`, `clean`, `records`, `export` import nothing from
this module. Keep it that way.

The only third-party dependency is `golang.org/x/net/html` (pinned to a
version whose `go.mod` is <= 1.24). The Anthropic Go SDK was deliberately
not used: it drags in a dozen modules and a newer toolchain, and the
Messages API call is forty lines. Do not add the OpenAI SDK either.

## Load-bearing contracts

- **IDs are deterministic.** `store.SourceID` = first 12 hex of SHA-256
  of the file bytes; chunk ID `<src>-<index:04d>`; question ID
  `<chunk>-q<n>` with n starting at 1. Resume (`gen.Run`) and duplicate
  detection (`ingest`) are built on this. Changing the scheme orphans
  existing projects.
- **JSONL files are append-only.** `store` never rewrites a file.
  `Pairs()` resolves duplicates by "last answer wins", so a re-answer is
  appended, never edited in place. `AllPairs()` is the same join without
  dropping unanswered questions; `export -answer-source chunk` uses it.
- **`Chunk.Ref` and `Chunk.Meta` come only from JSONL record ingest**
  (`internal/records`). They are passthrough: Ref is the caller's own
  passage id and is never used for resume or dedup (chunk IDs still are);
  Meta is rendered into prompts (`records.FormatMeta`, keys sorted) and
  copied into the `pairs` export. Chunks cut by datamill have neither.
- **The question stage writes the user turn; `-task` decides what kind.**
  `question` (default) pairs with a model-written answer. `prompt` and
  `reply` are reverse tasks: the chunk itself is the assistant turn, so
  `gen.Run` downgrades `-stage all` to the question stage and refuses
  `-stage answers` (`Task.HasAnswerStage`). The records still live in
  `questions.jsonl` whatever the task; do not add a parallel file.
  `QuestionsPerChunk` is 1 for the reverse tasks regardless of length.
- **`-unit paragraph` and `-unit document`** are `chunk.Options.Unit`.
  Paragraph mode keeps the no-empty / `<= Max` / content-preserved
  invariants and glues runts *forward* (a heading joins the paragraph
  after it); overlap is ignored. Document mode is the whole text, any
  size. The default unit's behaviour is untouched.
- **`llm.Request.ReasoningEffort`** is sent as `reasoning_effort` by the
  OpenAI backend only (Ollama maps it to its think option; `"none"`
  turns Qwen3 thinking off). The Anthropic backend ignores it on purpose;
  do not translate it into `thinking` budgets without a test.
- **`clean.Text` output** has exactly one blank line between paragraphs
  and no leading/trailing whitespace. `chunk.Split` relies on
  `\n\n` as the paragraph separator.
- **`chunk.Split` invariants** (tested): no empty chunks; every chunk <=
  `Max` runes (+ overlap); content is preserved modulo whitespace. Sizes
  are runes, not bytes and not tokens — do not "fix" this to bytes.
- **`extract.Document.Wrapped`** decides reflow. PDF sets it; plain text
  uses `clean.LooksHardWrapped`; Markdown/HTML/EPUB/DOCX/MOBI never set
  it (their paragraphs are already real). Reflow on Markdown destroys
  lists and code, so keep that rule.
- **`llm.Retryable`** is the single source of truth for what is retried:
  network errors and 408/409/429/5xx. 4xx, context cancellation and
  `llm.ErrRefused` are never retried. `gen.Run` counts a final failure in
  `Stats.Failed` and leaves the item pending.
- **Anthropic requests omit `temperature` unless set**, because current
  Claude models return 400 for sampling parameters. The CLI maps
  `-temperature -1` (default) to nil.
- **Prompt parsing is defensive on purpose** (`gen.ParseQuestions`,
  `gen.ParseAnswer`): code fences, prose around the JSON, numbered lists,
  `{"question": ...}` objects, a leading `<think>` block before the
  array, `<think>`/`<reasoning>` variants and an unterminated `<answer>`
  are all accepted. Add new tolerated shapes with a test case rather than
  tightening the parser.

## MOBI reader

`internal/extract/mobi.go` implements PDB record parsing, the MOBI
header fields we need (compression, encoding, title, extra-data flags),
trailing-entry stripping (`trailingSize`) and PalmDOC LZ77 decompression.
`PalmDocCompress` exists only so tests can round-trip real compressed
records; it is not optimal and should not be used for output. HUFF/CDIC
returns `ErrMOBIHuffCDIC`, which `mobiFile` turns into an `ebook-convert`
fallback. Encrypted books return `ErrMOBIEncrypted`. The multibyte
trailing flag (bit 0) and the backwards varint are easy to get wrong —
there are tests with hand-computed byte sequences; keep them.

## Source-file gotcha

Go rejects a raw U+FEFF inside a string literal, and some editing tools
rewrite `\uXXXX` escapes into the literal character. Non-ASCII runes in
this code base are therefore written as numeric constants
(`0x00a0`, `rune(0xfeff)`), not as `' '`. Keep doing that.

## Testing

`go test ./...` from inside `datamill/`; no network and no external
programs are needed (the PDF path is only tested for the "tool missing"
error). `go test -race ./internal/gen/` exercises the worker pool. When
changing behaviour, add a test that fails without the change; the
round-trip and invariant tests (`chunk`, `extract`) catch most
regressions.

## What not to do

- Do not add a web UI, a database, or a daemon. The project directory of
  JSONL files is the whole state model.
- Do not share code with sibling tools in this repo (see root CLAUDE.md).
- Do not count tokens with a tokenizer library; rune counts with
  documented defaults are intentional and dependency-free.
- Do not make `ingest` rewrite `chunks.jsonl` on re-chunking. If a user
  wants different chunk sizes they start a new project directory.
