package extract

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

// MOBI (Mobipocket / Kindle KF7) support.
//
// A .mobi file is a PalmDOC database (PDB): a 78-byte header, a record
// index, then the records. Record 0 holds the PalmDOC header (compression
// type, text record count) followed by the MOBI header (encoding, name,
// trailing-entry flags). Records 1..N are the book text, usually PalmDOC
// (LZ77) compressed, each possibly followed by "trailing entries" that must
// be stripped before decompression. The decompressed text is HTML.
//
// HUFF/CDIC compression (used by some older Kindle books) is not
// implemented; such files fall back to calibre's ebook-convert.

const (
	mobiCompressionNone     = 1
	mobiCompressionPalmDoc  = 2
	mobiCompressionHuffCdic = 17480
)

// ErrMOBIHuffCDIC is returned for HUFF/CDIC-compressed books.
var ErrMOBIHuffCDIC = errors.New("mobi: HUFF/CDIC compression not supported")

// ErrMOBIEncrypted is returned for DRM-protected books.
var ErrMOBIEncrypted = errors.New("mobi: book is DRM-protected")

// MOBIBook is the parsed content of a MOBI file.
type MOBIBook struct {
	Title string
	// HTML is the decoded book markup as UTF-8.
	HTML string
}

// ParseMOBI parses the raw bytes of a .mobi/.prc/.azw file.
func ParseMOBI(data []byte) (*MOBIBook, error) {
	recs, err := pdbRecords(data)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, errors.New("mobi: no records")
	}
	r0 := recs[0]
	if len(r0) < 16 {
		return nil, errors.New("mobi: record 0 too short")
	}
	compression := binary.BigEndian.Uint16(r0[0:2])
	textRecords := int(binary.BigEndian.Uint16(r0[8:10]))
	encryption := binary.BigEndian.Uint16(r0[12:14])
	if encryption != 0 {
		return nil, ErrMOBIEncrypted
	}

	encoding := uint32(65001)
	extraFlags := uint16(0)
	title := ""
	if len(r0) >= 24 && string(r0[16:20]) == "MOBI" {
		headerLen := int(binary.BigEndian.Uint32(r0[20:24]))
		if len(r0) >= 32 {
			encoding = binary.BigEndian.Uint32(r0[28:32])
		}
		if headerLen >= 0xE4 && len(r0) >= 0xF4 {
			extraFlags = binary.BigEndian.Uint16(r0[0xF2:0xF4])
		}
		if len(r0) >= 0x5C {
			off := int(binary.BigEndian.Uint32(r0[0x54:0x58]))
			n := int(binary.BigEndian.Uint32(r0[0x58:0x5C]))
			if off >= 0 && n >= 0 && off+n <= len(r0) {
				title = string(r0[off : off+n])
			}
		}
	}

	if textRecords >= len(recs) {
		textRecords = len(recs) - 1
	}
	var text []byte
	for i := 1; i <= textRecords; i++ {
		rec := recs[i]
		rec = rec[:len(rec)-trailingSize(rec, extraFlags)]
		switch compression {
		case mobiCompressionNone:
			text = append(text, rec...)
		case mobiCompressionPalmDoc:
			text = append(text, PalmDocDecompress(rec)...)
		case mobiCompressionHuffCdic:
			return nil, ErrMOBIHuffCDIC
		default:
			return nil, fmt.Errorf("mobi: unknown compression type %d", compression)
		}
	}

	var html string
	if encoding == 1252 {
		html = cp1252ToString(text)
		title = cp1252ToString([]byte(title))
	} else {
		html = decodeText(text)
	}
	return &MOBIBook{Title: strings.TrimSpace(title), HTML: html}, nil
}

// pdbRecords splits a Palm database into its records.
func pdbRecords(data []byte) ([][]byte, error) {
	if len(data) < 78 {
		return nil, errors.New("mobi: file too short for a PDB header")
	}
	typ, creator := string(data[60:64]), string(data[64:68])
	if !(typ == "BOOK" && creator == "MOBI") && !(typ == "TEXt" && creator == "REAd") {
		return nil, fmt.Errorf("mobi: not a Mobipocket file (type %q creator %q)", typ, creator)
	}
	n := int(binary.BigEndian.Uint16(data[76:78]))
	if 78+n*8 > len(data) {
		return nil, errors.New("mobi: truncated record list")
	}
	offsets := make([]int, n)
	for i := 0; i < n; i++ {
		offsets[i] = int(binary.BigEndian.Uint32(data[78+i*8 : 82+i*8]))
	}
	recs := make([][]byte, n)
	for i := 0; i < n; i++ {
		start := offsets[i]
		end := len(data)
		if i+1 < n {
			end = offsets[i+1]
		}
		if start < 0 || start > end || end > len(data) {
			return nil, fmt.Errorf("mobi: record %d has bad bounds [%d,%d)", i, start, end)
		}
		recs[i] = data[start:end]
	}
	return recs, nil
}

// trailingSize returns the number of bytes at the end of a text record that
// belong to trailing entries (as signalled by the MOBI header's extra data
// flags) and must be removed before decompression.
func trailingSize(rec []byte, flags uint16) int {
	num := 0
	for f := flags >> 1; f != 0; f >>= 1 {
		if f&1 != 0 {
			num += backwardVarint(rec[:len(rec)-num])
		}
	}
	if flags&1 != 0 && len(rec)-num-1 >= 0 {
		num += int(rec[len(rec)-num-1]&3) + 1
	}
	if num > len(rec) {
		num = len(rec)
	}
	return num
}

// backwardVarint decodes the size of one trailing entry, stored as a
// variable-width big-endian integer read from the end of the record. The
// size includes the bytes of the integer itself.
func backwardVarint(b []byte) int {
	result, bitpos := 0, 0
	for i := len(b) - 1; i >= 0; i-- {
		v := int(b[i])
		result |= (v & 0x7F) << bitpos
		bitpos += 7
		if v&0x80 != 0 || bitpos >= 28 {
			break
		}
	}
	return result
}

// PalmDocDecompress expands PalmDOC (LZ77-style) compressed data.
//
// Byte semantics: 0x00 and 0x09..0x7F are literals; 0x01..0x08 mean "copy
// the next n bytes literally"; 0x80..0xBF start a two-byte back-reference
// (11-bit distance, 3-bit length+3); 0xC0..0xFF expand to a space followed
// by the byte XOR 0x80.
func PalmDocDecompress(in []byte) []byte {
	out := make([]byte, 0, len(in)*2)
	for i := 0; i < len(in); {
		c := in[i]
		i++
		switch {
		case c == 0 || (c >= 0x09 && c <= 0x7F):
			out = append(out, c)
		case c >= 0x01 && c <= 0x08:
			n := int(c)
			if i+n > len(in) {
				n = len(in) - i
			}
			out = append(out, in[i:i+n]...)
			i += n
		case c >= 0xC0:
			out = append(out, ' ', c^0x80)
		default: // 0x80..0xBF
			if i >= len(in) {
				return out
			}
			pair := int(c)<<8 | int(in[i])
			i++
			dist := (pair >> 3) & 0x7FF
			length := (pair & 7) + 3
			if dist == 0 || dist > len(out) {
				continue
			}
			start := len(out) - dist
			for k := 0; k < length; k++ {
				out = append(out, out[start+k])
			}
		}
	}
	return out
}

// PalmDocCompress is a simple (non-optimal) PalmDOC encoder, provided so
// tests can round-trip real compressed records. It emits literals and
// back-references only.
func PalmDocCompress(in []byte) []byte {
	var out []byte
	var lits []byte
	flushLits := func() {
		for len(lits) > 0 {
			n := len(lits)
			if n > 8 {
				n = 8
			}
			out = append(out, byte(n))
			out = append(out, lits[:n]...)
			lits = lits[n:]
		}
	}
	for i := 0; i < len(in); {
		bestLen, bestDist := 0, 0
		for dist := 1; dist <= 2047 && dist <= i; dist++ {
			l := 0
			for l < 10 && i+l < len(in) && in[i+l] == in[i-dist+l] {
				l++
			}
			if l > bestLen {
				bestLen, bestDist = l, dist
			}
		}
		if bestLen >= 3 {
			flushLits()
			pair := 0x8000 | (bestDist << 3) | (bestLen - 3)
			out = append(out, byte(pair>>8), byte(pair))
			i += bestLen
			continue
		}
		lits = append(lits, in[i])
		i++
	}
	flushLits()
	return out
}

// cp1252ToString decodes Windows-1252 bytes.
func cp1252ToString(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c >= 0x80 && c <= 0x9F:
			r := cp1252High[c-0x80]
			if r == 0 {
				r = 0xFFFD
			}
			sb.WriteRune(r)
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// cp1252High maps bytes 0x80..0x9F to Unicode (0 = undefined).
var cp1252High = [32]rune{
	0x20AC, 0, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017D, 0,
	0, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0, 0x017E, 0x0178,
}

func mobiFile(ctx context.Context, path string, opts Options) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	book, err := ParseMOBI(data)
	if errors.Is(err, ErrMOBIHuffCDIC) {
		text, cerr := runEbookConvert(ctx, opts.EbookConvert, path)
		if cerr != nil {
			return nil, fmt.Errorf("%w; fallback failed: %v", err, cerr)
		}
		return &Document{Text: text, Format: FormatMOBI, Wrapped: true}, nil
	}
	if err != nil {
		return nil, err
	}
	title, text, err := HTMLToText(strings.NewReader(book.HTML))
	if err != nil {
		return nil, err
	}
	if book.Title != "" {
		title = book.Title
	}
	return &Document{Title: title, Text: text, Format: FormatMOBI}, nil
}
