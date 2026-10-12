// Package export writes question/answer pairs in common fine-tuning formats.
//
// Formats:
//
//	alpaca    {"instruction","input","output"}; the chunk goes in "input"
//	          when IncludeContext is set, else "input" is "".
//	sharegpt  {"conversations":[{"from":"system"|"human"|"gpt","value"}]}
//	messages  {"messages":[{"role":"system"|"user"|"assistant","content"}]}
//	          (the OpenAI fine-tuning / chat-template convention)
//	text      {"text": ...} one record per chunk, for continued pretraining
//
// Contract: JSONL output has exactly one JSON object per line; JSON output
// is a single array. Records are written in input order. Reasoning, when
// present and requested, is prepended to the answer inside <think> tags,
// the de-facto convention for distilled chain-of-thought data.
package export

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Format names.
const (
	Alpaca   = "alpaca"
	ShareGPT = "sharegpt"
	Messages = "messages"
	Text     = "text"
)

// Formats lists the supported pair formats (Text is chunk-based).
var Formats = []string{Alpaca, ShareGPT, Messages, Text}

// Pair is one training example.
type Pair struct {
	Question  string
	Answer    string
	Reasoning string
	Context   string
}

// Options controls the output.
type Options struct {
	Format string
	// JSONArray writes one JSON array instead of JSON Lines.
	JSONArray bool
	// IncludeContext adds the source chunk to each example (alpaca "input",
	// or prepended to the user turn in chat formats).
	IncludeContext bool
	// IncludeReasoning emits Reasoning inside <think> tags before the answer.
	IncludeReasoning bool
	// System is an optional system prompt for the chat formats.
	System string
}

// ValidFormat reports whether f is one of Formats.
func ValidFormat(f string) bool {
	for _, k := range Formats {
		if k == f {
			return true
		}
	}
	return false
}

// WritePairs encodes pairs to w. The format is validated even when pairs
// is empty.
func WritePairs(w io.Writer, pairs []Pair, o Options) error {
	if !ValidFormat(o.Format) || o.Format == Text {
		return fmt.Errorf("export: unknown pair format %q (want one of %s)", o.Format, strings.Join(Formats[:len(Formats)-1], ", "))
	}
	var records []any
	for _, p := range pairs {
		r, err := record(p, o)
		if err != nil {
			return err
		}
		records = append(records, r)
	}
	return write(w, records, o.JSONArray)
}

// WriteText encodes chunk texts as {"text": ...} records.
func WriteText(w io.Writer, texts []string, jsonArray bool) error {
	records := make([]any, len(texts))
	for i, t := range texts {
		records[i] = map[string]string{"text": t}
	}
	return write(w, records, jsonArray)
}

func record(p Pair, o Options) (any, error) {
	answer := p.Answer
	if o.IncludeReasoning && strings.TrimSpace(p.Reasoning) != "" {
		answer = "<think>\n" + strings.TrimSpace(p.Reasoning) + "\n</think>\n" + p.Answer
	}
	userTurn := p.Question
	if o.IncludeContext && p.Context != "" {
		userTurn = "Context:\n" + p.Context + "\n\nQuestion: " + p.Question
	}
	switch o.Format {
	case Alpaca:
		input := ""
		if o.IncludeContext {
			input = p.Context
		}
		rec := map[string]string{"instruction": p.Question, "input": input, "output": answer}
		if o.System != "" {
			rec["system"] = o.System
		}
		return rec, nil
	case ShareGPT:
		var conv []map[string]string
		if o.System != "" {
			conv = append(conv, map[string]string{"from": "system", "value": o.System})
		}
		conv = append(conv,
			map[string]string{"from": "human", "value": userTurn},
			map[string]string{"from": "gpt", "value": answer})
		return map[string]any{"conversations": conv}, nil
	case Messages:
		var msgs []map[string]string
		if o.System != "" {
			msgs = append(msgs, map[string]string{"role": "system", "content": o.System})
		}
		msgs = append(msgs,
			map[string]string{"role": "user", "content": userTurn},
			map[string]string{"role": "assistant", "content": answer})
		return map[string]any{"messages": msgs}, nil
	default:
		return nil, fmt.Errorf("export: unknown format %q (want one of %s)", o.Format, strings.Join(Formats, ", "))
	}
}

func write(w io.Writer, records []any, jsonArray bool) error {
	bw := bufio.NewWriter(w)
	if jsonArray {
		enc := json.NewEncoder(bw)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if records == nil {
			records = []any{}
		}
		if err := enc.Encode(records); err != nil {
			return err
		}
		return bw.Flush()
	}
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return bw.Flush()
}
