// splitmerge is a small CLI that splits a file into fixed-size chunks and
// merges those chunks back into the original file.
//
// Usage:
//
//	splitmerge split -input FILE -size SIZE -output DIR [-buf BYTES]
//	splitmerge merge -input DIR  -output FILE [-name BASENAME] [-buf BYTES]
//
// SIZE and -buf accept plain bytes or a suffix: K, M, G, T (powers of 1024).
//
// Speed: copies are optimized for throughput. On Linux, *os.File satisfies
// io.ReaderFrom and the standard library routes copies through
// copy_file_range/sendfile, so file-to-file moves stay in the kernel. On
// other platforms the user-space buffer is used directly. Per-chunk progress
// logging is off by default; pass -verbose to enable it.
//
// Memory: the default 8 MiB buffer assumes at least ~100 MiB of free RAM.
// Override -buf (e.g. -buf 256K) on smaller machines; any value is honored
// verbatim, down to a few KiB.
//
// Chunks are written as "<basename>.partNNN..." with a width sized to the
// expected part count (minimum 6 digits). Merge sorts by parsed numeric
// index, so chunks produced with any width are accepted.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	partSuffix      = ".part"
	minPartDigits   = 6
	defaultBufBytes = 8 << 20 // 8 MiB
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "split":
		err = runSplit(os.Args[2:])
	case "merge":
		err = runMerge(os.Args[2:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `splitmerge - split a file into chunks and merge them back

Commands:
  split   Split an input file into fixed-size chunks
  merge   Merge chunks in an input directory back into a single file

Split:
  splitmerge split -input FILE -size SIZE -output DIR [-buf BYTES] [-verbose]

Merge:
  splitmerge merge -input DIR -output FILE [-name BASENAME] [-buf BYTES] [-verbose]

SIZE and -buf accept a suffix: K, M, G, T (powers of 1024). Examples:
  -size 10M, -size 1G, -buf 16M.

Default buffer is 8 MiB, tuned for throughput on machines with at least
~100 MiB of free RAM. On smaller machines override -buf (e.g. -buf 256K)
to cap memory use; any value is honored verbatim. Pass -verbose to log
every chunk; otherwise only a summary is printed.
`)
}

func runSplit(args []string) error {
	fs := flag.NewFlagSet("split", flag.ContinueOnError)
	input := fs.String("input", "", "path to the input file to split (required)")
	sizeStr := fs.String("size", "", "chunk size, e.g. 10M, 1G (required)")
	output := fs.String("output", "", "directory to write chunks to (required)")
	bufStr := fs.String("buf", "8M", "copy buffer size; tuned for throughput")
	verbose := fs.Bool("verbose", false, "log every chunk; off by default for speed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *sizeStr == "" || *output == "" {
		fs.Usage()
		return errors.New("split: -input, -size, and -output are required")
	}

	size, err := parseSize(*sizeStr)
	if err != nil {
		return fmt.Errorf("invalid -size: %w", err)
	}
	if size <= 0 {
		return errors.New("-size must be greater than zero")
	}
	bufBytes, err := parseBuf(*bufStr)
	if err != nil {
		return err
	}

	in, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	width := digitsFor(info.Size(), size)

	if err := os.MkdirAll(*output, 0o755); err != nil {
		return err
	}

	base := filepath.Base(*input)
	buf := make([]byte, bufBytes)
	var (
		index   int
		written int64
	)
	for {
		partPath := filepath.Join(*output, partName(base, index, width))
		out, err := os.Create(partPath)
		if err != nil {
			return err
		}

		n, copyErr := io.CopyBuffer(out, io.LimitReader(in, size), buf)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}

		if n == 0 {
			// Nothing was read this round; the part is empty. Remove it
			// unless it's the first (and only) part of an empty input,
			// where we still want one zero-byte chunk on disk.
			if index > 0 {
				_ = os.Remove(partPath)
				break
			}
		}

		written += n
		index++
		if *verbose {
			fmt.Printf("wrote %s (%d bytes)\n", partPath, n)
		}

		if n < size {
			break
		}
	}

	fmt.Printf("split complete: %d byte(s) into %d part(s) in %s\n", written, index, *output)
	return nil
}

func runMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	input := fs.String("input", "", "directory containing chunk files (required)")
	output := fs.String("output", "", "path to write the merged file (required)")
	name := fs.String("name", "", "basename of the chunks to merge (default: auto-detect)")
	bufStr := fs.String("buf", "8M", "copy buffer size; tuned for throughput")
	verbose := fs.Bool("verbose", false, "log every chunk; off by default for speed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *output == "" {
		fs.Usage()
		return errors.New("merge: -input and -output are required")
	}
	bufBytes, err := parseBuf(*bufStr)
	if err != nil {
		return err
	}

	parts, err := findParts(*input, *name)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return fmt.Errorf("no chunk files found in %s", *input)
	}

	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	out, err := os.Create(*output)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, bufBytes)
	var total int64
	for _, p := range parts {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		n, err := io.CopyBuffer(out, f, buf)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		total += n
		if *verbose {
			fmt.Printf("merged %s (%d bytes)\n", p, n)
		}
	}

	fmt.Printf("merge complete: %d byte(s) from %d part(s) into %s\n", total, len(parts), *output)
	return nil
}

// partName returns "<base>.partNNN..." with index zero-padded to width.
func partName(base string, index, width int) string {
	if width < 1 {
		width = 1
	}
	return fmt.Sprintf("%s%s%0*d", base, partSuffix, width, index)
}

// digitsFor returns the zero-padding width to use for chunk filenames given
// the input file size and chunk size. The width is large enough to hold the
// highest index, with a floor of minPartDigits so small files still get
// pleasant-looking names.
func digitsFor(fileSize, chunkSize int64) int {
	if chunkSize <= 0 || fileSize <= 0 {
		return minPartDigits
	}
	parts := (fileSize + chunkSize - 1) / chunkSize
	w := len(strconv.FormatInt(parts-1, 10))
	if w < minPartDigits {
		w = minPartDigits
	}
	return w
}

type partRef struct {
	index int64
	path  string
}

// findParts returns the chunk files in dir for the given basename, sorted by
// numeric part index (so any zero-pad width works). If basename is empty, it
// is inferred from the directory contents and merge fails if more than one
// chunk family is present.
func findParts(dir, basename string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	bases := map[string][]partRef{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		idx := strings.LastIndex(name, partSuffix)
		if idx < 0 {
			continue
		}
		suffix := name[idx+len(partSuffix):]
		if len(suffix) == 0 {
			continue
		}
		n, err := strconv.ParseInt(suffix, 10, 64)
		if err != nil {
			continue
		}
		b := name[:idx]
		bases[b] = append(bases[b], partRef{index: n, path: filepath.Join(dir, name)})
	}

	pick := func(refs []partRef) []string {
		sort.Slice(refs, func(i, j int) bool { return refs[i].index < refs[j].index })
		out := make([]string, len(refs))
		for i, r := range refs {
			out[i] = r.path
		}
		return out
	}

	if basename == "" {
		switch len(bases) {
		case 0:
			return nil, nil
		case 1:
			for _, v := range bases {
				return pick(v), nil
			}
		default:
			keys := make([]string, 0, len(bases))
			for k := range bases {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return nil, fmt.Errorf("multiple chunk families in %s (%s); pass -name to disambiguate", dir, strings.Join(keys, ", "))
		}
	}

	v, ok := bases[basename]
	if !ok {
		return nil, fmt.Errorf("no chunks found for basename %q in %s", basename, dir)
	}
	return pick(v), nil
}

// parseBuf parses -buf. A zero value falls back to the default; otherwise the
// user's value is honored verbatim (we trust the operator to pick a sensible
// size for their environment).
func parseBuf(s string) (int, error) {
	v, err := parseSize(s)
	if err != nil {
		return 0, fmt.Errorf("invalid -buf: %w", err)
	}
	if v <= 0 {
		return defaultBufBytes, nil
	}
	return int(v), nil
}

// parseSize accepts decimal bytes with an optional K/M/G/T suffix (1024-based).
// A trailing "B" or "iB" is tolerated (e.g. "10MB", "10MiB").
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	upper := strings.ToUpper(s)
	upper = strings.TrimSuffix(upper, "IB")
	upper = strings.TrimSuffix(upper, "B")

	mult := int64(1)
	if n := len(upper); n > 0 {
		switch upper[n-1] {
		case 'K':
			mult = 1 << 10
			upper = upper[:n-1]
		case 'M':
			mult = 1 << 20
			upper = upper[:n-1]
		case 'G':
			mult = 1 << 30
			upper = upper[:n-1]
		case 'T':
			mult = 1 << 40
			upper = upper[:n-1]
		}
	}
	upper = strings.TrimSpace(upper)

	v, err := strconv.ParseInt(upper, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q", s)
	}
	if v < 0 {
		return 0, errors.New("size must be non-negative")
	}
	return v * mult, nil
}
