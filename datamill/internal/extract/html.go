package extract

import (
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLToText strips markup and returns the <title> and the body text, with
// block-level elements separated by blank lines and inline whitespace
// collapsed (as a browser would render it). <pre> content keeps its line
// breaks. Contents of <script>, <style>, <head>, <noscript>, <template> and
// <svg> are dropped.
func HTMLToText(r io.Reader) (title, text string, err error) {
	z := html.NewTokenizer(r)
	var b strings.Builder
	var titleB strings.Builder
	skipDepth := 0
	preDepth := 0
	inTitle := false

	para := func() {
		s := b.String()
		if s == "" || strings.HasSuffix(s, "\n\n") {
			return
		}
		if strings.HasSuffix(s, "\n") {
			b.WriteByte('\n')
			return
		}
		b.WriteString("\n\n")
	}

	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				return strings.TrimSpace(titleB.String()), finishText(b.String()), nil
			}
			return "", "", z.Err()
		case html.TextToken:
			if inTitle {
				titleB.Write(z.Text())
				continue
			}
			if skipDepth > 0 {
				continue
			}
			if preDepth > 0 {
				b.Write(z.Text())
				continue
			}
			writeCollapsed(&b, string(z.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			tn, _ := z.TagName()
			a := atom.Lookup(tn)
			if a == atom.Title {
				inTitle = true
				continue
			}
			if isSkipped(a) {
				if tt == html.StartTagToken {
					skipDepth++
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			switch {
			case a == atom.Br:
				b.WriteByte('\n')
			case a == atom.Pre:
				para()
				if tt == html.StartTagToken {
					preDepth++
				}
			case a == atom.Td || a == atom.Th:
				if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, " ") {
					b.WriteByte(' ')
				}
			case isBlock(a):
				para()
			}
		case html.EndTagToken:
			tn, _ := z.TagName()
			a := atom.Lookup(tn)
			if a == atom.Title {
				inTitle = false
				continue
			}
			if isSkipped(a) {
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			if a == atom.Pre {
				if preDepth > 0 {
					preDepth--
				}
				para()
			} else if isBlock(a) {
				para()
			}
		}
	}
}

func isSkipped(a atom.Atom) bool {
	switch a {
	case atom.Script, atom.Style, atom.Head, atom.Noscript, atom.Template, atom.Svg, atom.Math:
		return true
	}
	return false
}

func isBlock(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Li, atom.Ul, atom.Ol, atom.Dl, atom.Dt, atom.Dd, atom.Tr, atom.Table,
		atom.Thead, atom.Tbody, atom.Blockquote, atom.Section, atom.Article,
		atom.Header, atom.Footer, atom.Nav, atom.Aside, atom.Hr, atom.Figure,
		atom.Figcaption, atom.Body, atom.Html, atom.Address, atom.Main, atom.Form,
		atom.Fieldset, atom.Details, atom.Summary, atom.Center:
		return true
	}
	return false
}

// writeCollapsed appends s with runs of whitespace collapsed to one space;
// a leading space is dropped at the start of a line.
func writeCollapsed(b *strings.Builder, s string) {
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			prevSpace = true
			continue
		}
		if prevSpace {
			cur := b.String()
			if cur != "" && !strings.HasSuffix(cur, "\n") && !strings.HasSuffix(cur, " ") {
				b.WriteByte(' ')
			}
			prevSpace = false
		}
		b.WriteRune(r)
	}
	if prevSpace {
		cur := b.String()
		if cur != "" && !strings.HasSuffix(cur, "\n") && !strings.HasSuffix(cur, " ") {
			b.WriteByte(' ')
		}
	}
}

// finishText trims each paragraph and drops empty ones.
func finishText(s string) string {
	var out []string
	for _, p := range strings.Split(s, "\n\n") {
		lines := strings.Split(p, "\n")
		for i, ln := range lines {
			lines[i] = strings.TrimRight(ln, " \t")
		}
		p = strings.TrimSpace(strings.Join(lines, "\n"))
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

func htmlFile(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	title, text, err := HTMLToText(f)
	if err != nil {
		return nil, err
	}
	return &Document{Title: title, Text: text, Format: FormatHTML}, nil
}
