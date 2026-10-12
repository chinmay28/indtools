package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	in := `<html><head><title> My  Title </title><style>p{color:red}</style></head>
<body><h1>Heading</h1><p>First   paragraph
with   wrapped
lines &amp; entities.</p><script>var x = "<p>no</p>";</script>
<div>Line one<br>Line two</div>
<ul><li>a</li><li>b</li></ul>
<table><tr><td>c1</td><td>c2</td></tr></table>
<pre>code
  indented</pre>
<p>  </p><p>Last.</p></body></html>`
	title, text, err := HTMLToText(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if title != "My  Title" && title != "My Title" {
		t.Fatalf("title %q", title)
	}
	want := "Heading\n\nFirst paragraph with wrapped lines & entities.\n\nLine one\nLine two\n\na\n\nb\n\nc1 c2\n\ncode\n  indented\n\nLast."
	if text != want {
		t.Fatalf("text:\n got %q\nwant %q", text, want)
	}
}

func TestHTMLToTextUnclosedAndNested(t *testing.T) {
	in := `<p>One<p>Two<div><span>Three</span> four</div>`
	_, text, err := HTMLToText(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if text != "One\n\nTwo\n\nThree four" {
		t.Fatalf("got %q", text)
	}
}

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sampleEPUB(t *testing.T) []byte {
	return buildZip(t, map[string]string{
		"mimetype": "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" version="2.0">
  <metadata><dc:title>Test Book</dc:title></metadata>
  <manifest>
    <item id="ch2" href="text/ch%202.xhtml" media-type="application/xhtml+xml"/>
    <item id="ch1" href="text/ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="css" href="style.css" media-type="text/css"/>
  </manifest>
  <spine><itemref idref="ch1"/><itemref idref="ch2"/></spine>
</package>`,
		"OEBPS/text/ch1.xhtml":  `<html><body><h1>Chapter 1</h1><p>It was a dark night.</p></body></html>`,
		"OEBPS/text/ch 2.xhtml": `<html><body><h1>Chapter 2</h1><p>Morning came.</p></body></html>`,
		"OEBPS/style.css":       `p{}`,
	})
}

func TestEPUBToText(t *testing.T) {
	data := sampleEPUB(t)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	title, text, err := EPUBToText(zr)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Test Book" {
		t.Fatalf("title %q", title)
	}
	want := "Chapter 1\n\nIt was a dark night.\n\nChapter 2\n\nMorning came."
	if text != want {
		t.Fatalf("got %q want %q", text, want)
	}
}

func TestEPUBMissingContainer(t *testing.T) {
	data := buildZip(t, map[string]string{"mimetype": "application/epub+zip"})
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if _, _, err := EPUBToText(zr); err == nil {
		t.Fatal("expected error")
	}
}

func TestDOCXToText(t *testing.T) {
	data := buildZip(t, map[string]string{
		"word/document.xml": `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t xml:space="preserve"> world</w:t></w:r></w:p>
<w:p><w:r><w:t>Tab</w:t><w:tab/><w:t>bed</w:t><w:br/><w:t>next</w:t></w:r></w:p>
<w:p/>
</w:body></w:document>`,
	})
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	text, err := DOCXToText(zr)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello world\n\nTab bed\nnext" {
		t.Fatalf("got %q", text)
	}
}

func TestPalmDocRoundTrip(t *testing.T) {
	cases := []string{
		"",
		"a",
		"hello hello hello hello world",
		strings.Repeat("The quick brown fox jumps over the lazy dog. ", 50),
		"tabs\tand\nnewlines and high bytes \xe9\xff end",
	}
	for _, c := range cases {
		got := string(PalmDocDecompress(PalmDocCompress([]byte(c))))
		if got != c {
			t.Errorf("round trip mismatch for %q: got %q", c, got)
		}
	}
}

func TestPalmDocDecompressKnownBytes(t *testing.T) {
	// "ab" then a 0xC0-class byte -> " " + 'A'; literal run of 2; backref
	// distance 2 length 3 over "cd" -> "cdc".
	in := []byte{'a', 'b', 0xC1, 0x02, 'c', 'd', 0x80 | (2 << 3 >> 8), byte(2<<3) | 0}
	got := PalmDocDecompress(in)
	if string(got) != "ab Acdcdc" {
		t.Fatalf("got %q", got)
	}
}

func TestTrailingSize(t *testing.T) {
	// flags bit1 set: one trailing entry whose size byte is 0x83 (=3, includes itself).
	rec := []byte{'x', 'y', 'z', 'p', 'q', 0x83}
	if got := trailingSize(rec, 0x02); got != 3 {
		t.Fatalf("got %d", got)
	}
	// multibyte flag only: last byte & 3 = 2 overflow bytes + the flag byte.
	rec = []byte{'x', 'y', 'z', 0xE2, 0x82, 0x02}
	if got := trailingSize(rec, 0x01); got != 3 {
		t.Fatalf("got %d", got)
	}
	// both: entry of 3 then multibyte 1 (flag byte 0x00 => 0 overflow + 1).
	rec = []byte{'x', 'y', 0x00, 'a', 'b', 0x83}
	if got := trailingSize(rec, 0x03); got != 4 {
		t.Fatalf("got %d", got)
	}
	// two-byte varint read backwards: 0x02 (low 7 bits), then 0x81 whose high
	// bit marks the end => 2 | (1<<7) = 130 bytes.
	rec = make([]byte, 200)
	rec[198], rec[199] = 0x81, 0x02
	if got := trailingSize(rec, 0x02); got != 130 {
		t.Fatalf("got %d", got)
	}
	if got := trailingSize([]byte{}, 0x03); got != 0 {
		t.Fatalf("empty record: got %d", got)
	}
}

// buildMOBI assembles a minimal Mobipocket file from HTML, using the given
// compression (1 or 2), encoding and per-record trailing bytes.
func buildMOBI(t *testing.T, html string, compression uint16, encoding uint32, title string) []byte {
	t.Helper()
	const recSize = 4096
	var textRecs [][]byte
	body := []byte(html)
	for len(body) > 0 {
		n := recSize
		if n > len(body) {
			n = len(body)
		}
		chunk := body[:n]
		body = body[n:]
		if compression == mobiCompressionPalmDoc {
			chunk = PalmDocCompress(chunk)
		}
		// Append a trailing entry of 3 bytes ("zz" + size byte 0x83) to
		// exercise stripping (flags bit 1).
		chunk = append(append([]byte{}, chunk...), 'z', 'z', 0x83)
		textRecs = append(textRecs, chunk)
	}

	// Record 0: 16-byte PalmDOC header + MOBI header of 0xE8 bytes + title.
	r0 := make([]byte, 16+0xE8)
	binary.BigEndian.PutUint16(r0[0:2], compression)
	binary.BigEndian.PutUint32(r0[4:8], uint32(len(html)))
	binary.BigEndian.PutUint16(r0[8:10], uint16(len(textRecs)))
	binary.BigEndian.PutUint16(r0[10:12], recSize)
	copy(r0[16:20], "MOBI")
	binary.BigEndian.PutUint32(r0[20:24], 0xE8)
	binary.BigEndian.PutUint32(r0[28:32], encoding)
	binary.BigEndian.PutUint32(r0[0x54:0x58], uint32(len(r0)))
	binary.BigEndian.PutUint32(r0[0x58:0x5C], uint32(len(title)))
	binary.BigEndian.PutUint16(r0[0xF2:0xF4], 0x02)
	r0 = append(r0, []byte(title)...)

	recs := append([][]byte{r0}, textRecs...)
	hdr := make([]byte, 78)
	copy(hdr[0:], "testbook")
	copy(hdr[60:64], "BOOK")
	copy(hdr[64:68], "MOBI")
	binary.BigEndian.PutUint16(hdr[76:78], uint16(len(recs)))
	out := append([]byte{}, hdr...)
	off := 78 + 8*len(recs) + 2
	for i := range recs {
		var e [8]byte
		binary.BigEndian.PutUint32(e[0:4], uint32(off))
		e[7] = byte(i)
		out = append(out, e[:]...)
		off += len(recs[i])
	}
	out = append(out, 0, 0)
	for _, r := range recs {
		out = append(out, r...)
	}
	return out
}

func TestParseMOBI(t *testing.T) {
	html := "<html><body><h1>Title</h1>" + strings.Repeat("<p>Paragraph of text that repeats. </p>", 400) + "</body></html>"
	for _, comp := range []uint16{mobiCompressionNone, mobiCompressionPalmDoc} {
		book, err := ParseMOBI(buildMOBI(t, html, comp, 65001, "My Book"))
		if err != nil {
			t.Fatalf("compression %d: %v", comp, err)
		}
		if book.Title != "My Book" {
			t.Fatalf("title %q", book.Title)
		}
		if book.HTML != html {
			t.Fatalf("compression %d: html mismatch (len %d vs %d)", comp, len(book.HTML), len(html))
		}
	}
}

func TestParseMOBICP1252(t *testing.T) {
	html := "<p>caf\xe9 \x93quoted\x94</p>"
	book, err := ParseMOBI(buildMOBI(t, html, mobiCompressionNone, 1252, "Caf\xe9"))
	if err != nil {
		t.Fatal(err)
	}
	if book.HTML != "<p>café “quoted”</p>" || book.Title != "Café" {
		t.Fatalf("got %q / %q", book.HTML, book.Title)
	}
}

func TestParseMOBIErrors(t *testing.T) {
	if _, err := ParseMOBI([]byte("short")); err == nil {
		t.Fatal("expected error for short input")
	}
	data := buildMOBI(t, "<p>x</p>", mobiCompressionNone, 65001, "")
	copy(data[60:64], "XXXX")
	if _, err := ParseMOBI(data); err == nil {
		t.Fatal("expected error for wrong type")
	}
	data = buildMOBI(t, "<p>x</p>", mobiCompressionHuffCdic, 65001, "")
	if _, err := ParseMOBI(data); !errors.Is(err, ErrMOBIHuffCDIC) {
		t.Fatalf("expected ErrMOBIHuffCDIC, got %v", err)
	}
}

func TestFileDispatch(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()

	// Plain text with BOM and CRLF.
	p := write("a.txt", []byte("\xEF\xBB\xBFhello\r\nworld"))
	doc, err := File(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format != FormatText || doc.Text != "hello\r\nworld" || doc.Title != "a" {
		t.Fatalf("%+v", doc)
	}

	// Markdown is never reflowed.
	p = write("b.md", []byte(strings.Repeat("- a list item that is fairly long and has no punctuation at end\n", 20)))
	doc, err = File(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format != FormatMarkdown || doc.Wrapped {
		t.Fatalf("%+v", doc)
	}

	// Hard-wrapped plain text is flagged.
	p = write("c.txt", []byte(strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit sed do\n", 20)))
	doc, err = File(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Wrapped {
		t.Fatal("expected Wrapped")
	}

	// Unknown extension but text content is accepted; binary is not.
	p = write("notes.unknownext", []byte("plain text here"))
	if _, err := File(ctx, p, Options{}); err != nil {
		t.Fatal(err)
	}
	p = write("bin.unknownext", []byte{0, 1, 2, 3})
	if _, err := File(ctx, p, Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}

	// Latin-1 bytes are decoded rather than rejected.
	p = write("d.txt", []byte("na\xefve caf\xe9"))
	doc, err = File(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != "naïve café" {
		t.Fatalf("got %q", doc.Text)
	}

	// Empty file is an error.
	p = write("e.txt", []byte("   \n"))
	if _, err := File(ctx, p, Options{}); err == nil {
		t.Fatal("expected error for empty text")
	}

	// EPUB, DOCX, MOBI, HTML through the dispatcher.
	p = write("book.epub", sampleEPUB(t))
	doc, err = File(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format != FormatEPUB || doc.Title != "Test Book" || !strings.Contains(doc.Text, "Morning came.") {
		t.Fatalf("%+v", doc)
	}
	p = write("book.mobi", buildMOBI(t, "<html><head><title>T</title></head><body><p>Mobi text.</p></body></html>", mobiCompressionPalmDoc, 65001, "Mobi Book"))
	doc, err = File(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format != FormatMOBI || doc.Title != "Mobi Book" || doc.Text != "Mobi text." {
		t.Fatalf("%+v", doc)
	}
	p = write("page.html", []byte("<title>Page</title><p>Hi</p>"))
	doc, err = File(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format != FormatHTML || doc.Title != "Page" || doc.Text != "Hi" {
		t.Fatalf("%+v", doc)
	}

	// Missing pdftotext reports ErrToolMissing.
	p = write("x.pdf", []byte("%PDF-1.4"))
	if _, err := File(ctx, p, Options{PDFToText: "definitely-not-installed-xyz"}); !errors.Is(err, ErrToolMissing) {
		t.Fatalf("expected ErrToolMissing, got %v", err)
	}
}

func TestFormatOf(t *testing.T) {
	cases := map[string]string{
		"a.PDF": FormatPDF, "b.epub": FormatEPUB, "c.azw3": FormatMOBI, "d.docx": FormatDOCX,
		"e.htm": FormatHTML, "f.markdown": FormatMarkdown, "g.txt": FormatText, "h.bin": "",
	}
	for in, want := range cases {
		if got := FormatOf(in); got != want {
			t.Errorf("FormatOf(%q) = %q, want %q", in, got, want)
		}
	}
}
