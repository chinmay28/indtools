package records

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := `{"id":"s-0001","text":"First passage.","volume":1,"title":"Karma-Yoga","tags":["a","b"],"empty":"","meta":{"date":"1894-05-28","nested":{"x":1}}}

{"id": 42, "text": "  Second.  "}
{"text":"Third, no id."}
`
	recs, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.Ref != "s-0001" || r.Text != "First passage." {
		t.Fatalf("%+v", r)
	}
	if r.Meta["volume"] != "1" || r.Meta["title"] != "Karma-Yoga" || r.Meta["date"] != "1894-05-28" {
		t.Fatalf("meta %v", r.Meta)
	}
	for _, k := range []string{"tags", "nested", "empty", "text", "id", "meta"} {
		if _, ok := r.Meta[k]; ok {
			t.Errorf("meta should not contain %q: %v", k, r.Meta)
		}
	}
	if recs[1].Ref != "42" || recs[1].Text != "  Second.  " || recs[1].Meta != nil {
		t.Fatalf("%+v", recs[1])
	}
	if recs[2].Ref != "" || recs[2].Text != "Third, no id." {
		t.Fatalf("%+v", recs[2])
	}
	if got := FormatMeta(r.Meta); got != "date: 1894-05-28\ntitle: Karma-Yoga\nvolume: 1" {
		t.Fatalf("FormatMeta = %q", got)
	}
	if FormatMeta(nil) != "" {
		t.Fatal("empty meta should format to empty string")
	}
}

func TestParseMetaWinsOverTopLevel(t *testing.T) {
	recs, err := Parse(strings.NewReader(`{"text":"t","title":"outer","meta":{"title":"inner"}}`))
	if err != nil || recs[0].Meta["title"] != "inner" {
		t.Fatalf("%v %v", recs, err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		`not json`:                             "line 1: not a JSON object",
		`["array"]`:                            "line 1: not a JSON object",
		`{"id":"x"}`:                           `line 1: missing "text"`,
		`{"text":"   "}`:                       `line 1: "text" must be a non-blank string`,
		`{"text":5}`:                           `line 1: "text" must be a non-blank string`,
		`{"text":"ok","id":[1]}`:               `line 1: "id" must be a string or number`,
		`{"text":"ok","meta":"no"}`:            `line 1: "meta" must be an object`,
		"{\"text\":\"ok\"}\n\n{\"text\":\"\"}": `line 3: "text" must be a non-blank string`,
	}
	for in, want := range cases {
		_, err := Parse(strings.NewReader(in))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Parse(%q) err = %v, want prefix %q", in, err, want)
		}
	}
	if recs, err := Parse(strings.NewReader("\n  \n")); err != nil || recs != nil {
		t.Fatalf("blank input: %v %v", recs, err)
	}
}
