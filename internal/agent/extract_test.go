package agent

import (
	"bytes"
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

// TestExtractTruncatedJSONClosesACutReport: a run stopped while the model was
// writing ends mid-string. Closing it recovers what had been written, and only
// when the text makes that unambiguous.
func TestExtractTruncatedJSONClosesACutReport(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		// A document that already closes is not a truncation: reporting it as
		// repaired is what labelled every complete report as cut off.
		{"complete", `{"schema_version":"1","findings":[]}`, false},
		{"cut in string", `{"schema_version":"1","summary":"half a senten`, true},
		{"cut in nested array", `{"schema_version":"1","findings":[{"id":"f1","title":"hal`, true},
		// The member being written is dropped, leaving what was complete.
		{"cut in key", `{"schema_version":"1","summ`, true},
		{"cut in escape", `{"schema_version":"1","summary":"a\`, false},
		{"cut in number", `{"schema_version":"1","reviewer":{"id":"r"},"n":12`, false},
		{"without the marker", `{"other":"x","summary":"half`, false},
		{"no object at all", `just prose`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := ExtractTruncatedJSON([]byte(c.in), "schema_version")
			if c.ok && err != nil {
				t.Fatalf("expected a recovered report, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("expected a refusal, got %s", out)
			}
		})
	}
}

// TestExtractTruncatedJSONDropsAnIncompleteMember: a report cut after a key and
// its colon cannot be closed by adding brackets alone, since that would leave
// `"note":}`. The incomplete member goes and the complete ones stay.
func TestExtractTruncatedJSONDropsAnIncompleteMember(t *testing.T) {
	in := `{"schema_version":"1","summary":"ok","references":[{"source":"main.go","line":1,"note":`
	out, err := ExtractTruncatedJSON([]byte(in), "schema_version")
	if err != nil {
		t.Fatalf("expected a recovered report, got %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("recovered output does not parse: %v (%s)", err, out)
	}
	if obj["schema_version"] != "1" || obj["summary"] != "ok" {
		t.Errorf("complete members were lost: %s", out)
	}
	// The reference keeps the fields it had finished; only the member cut in
	// the middle is gone.
	refs, ok := obj["references"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("the completed part of the reference should stay: %s", out)
	}
	ref := refs[0].(map[string]any)
	if ref["source"] != "main.go" || ref["line"] != float64(1) {
		t.Errorf("reference mangled: %v", ref)
	}
	if _, has := ref["note"]; has {
		t.Errorf("the member cut mid-value must not appear: %v", ref)
	}

	// A member cut in the middle of its string keeps everything before it and
	// closes the string it was writing.
	out, err = ExtractTruncatedJSON([]byte(`{"schema_version":"1","summary":"a","note":"hal`), "schema_version")
	if err != nil {
		t.Fatalf("expected a recovered report, got %v", err)
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("recovered output does not parse: %v (%s)", err, out)
	}
	if obj["note"] != "hal" {
		t.Errorf("the partially written value should be kept as far as it went: %s", out)
	}
}

// TestExtractTruncatedJSONIsAgentAgnostic: a report printed by any agent and cut
// short by the timeout is repairable, whatever produced it. Pi streams deltas,
// opencode emits text parts, Claude Code wraps its answer in an envelope and
// plain agents print one document. The repair only looks at the report.
func TestExtractTruncatedJSONIsAgentAgnostic(t *testing.T) {
	const cut = `{\"schema_version\":\"1\",\"reviewer\":{\"id\":\"r1\"},\"summary\":\"s\",\"findings\":[],\"verd`
	cases := map[string]string{
		// A bare document, as an agent printing its answer directly leaves it.
		"plain stdout": `I reviewed the diff.

{"schema_version":"1","reviewer":{"id":"r1"},"summary":"s","findings":[],"verd`,
		// Claude Code --output-format json: the answer lives in "result".
		"claude envelope": `{"type":"result","subtype":"success","result":"` + cut + `"}`,
		// opencode run --format json: the answer is a text part in an event.
		"opencode event": `{"type":"text","part":{"type":"text","text":"Here: ` + cut + `"}}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := ExtractTruncatedJSON([]byte(in), "schema_version")
			if err != nil {
				t.Fatalf("a cut report must be repairable whatever wrote it: %v", err)
			}
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Fatalf("repaired report does not parse: %v (%s)", err, out)
			}
			if obj["schema_version"] != "1" {
				t.Errorf("marker lost: %s", out)
			}
		})
	}
}

// TestEnsureVerdict: the schema puts the verdict last, so a report cut
// mid-write has lost it while keeping its findings. A partial report must never
// be able to approve, so the verdict added is the conservative one.
func TestEnsureVerdict(t *testing.T) {
	cut := []byte(`{"schema_version":"1","findings":[{"id":"f1"}]}`)
	out, added := EnsureVerdict(cut)
	if !added {
		t.Fatal("a report without a verdict must get one")
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	if obj["verdict"] != "comment" {
		t.Errorf("verdict = %v, want comment: a partial report must not approve", obj["verdict"])
	}
	// The findings survive.
	if _, ok := obj["findings"]; !ok {
		t.Errorf("the repair dropped the findings: %s", out)
	}

	// A report that has one keeps it.
	complete := []byte(`{"schema_version":"1","verdict":"request_changes"}`)
	if _, added := EnsureVerdict(complete); added {
		t.Error("an existing verdict must not be replaced")
	}
}

// TestExtractTruncatedJSONKilledInsideAnEnvelope: Claude Code and opencode both
// put the answer in a JSON string. A timeout kills the process mid-value, so
// that string is itself unterminated - the case a real kill produces, unlike a
// fixture that politely closes its envelope.
func TestExtractTruncatedJSONKilledInsideAnEnvelope(t *testing.T) {
	cases := map[string]string{
		"claude result": `{"type":"result","subtype":"success","result":"{\"schema_version\":\"1\",\"summary\":\"hal`,
		"opencode part": `{"type":"text","part":{"type":"text","text":"{\"schema_version\":\"1\",\"summary\":\"hal`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := ExtractTruncatedJSON([]byte(in), "schema_version")
			if err != nil {
				t.Fatalf("an envelope killed mid-value holds a report up to the cut: %v", err)
			}
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Fatalf("recovered report does not parse: %v (%s)", err, out)
			}
			if obj["schema_version"] != "1" {
				t.Errorf("marker lost: %s", out)
			}
		})
	}
}

// TestExtractTruncatedJSONIgnoresTrailingSpaceWhenJudgingANumber: adapters append
// a newline to what they keep, and a cut that landed inside a number must still
// be refused once that byte is there.
func TestExtractTruncatedJSONIgnoresTrailingSpaceWhenJudgingANumber(t *testing.T) {
	for _, in := range []string{
		`{"schema_version":"1","reviewer":{"id":"r"},"n":12`,
		"{\"schema_version\":\"1\",\"reviewer\":{\"id\":\"r\"},\"n\":12\n",
	} {
		if out, err := ExtractTruncatedJSON([]byte(in), "schema_version"); err == nil {
			t.Errorf("a number cut in half must be refused, got %s", out)
		}
	}
}

// TestExtractTruncatedJSONLeavesACompleteReportAlone: returning a complete
// document as "repaired" made every successful run warn that its report had been
// cut off. Only a document that does not close is a truncation.
func TestExtractTruncatedJSONLeavesACompleteReportAlone(t *testing.T) {
	complete := []byte(`{"schema_version":"1","summary":"s","findings":[],"verdict":"approve"}`)
	if out, err := ExtractTruncatedJSON(complete, "schema_version"); err == nil {
		t.Errorf("a complete report must not be reported as repaired, got %s", out)
	}
	// A complete report inside a complete envelope is not one either.
	env := []byte(`{"type":"result","result":"{\"schema_version\":\"1\",\"summary\":\"s\"}"}`)
	if out, err := ExtractTruncatedJSON(env, "schema_version"); err == nil {
		t.Errorf("a complete envelope must not be reported as repaired, got %s", out)
	}
}

// TestExtractTruncatedJSONIsBoundedOnALargeStream: the repair runs from adapt()
// on every run, including the ones with no report at all, where its input is the
// whole event stream. Walking every brace there was quadratic and took minutes on
// 12 MB; it now looks only at the outermost and the last object.
func TestExtractTruncatedJSONIsBoundedOnALargeStream(t *testing.T) {
	// 4 MB of stream-like text with many braces and no report.
	var b bytes.Buffer
	for b.Len() < 4<<20 {
		b.WriteString(`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"{ not a report "}}` + "\n")
	}
	start := time.Now()
	if _, err := ExtractTruncatedJSON(b.Bytes(), "schema_version"); err == nil {
		t.Fatal("a stream without a report must not be repairable")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the repair took %s on %d bytes: it is scanning every brace again", d, b.Len())
	}
}

// TestExtractTruncatedJSONDropsACutMemberAfterAColon: the member being written
// is dropped whichever position it holds. A cut after "key": must not leave a
// document that closes as "key":}, which no parser accepts.
func TestExtractTruncatedJSONDropsACutMemberAfterAColon(t *testing.T) {
	cases := map[string]string{
		"first member":      `{"schema_version":"1","note":`,
		"after a number":    `{"schema_version":"1","a":1,"note":`,
		"after a string":    `{"schema_version":"1","s":"x","note":`,
		"after an object":   `{"schema_version":"1","o":{"k":1},"note":`,
		"after an array":    `{"schema_version":"1","arr":[1],"note":`,
		"after a true":      `{"schema_version":"1","t":true,"note":`,
		"after a null":      `{"schema_version":"1","t":null,"note":`,
		"nested afterwards": `{"schema_version":"1","o":{"k":1,"note":`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := ExtractTruncatedJSON([]byte(in), "schema_version")
			if err != nil {
				t.Fatalf("the report must survive losing one member: %v", err)
			}
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Fatalf("recovered output does not parse: %v (%s)", err, out)
			}
			if obj["schema_version"] != "1" {
				t.Errorf("marker lost: %s", out)
			}
		})
	}
}
