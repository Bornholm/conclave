package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// truncationSeeds are reports shaped like the ones agents write: nested
// objects, arrays of findings, numbers, booleans, escapes and non-ASCII text.
var truncationSeeds = []string{
	`{"schema_version":"1","reviewer":{"id":"r","model":"m"},"summary":"Le délai n'est jamais \"appliqué\".\nVoir ci-dessous.","findings":[{"id":"f1","category":"correctness","severity":"high","confidence":0.85,"file":"internal/app/review.go","start_line":123,"end_line":130,"title":"Timeout ignoré","description":"a\\b","breaking":false},{"id":"f2","severity":"low","confidence":1e-2,"file":"docs/x.md","start_line":7,"end_line":7,"title":"t","verified":true,"note":null}],"questions":[],"verdict":"request_changes"}`,
	`{"reviewer":{"id":"r"},"schema_version":"1","findings":[{"file":"a.go","start_line":-1,"tags":["x","y",["z"]],"meta":{"a":{"b":{}}}}],"verdict":"comment"}`,
	`{"schema_version":"1","answer":"€ 😀 é \t","key_points":["one","two"],"confidence":0.5}`,
	longReport(),
}

// longReport is a report long enough that most of it lies past markerWindow:
// the reports worth salvaging are the long ones, written by a run that used its
// whole budget.
func longReport() string {
	var findings []string
	for i := range 18 {
		findings = append(findings, fmt.Sprintf(`{"id":"f%d","severity":"medium","confidence":0.%d,"file":"internal/app/review.go","start_line":%d,"end_line":%d,"title":"Finding %d","description":"%s"}`,
			i, i%10, 100+i, 110+i, i, strings.Repeat("détail ", 20)))
	}
	return `{"schema_version":"1","reviewer":{"id":"r"},"summary":"s","findings":[` + strings.Join(findings, ",") + `],"verdict":"comment"}`
}

// TestExtractTruncatedJSONRecoversPastTheMarkerWindow: the window bounds where
// the marker is looked for, not how far the report may go. A cut deep inside a
// long report is repaired, not refused.
func TestExtractTruncatedJSONRecoversPastTheMarkerWindow(t *testing.T) {
	doc := longReport()
	if len(doc) < markerWindow+1024 {
		t.Fatalf("the seed must reach past the window: %d bytes", len(doc))
	}
	cut := strings.LastIndex(doc, "détail ") + 3
	out, err := ExtractTruncatedJSON([]byte(doc[:cut]), "schema_version")
	if err != nil {
		t.Fatalf("a cut %d bytes in must be repaired: %v", cut, err)
	}
	var obj struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(out, &obj); err != nil || len(obj.Findings) != 18 {
		t.Errorf("the findings before the cut must all be kept: %d (%v)", len(obj.Findings), err)
	}
}

// TestExtractTruncatedJSONNeverInvents cuts every seed at every byte and checks
// the repair against the original: whatever it returns must be the beginning of
// that report and nothing else.
func TestExtractTruncatedJSONNeverInvents(t *testing.T) {
	for _, seed := range truncationSeeds {
		for cut := 1; cut < len(seed); cut++ {
			if err := checkTruncation([]byte(seed), cut); err != nil {
				t.Fatalf("cut at %d of %s: %v", cut, seed, err)
			}
		}
	}
}

// FuzzExtractTruncatedJSON explores reports and cut points beyond the seeds.
// Run it with: go test ./internal/agent -run '^$' -fuzz FuzzExtractTruncatedJSON
func FuzzExtractTruncatedJSON(f *testing.F) {
	for _, seed := range truncationSeeds {
		f.Add([]byte(seed), uint(len(seed)/2))
	}
	f.Fuzz(func(t *testing.T, doc []byte, cut uint) {
		var obj map[string]json.RawMessage
		if len(doc) < 2 || json.Unmarshal(doc, &obj) != nil {
			t.Skip()
		}
		// A duplicate key keeps its last value, so the beginning of the
		// document is not the beginning of what it decodes to.
		if _, ok := obj["schema_version"]; !ok || hasDuplicateKeys(doc) {
			t.Skip()
		}
		if err := checkTruncation(doc, 1+int(cut%uint(len(doc)-1))); err != nil {
			t.Fatalf("cut at %d of %s: %v", cut, doc, err)
		}
	})
}

// checkTruncation repairs doc[:cut] and compares the result with doc.
func checkTruncation(doc []byte, cut int) error {
	out, err := ExtractTruncatedJSON(doc[:cut], "schema_version")
	if err != nil {
		// Refusing is always allowed: the property is about what is returned.
		return nil
	}
	got, err := decodeNumbers(out)
	if err != nil {
		return fmt.Errorf("the repair does not parse: %v: %s", err, out)
	}
	want, err := decodeNumbers(doc)
	if err != nil {
		return err
	}
	if m, ok := got.(map[string]any); !ok || m["schema_version"] == nil {
		return fmt.Errorf("the repair lost the marker: %s", out)
	}
	return isPrefixOf(got, want, "$")
}

// hasDuplicateKeys reports whether any object in doc repeats a key.
func hasDuplicateKeys(doc []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(doc))
	// Each open container: the keys seen so far, or nil for an array.
	var stack []map[string]bool
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, map[string]bool{})
			expectKey = true
			continue
		case json.Delim('['):
			stack = append(stack, nil)
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		default:
			if k, ok := tok.(string); ok && expectKey {
				if stack[len(stack)-1][k] {
					return true
				}
				stack[len(stack)-1][k] = true
				expectKey = false
				continue
			}
		}
		// After a value, the next token of an object is a key.
		expectKey = len(stack) > 0 && stack[len(stack)-1] != nil
	}
}

func decodeNumbers(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// isPrefixOf reports, as an error, where got stops being the beginning of want:
// a key or an element want does not have, a scalar that differs, a string that
// is not a prefix of the original, or a number that is not the original number.
func isPrefixOf(got, want any, path string) error {
	switch g := got.(type) {
	case map[string]any:
		w, ok := want.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: an object where the report has %T", path, want)
		}
		for k, v := range g {
			wv, ok := w[k]
			if !ok {
				return fmt.Errorf("%s: invented key %q", path, k)
			}
			if err := isPrefixOf(v, wv, path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		w, ok := want.([]any)
		if !ok || len(g) > len(w) {
			return fmt.Errorf("%s: an array the report does not have", path)
		}
		for i := range g {
			if err := isPrefixOf(g[i], w[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case string:
		w, ok := want.(string)
		if !ok || !strings.HasPrefix(w, g) {
			return fmt.Errorf("%s: %q is not the beginning of %q", path, g, want)
		}
	default:
		// Numbers, booleans and null are never repaired: a cut one is refused,
		// so what remains is the original value or nothing.
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("%s: %v where the report has %v", path, got, want)
		}
	}
	return nil
}
