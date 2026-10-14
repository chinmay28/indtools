// Package records reads pre-made passages from JSON Lines, for corpora
// that were already split by another tool (one section or paragraph per
// line) and must keep their own identifiers.
//
// Contract: each non-blank line is a JSON object. "text" (string) is
// required and must not be blank. "id" (string or number) becomes
// Record.Ref. "meta" (object) and every other top-level field with a
// string, number or boolean value become Record.Meta, stringified;
// arrays and nested objects elsewhere are ignored. Records are returned in
// file order. A malformed line, a missing or blank "text", or a non-scalar
// "id" is an error naming the line number.
package records

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Record is one passage.
type Record struct {
	// Ref is the caller's identifier for the passage ("" if none).
	Ref string
	// Text is the passage text, untouched.
	Text string
	// Meta holds descriptive fields (title, author, date, ...).
	Meta map[string]string
}

// Parse reads every record from r.
func Parse(r io.Reader) ([]Record, error) {
	var out []Record
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		rec, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseLine(line string) (Record, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return Record{}, fmt.Errorf("not a JSON object: %w", err)
	}
	var rec Record
	raw, ok := obj["text"]
	if !ok {
		return Record{}, fmt.Errorf(`missing "text"`)
	}
	if err := json.Unmarshal(raw, &rec.Text); err != nil || strings.TrimSpace(rec.Text) == "" {
		return Record{}, fmt.Errorf(`"text" must be a non-blank string`)
	}
	if raw, ok := obj["id"]; ok {
		s, ok := scalar(raw)
		if !ok {
			return Record{}, fmt.Errorf(`"id" must be a string or number`)
		}
		rec.Ref = s
	}
	rec.Meta = map[string]string{}
	if raw, ok := obj["meta"]; ok {
		var meta map[string]json.RawMessage
		if err := json.Unmarshal(raw, &meta); err != nil {
			return Record{}, fmt.Errorf(`"meta" must be an object`)
		}
		for k, v := range meta {
			if s, ok := scalar(v); ok {
				rec.Meta[k] = s
			}
		}
	}
	for k, v := range obj {
		if k == "text" || k == "id" || k == "meta" {
			continue
		}
		if s, ok := scalar(v); ok {
			if _, taken := rec.Meta[k]; !taken {
				rec.Meta[k] = s
			}
		}
	}
	if len(rec.Meta) == 0 {
		rec.Meta = nil
	}
	return rec, nil
}

// scalar renders a JSON string, number or bool as text. Empty strings and
// null are dropped (ok=false), as are arrays and objects.
func scalar(raw json.RawMessage) (string, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, x != ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// FormatMeta renders meta as "key: value" lines in key order, for
// inclusion in a prompt. It returns "" for empty meta.
func FormatMeta(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(meta[k]))
	}
	return b.String()
}
