// Package extract turns documents (PDF, EPUB, MOBI, DOCX, HTML, Markdown,
// plain text) into plain text.
//
// Contract: File returns a Document whose Text has paragraphs separated by
// blank lines and whose Wrapped flag tells the caller whether lines inside a
// paragraph are hard-wrapped (and therefore need reflowing by package
// clean). Text is valid UTF-8. The function never returns an empty Text
// without an error.
//
// Formats that need an external program (PDF needs poppler's pdftotext;
// HUFF/CDIC-compressed MOBI needs calibre's ebook-convert) report
// ErrToolMissing with the program name when it is not installed.
package extract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/chinmay28/indtools/datamill/internal/clean"
)

// Document is the result of extracting one file.
type Document struct {
	// Title is the document's own title when the format carries one
	// (EPUB/MOBI/HTML metadata), otherwise the file's base name.
	Title string
	// Text is plain text with paragraphs separated by one blank line.
	Text string
	// Format is one of the Format* constants.
	Format string
	// Wrapped is true when paragraphs contain hard line breaks that should
	// be joined (PDF output, fixed-column plain text).
	Wrapped bool
}

// Format identifiers.
const (
	FormatPDF      = "pdf"
	FormatEPUB     = "epub"
	FormatMOBI     = "mobi"
	FormatDOCX     = "docx"
	FormatHTML     = "html"
	FormatMarkdown = "markdown"
	FormatText     = "text"
)

// Options configures external helpers.
type Options struct {
	// PDFToText is the pdftotext executable (poppler-utils). Empty means
	// look up "pdftotext" on PATH.
	PDFToText string
	// EbookConvert is calibre's ebook-convert executable, used as a
	// fallback for MOBI files with HUFF/CDIC compression. Empty means look
	// up "ebook-convert" on PATH.
	EbookConvert string
}

// ErrToolMissing is wrapped by errors reporting a missing external program.
var ErrToolMissing = errors.New("required external program not found")

// ErrUnsupported is wrapped by errors for file types this package cannot read.
var ErrUnsupported = errors.New("unsupported file type")

// FormatOf guesses the format from the file extension. It returns "" for
// extensions it does not know.
func FormatOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return FormatPDF
	case ".epub":
		return FormatEPUB
	case ".mobi", ".prc", ".azw", ".azw3", ".azw4", ".kf8":
		return FormatMOBI
	case ".docx":
		return FormatDOCX
	case ".html", ".htm", ".xhtml":
		return FormatHTML
	case ".md", ".markdown", ".mdown", ".mkd":
		return FormatMarkdown
	case ".txt", ".text", ".asc", ".rst", ".org", ".tex", ".csv", ".log", ".json", ".yaml", ".yml":
		return FormatText
	}
	return ""
}

// Supported reports whether File can handle the given path, judged by
// extension only.
func Supported(path string) bool { return FormatOf(path) != "" }

// File extracts the document at path.
func File(ctx context.Context, path string, opts Options) (*Document, error) {
	format := FormatOf(path)
	if format == "" {
		// Unknown extension: accept it if it is plain UTF-8 text.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !looksLikeText(data) {
			return nil, fmt.Errorf("%w: %s", ErrUnsupported, path)
		}
		return textDocument(path, data, FormatText), nil
	}

	var doc *Document
	var err error
	switch format {
	case FormatPDF:
		doc, err = pdfFile(ctx, path, opts)
	case FormatEPUB:
		doc, err = epubFile(path)
	case FormatMOBI:
		doc, err = mobiFile(ctx, path, opts)
	case FormatDOCX:
		doc, err = docxFile(path)
	case FormatHTML:
		doc, err = htmlFile(path)
	default:
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			doc = textDocument(path, data, format)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Title == "" {
		doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if strings.TrimSpace(doc.Text) == "" {
		return nil, fmt.Errorf("%s: no text could be extracted", path)
	}
	return doc, nil
}

// textDocument wraps raw text bytes as a Document, repairing invalid UTF-8.
func textDocument(path string, data []byte, format string) *Document {
	text := decodeText(data)
	wrapped := false
	if format == FormatText {
		wrapped = clean.LooksHardWrapped(text)
	}
	return &Document{Text: text, Format: format, Wrapped: wrapped}
}

// decodeText converts bytes to a string: a UTF-8 BOM is dropped, and a file
// that is not valid UTF-8 is decoded as Windows-1252 (the most common
// legacy encoding for English-language text).
func decodeText(data []byte) string {
	data = stripBOM(data)
	if utf8.Valid(data) {
		return string(data)
	}
	return cp1252ToString(data)
}

func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// looksLikeText accepts data without NUL bytes that is valid UTF-8 (after
// BOM removal) or plausible 8-bit text.
func looksLikeText(data []byte) bool {
	data = stripBOM(data)
	if len(data) == 0 {
		return false
	}
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	for _, b := range sample {
		if b == 0 {
			return false
		}
	}
	return utf8.Valid(sample) || isMostlyPrintable(sample)
}

func isMostlyPrintable(b []byte) bool {
	bad := 0
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' && c != '\f' {
			bad++
		}
	}
	return bad*100 < len(b)
}
