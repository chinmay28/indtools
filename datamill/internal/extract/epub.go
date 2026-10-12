package extract

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// EPUB structures (only the fields we read).
type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubPackage struct {
	Metadata struct {
		Title []string `xml:"title"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID        string `xml:"id,attr"`
			Href      string `xml:"href,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		ItemRefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// EPUBToText reads an EPUB (a zip container) and returns its title and the
// text of every spine document in reading order.
func EPUBToText(zr *zip.Reader) (title, text string, err error) {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[path.Clean(f.Name)] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[path.Clean(name)]
		if !ok {
			return nil, fmt.Errorf("epub: missing %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	cdata, err := read("META-INF/container.xml")
	if err != nil {
		return "", "", err
	}
	var c epubContainer
	if err := xml.Unmarshal(cdata, &c); err != nil {
		return "", "", fmt.Errorf("epub: container.xml: %w", err)
	}
	if len(c.Rootfiles) == 0 {
		return "", "", fmt.Errorf("epub: container.xml has no rootfile")
	}
	opfPath := c.Rootfiles[0].FullPath
	odata, err := read(opfPath)
	if err != nil {
		return "", "", err
	}
	var pkg epubPackage
	if err := xml.Unmarshal(odata, &pkg); err != nil {
		return "", "", fmt.Errorf("epub: %s: %w", opfPath, err)
	}
	if len(pkg.Metadata.Title) > 0 {
		title = strings.TrimSpace(pkg.Metadata.Title[0])
	}

	hrefByID := make(map[string]string)
	for _, it := range pkg.Manifest.Items {
		hrefByID[it.ID] = it.Href
	}
	base := path.Dir(opfPath)
	var parts []string
	for _, ref := range pkg.Spine.ItemRefs {
		href, ok := hrefByID[ref.IDRef]
		if !ok {
			continue
		}
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		if i := strings.IndexByte(href, '#'); i >= 0 {
			href = href[:i]
		}
		full := href
		if base != "." {
			full = path.Join(base, href)
		}
		data, err := read(full)
		if err != nil {
			return "", "", err
		}
		_, t, err := HTMLToText(strings.NewReader(string(data)))
		if err != nil {
			return "", "", fmt.Errorf("epub: %s: %w", full, err)
		}
		if t != "" {
			parts = append(parts, t)
		}
	}
	return title, strings.Join(parts, "\n\n"), nil
}

func epubFile(p string) (*Document, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	title, text, err := EPUBToText(&zr.Reader)
	if err != nil {
		return nil, err
	}
	return &Document{Title: title, Text: text, Format: FormatEPUB}, nil
}
