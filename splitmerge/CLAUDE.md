# CLAUDE.md — splitmerge

Agent-facing notes for this tool. Read before editing.

## Scope

A single Go binary with two subcommands, `split` and `merge`. No external
dependencies, no sub-packages — everything lives in `main.go` and its
test file. Keep it that way unless the tool grows substantially.

## Filename contract (load-bearing)

Chunks are named `<basename>.partNNN...` where `<basename>` is
`filepath.Base(inputPath)` and `NNN...` is a zero-padded decimal index
starting at `000000`.

- Width is chosen by `digitsFor(fileSize, chunkSize)` with a floor of
  `minPartDigits = 6`. The floor is for ergonomics; merge does **not**
  depend on it.
- `findParts` sorts by **parsed numeric index**, not lexicographically.
  Any width works, including chunks from older versions of the tool.
- Keep the `.part` literal as the `partSuffix`. Changing it breaks every
  chunk previously produced.

If you add a feature that needs additional metadata in filenames (e.g.
total part count, source checksum), put it **before** the `.partNNN`
suffix so the existing parser still finds the index.

## Memory and speed defaults

- Default copy buffer is `defaultBufBytes = 8 MiB`. The choice assumes
  the documented ~100 MiB free-RAM budget. Do not silently raise it
  further without changing the README budget statement to match.
- `-buf` is honored verbatim with no floor — that is intentional. The
  operator on a 32 MiB embedded box must be able to ask for `-buf 16K`
  and get exactly that.
- On Linux, `*os.File.ReadFrom` routes through
  `copy_file_range`/`sendfile`, so the user-space `buf` is largely a
  fallback. Do not "optimize" by removing the buffer; the fallback path
  on macOS/Windows uses it directly.
- Per-chunk progress prints are gated behind `-verbose`. Tens of
  thousands of `Printf` calls is a real bottleneck — do not move logging
  back into the hot loop.

## Empty-input behaviour

A zero-byte input still produces a single zero-byte `partNNN000000` so
the chunk family always exists on disk. The `if index > 0 { remove }`
branch in the split loop preserves that invariant — don't simplify it
without preserving the empty-file case.

## Testing

`go test ./...` from inside `splitmerge/`. The suite covers:

- `parseSize` (suffix handling, edge cases).
- `partName` at the default and at over-width indices.
- `digitsFor` boundaries.
- `findParts` numeric ordering with mixed-width siblings.
- End-to-end round-trips at sizes 0, 1, 1023, 1024, 1025, 10 000.
- Ambiguous-family error path.

When adding behaviour, add a test that would fail without the change.
Round-trip tests are the most valuable — they catch nearly every
regression that matters.

## What not to do

- Do not introduce a third-party dependency for argument parsing,
  progress bars, or anything else without a strong reason. The lack of
  dependencies is a feature of this tool.
- Do not promote any helper into a shared package outside this
  directory. The repo's invariant is one self-contained tool per
  directory (see the repo-root `CLAUDE.md`).
- Do not change the chunk filename layout, the `.part` suffix, or the
  numeric-index parse rule without a migration story for existing chunk
  families on disk.
