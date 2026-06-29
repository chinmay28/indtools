# splitmerge

Split a file into fixed-size chunks and merge them back into the original.
Zero external dependencies; standalone Go binary.

## Install

```sh
cd splitmerge
go build -o splitmerge .
# or, to install on $GOBIN:
go install .
```

Requires Go 1.24+.

## Usage

```text
splitmerge split -input FILE -size SIZE -output DIR [-buf BYTES] [-verbose]
splitmerge merge -input DIR  -output FILE [-name BASENAME] [-buf BYTES] [-verbose]
```

`SIZE` and `-buf` accept a `K`/`M`/`G`/`T` suffix (powers of 1024). A
trailing `B` or `iB` is tolerated, so `10M`, `10MB`, and `10MiB` are all
the same 10 MiB.

### Split

```sh
splitmerge split -input ./big.iso -size 100M -output ./chunks
```

Writes `big.iso.part000000`, `big.iso.part000001`, … into `./chunks`. The
zero-pad width is sized to the expected part count, with a minimum of six
digits. A 1 TB file split at 1 KB will use a wider field — no overflow.

### Merge

```sh
splitmerge merge -input ./chunks -output ./big.iso
```

`merge` auto-detects the chunk family in the input directory. If the
directory contains chunks from more than one file, pass
`-name <basename>` to disambiguate (the basename is the part of the
filename before `.partNNN...`).

Parts are concatenated in **numeric** index order, so chunks produced
with any pad width — or by an older version of the tool — merge
correctly.

### Verifying a round-trip

```sh
sha256sum original.bin merged.bin
```

The output file is a byte-for-byte copy when split and merge are run on
the same chunk set without modification.

## Performance and memory

- The default copy buffer is **8 MiB**, sized for throughput on machines
  with at least ~100 MiB free RAM.
- On Linux the standard library routes file-to-file copies through
  `copy_file_range`/`sendfile`, so most of the work happens in the
  kernel and the user-space buffer becomes a no-op. On macOS/Windows the
  buffer is used directly and a larger buffer means fewer syscalls.
- On hosts with less free memory, override `-buf` — any value is honored
  verbatim:
  ```sh
  splitmerge split -input ./big.iso -size 100M -output ./chunks -buf 256K
  ```
- Progress logging is **off by default**. Pass `-verbose` to log each
  chunk; otherwise only a single summary line is printed. With tens of
  thousands of small chunks this matters.

## Chunk filename format

```
<basename>.partNNNNNN
```

`<basename>` is the input filename (no path). `NNNNNN` is the zero-padded
part index starting at `000000`. The width is at least six digits and
grows as needed for very large files split into very small chunks. Merge
parses the numeric portion, so don't rename parts: keep the `.part`
suffix and a parseable integer after it.

## Testing

```sh
cd splitmerge
go test ./...
```

The test suite covers size parsing, the part-name format, large-index
numeric sort, ambiguous chunk families, and end-to-end round-trips at
boundary sizes (0, 1, exact-chunk, off-by-one, larger).
