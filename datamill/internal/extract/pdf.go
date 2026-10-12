package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// pdfFile shells out to poppler's pdftotext. Pages are separated by form
// feeds, which package clean turns into paragraph breaks.
func pdfFile(ctx context.Context, path string, opts Options) (*Document, error) {
	bin := opts.PDFToText
	if bin == "" {
		bin = "pdftotext"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("%w: %s (install poppler-utils)", ErrToolMissing, bin)
	}
	cmd := exec.CommandContext(ctx, bin, "-enc", "UTF-8", "-eol", "unix", "-nopgbrk", path, "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftotext failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	text := decodeText(stdout.Bytes())
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("pdf has no extractable text (scanned image? run OCR first)")
	}
	return &Document{Text: text, Format: FormatPDF, Wrapped: true}, nil
}

// runEbookConvert converts any calibre-supported file to plain text.
func runEbookConvert(ctx context.Context, bin, in string) (string, error) {
	if bin == "" {
		bin = "ebook-convert"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("%w: %s (install calibre)", ErrToolMissing, bin)
	}
	tmp, err := newTempTxt()
	if err != nil {
		return "", err
	}
	defer tmp.cleanup()
	cmd := exec.CommandContext(ctx, bin, in, tmp.path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ebook-convert failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	data, err := tmp.read()
	if err != nil {
		return "", err
	}
	return decodeText(data), nil
}
