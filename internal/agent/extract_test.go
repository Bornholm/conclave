package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		"unescaped quotes in string": {
			in:      `{"schema_version":"1","summary":"reads ` + "`" + `if x != "" { ... }` + "`" + ` then stops"}`,
			summary: "reads `if x != \"\" { ... }` then stops",
		},
		// A literal quote is regularly followed by the very characters that
		// close a string, so the repair cannot decide one quote at a time.
		"quoted argument": {
			in:      `{"schema_version":"1","summary":"calls Split(s, ",") here"}`,
			summary: `calls Split(s, ",") here`,
		},
		"map literal": {
			in:      `{"schema_version":"1","summary":"map[string]string{"a": "b"} is wrong"}`,
			summary: `map[string]string{"a": "b"} is wrong`,
		},
		"nested json": {
			in:      `{"schema_version":"1","summary":"payload {"k": "v"} is rejected"}`,
			summary: `payload {"k": "v"} is rejected`,
		},
		"quoted speech": {
			in:      `{"schema_version":"1","summary":"he said "hello", then left"}`,
			summary: `he said "hello", then left`,
		},
		"quotes in array": {
			in:      `{"schema_version":"1","summary":"x","questions":["is "a", b?"]}`,
			summary: "x",
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

// TestExtractJSONQuoteFixerBudget checks that a large report the quote fixer
// cannot resolve is abandoned instead of explored exhaustively.
func TestExtractJSONQuoteFixerBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schema_version":"1","findings":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"evidence":"map[string]string{"a": "b"} and x != "" {","note":"ok"}`)
	}
	b.WriteString(`],"trailing":`) // never closed: unparsable whatever the quotes
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := ExtractJSON([]byte(b.String()), "schema_version"); err == nil {
			t.Error("expected an error")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("quote fixer did not give up")
	}
}

// TestExtractJSONPrefersIntactReport checks the two-pass search: when the
// output holds both a report the agent garbled and one that is well formed,
// the well-formed one wins, wherever each sits in the output.
func TestExtractJSONPrefersIntactReport(t *testing.T) {
	garbled := `{"schema_version":"1","summary":"broken "quote, here"}`
	intact := `{"schema_version":"1","summary":"intact"}`
	for name, in := range map[string]string{
		"garbled first": garbled + "\n" + intact,
		"intact first":  intact + "\n" + garbled,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := ExtractJSON([]byte(in), "schema_version")
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatal(err)
			}
			if m.Summary != "intact" {
				t.Errorf("got %q, want the intact report", m.Summary)
			}
		})
	}
}

// TestExtractJSONDoesNotWeldDocuments guards the riskiest thing the quote
// fixer can do: running a repaired string across the boundary between two
// documents, which would return an object welded out of both. The report must
// come back repaired but whole, with only its own keys.
func TestExtractJSONDoesNotWeldDocuments(t *testing.T) {
	garbled := `{"schema_version":"1","summary":"broken "quote, here"}`
	out, err := ExtractJSON([]byte(garbled+"\n"+`{"type":"event","summary":"other"}`), "schema_version")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if len(m) != 2 || m["summary"] != `broken "quote, here` {
		t.Errorf("got %s", out)
	}
}

// TestExtractJSONFromPiStream replays the failure a user reported: a Pi agent
// whose report quotes Go code and leaves the quotes inside `evidence`
// unescaped, which made the whole review fail with `malformed JSON report in
// agent output: invalid character '"' after object key:value pair`. The fixture
// mirrors the structure of the archived run — an event stream whose last
// assistant message fences the report — and the whole pipeline must recover it
// with every field intact, not merely parse it.
func TestExtractJSONFromPiStream(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "agent", "pi-json-unescaped-quotes.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	ad, err := Adapt("pi-json", raw)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture is only worth keeping while it still reproduces the bug.
	if _, err := extract(ad.Report, "schema_version", 0, false); err == nil {
		t.Fatal("fixture parses strictly: it no longer reproduces the reported failure")
	}
	out, err := ExtractJSON(ad.Report, "schema_version")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var report struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			Title      string `json:"title"`
			Evidence   string `json:"evidence"`
			Suggestion string `json:"suggestion"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if report.Verdict != "approve" || len(report.Findings) != 1 {
		t.Fatalf("got verdict %q and %d findings", report.Verdict, len(report.Findings))
	}
	f := report.Findings[0]
	wantEvidence := "The branch reads `if err == nil && d.JWKSURI != \"\" { ... } else if err == nil { warn }`. When `err != nil`, nothing is done."
	if f.Evidence != wantEvidence {
		t.Errorf("evidence:\n got %q\nwant %q", f.Evidence, wantEvidence)
	}
	// The quotes sit in the middle of the finding, so a repair that truncates
	// the string would silently drop everything after them.
	if f.Suggestion == "" || f.Title == "" {
		t.Errorf("fields after the unescaped quotes were lost: %+v", f)
	}
}
