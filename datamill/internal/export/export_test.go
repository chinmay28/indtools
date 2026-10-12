package export

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var sample = []Pair{
	{Question: "What is X?", Answer: "X is Y.", Reasoning: "Because Z.", Context: "X equals Y, <b>said</b> Z."},
	{Question: "Second?", Answer: "Yes."},
}

func lines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(s), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("line %q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func TestAlpaca(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePairs(&buf, sample, Options{Format: Alpaca}); err != nil {
		t.Fatal(err)
	}
	recs := lines(t, buf.String())
	if len(recs) != 2 || recs[0]["instruction"] != "What is X?" || recs[0]["input"] != "" || recs[0]["output"] != "X is Y." {
		t.Fatalf("%v", recs)
	}
	buf.Reset()
	WritePairs(&buf, sample, Options{Format: Alpaca, IncludeContext: true, IncludeReasoning: true, System: "S"})
	recs = lines(t, buf.String())
	if recs[0]["input"] != sample[0].Context || recs[0]["system"] != "S" {
		t.Fatalf("%v", recs[0])
	}
	if recs[0]["output"] != "<think>\nBecause Z.\n</think>\nX is Y." {
		t.Fatalf("output %q", recs[0]["output"])
	}
	if recs[1]["output"] != "Yes." { // no reasoning -> no think block
		t.Fatalf("output %q", recs[1]["output"])
	}
	if strings.Contains(buf.String(), "\\"+"u003c") {
		t.Fatal("HTML should not be escaped")
	}
}

func TestShareGPTAndMessages(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePairs(&buf, sample[:1], Options{Format: ShareGPT, System: "sys", IncludeContext: true}); err != nil {
		t.Fatal(err)
	}
	rec := lines(t, buf.String())[0]
	conv := rec["conversations"].([]any)
	if len(conv) != 3 || conv[0].(map[string]any)["from"] != "system" || conv[1].(map[string]any)["from"] != "human" || conv[2].(map[string]any)["value"] != "X is Y." {
		t.Fatalf("%v", conv)
	}
	if !strings.HasPrefix(conv[1].(map[string]any)["value"].(string), "Context:\n") {
		t.Fatalf("context missing: %v", conv[1])
	}

	buf.Reset()
	if err := WritePairs(&buf, sample[:1], Options{Format: Messages}); err != nil {
		t.Fatal(err)
	}
	rec = lines(t, buf.String())[0]
	msgs := rec["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "user" || msgs[0].(map[string]any)["content"] != "What is X?" || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("%v", msgs)
	}
}

func TestJSONArrayAndEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePairs(&buf, sample, Options{Format: Alpaca, JSONArray: true}); err != nil {
		t.Fatal(err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil || len(arr) != 2 {
		t.Fatalf("%v %d", err, len(arr))
	}
	buf.Reset()
	if err := WritePairs(&buf, nil, Options{Format: Alpaca, JSONArray: true}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Fatalf("empty array: %q", buf.String())
	}
	buf.Reset()
	if err := WritePairs(&buf, nil, Options{Format: Alpaca}); err != nil || buf.Len() != 0 {
		t.Fatalf("empty jsonl: %q %v", buf.String(), err)
	}
}

func TestTextAndUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteText(&buf, []string{"a", "b"}, false); err != nil {
		t.Fatal(err)
	}
	recs := lines(t, buf.String())
	if len(recs) != 2 || recs[1]["text"] != "b" {
		t.Fatalf("%v", recs)
	}
	if err := WritePairs(&buf, sample, Options{Format: "bogus"}); err == nil {
		t.Fatal("expected error")
	}
	if err := WritePairs(&buf, nil, Options{Format: "bogus"}); err == nil {
		t.Fatal("expected error even with no pairs")
	}
	if err := WritePairs(&buf, nil, Options{Format: Text}); err == nil {
		t.Fatal("text is not a pair format")
	}
}
