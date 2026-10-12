package extract

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// DOCXToText reads word/document.xml from a .docx container and returns
// its paragraphs. Each <w:p> becomes one paragraph; <w:tab> becomes a
// space and <w:br> a line break.
func DOCXToText(zr *zip.Reader) (string, error) {
	var docXML *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docXML = f
			break
		}
	}
	if docXML == nil {
		return "", fmt.Errorf("docx: word/document.xml not found")
	}
	rc, err := docXML.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return docxXMLToText(rc)
}

func docxXMLToText(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var paras []string
	var cur strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("docx: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tab":
				cur.WriteByte(' ')
			case "br", "cr":
				cur.WriteByte('\n')
			}
		case xml.EndElement:
			if t.Name.Local == "p" {
				if s := strings.TrimSpace(cur.String()); s != "" {
					paras = append(paras, s)
				}
				cur.Reset()
			}
		case xml.CharData:
			cur.Write(t)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		paras = append(paras, s)
	}
	return strings.Join(paras, "\n\n"), nil
}

func docxFile(p string) (*Document, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	text, err := DOCXToText(&zr.Reader)
	if err != nil {
		return nil, err
	}
	return &Document{Text: text, Format: FormatDOCX}, nil
}
