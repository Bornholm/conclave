package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	want := `{"schema_version":"1","x":1}`
	cases := map[string]string{
		"raw":        want,
		"claude":     `{"type":"result","result":"Sure!\n` + "```json\\n" + `{\"schema_version\":\"1\",\"x\":1}` + "\\n```" + `"}`,
		"structured": `{"type":"result","result":"text","structured_output":` + want + `}`,
		"ndjson":     `{"type":"step_start"}` + "\n" + `{"type":"text","part":"x"}` + "\n" + want + "\n",
		"fenced":     "Here you go:\n```json\n" + want + "\n```\nDone.",
		"prose":      "The report {\"nested\":{\"a\":\"}\"}} then " + want + " end",
		"escaped":    `noise {"schema_version":"1","x":1,"s":"a \"quoted\" }"} tail`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := ExtractJSON([]byte(in), "schema_version")
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil || m["schema_version"] != "1" {
				t.Errorf("got %s", out)
			}
		})
	}
	for name, in := range map[string]string{"empty": "", "prose": "nothing here", "broken": "{this is not", "other": `{"foo":1}`} {
		if _, err := ExtractJSON([]byte(in), "schema_version"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// TestExtractJSONTolerance covers the malformed output seen in the wild:
// agents that leave a backslash unescaped inside a JSON string (PHP or Java
// namespaces, regexps), that fence a nested object, or that prefix the report
// with prose containing an unbalanced brace.
func TestExtractJSONTolerance(t *testing.T) {
	cases := map[string]struct{ in, summary string }{
		"unescaped backslash": {
			in:      `Done.` + "\n\n" + `{"schema_version":"1","summary":"the OA\Property annotation"}`,
			summary: `the OA\Property annotation`,
		},
		"fenced nested object": {
			in:      "```json\n{\n  \"schema_version\": \"1\",\n  \"reviewer\": {\"id\": \"r\"},\n  \"summary\": \"nested\"\n}\n```",
			summary: "nested",
		},
		"unbalanced brace in prose": {
			in:      `A dangling { brace and a "quote, then ` + "\n" + `{"schema_version":"1","summary":"after noise"}`,
			summary: "after noise",
		},
		"literal newline in string": {
			in:      "{\"schema_version\":\"1\",\"summary\":\"two\nlines\"}",
			summary: "two\nlines",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := ExtractJSON([]byte(tc.in), "schema_version")
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				SchemaVersion string `json:"schema_version"`
				Summary       string `json:"summary"`
			}
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("decode %s: %v", out, err)
			}
			if m.SchemaVersion != "1" || m.Summary != tc.summary {
				t.Errorf("got %+v, want summary %q", m, tc.summary)
			}
		})
	}
}

// TestExtractJSONMalformedError checks that a report that stays unparsable is
// reported as malformed rather than as missing.
func TestExtractJSONMalformedError(t *testing.T) {
	_, err := ExtractJSON([]byte(`{"schema_version":"1","findings":[,]}`), "schema_version")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrNoJSON) || !strings.Contains(err.Error(), "malformed JSON report") {
		t.Fatalf("got %v", err)
	}
}
