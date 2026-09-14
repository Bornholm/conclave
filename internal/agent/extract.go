// Package agent runs one configured agent and turns its output into a report.
package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrNoJSON is returned when no candidate JSON object is found in the output.
var ErrNoJSON = errors.New("no JSON object found in agent output")

// fenceRe captures the body of a Markdown code fence. The body is processed
// like any other text, so a nested object inside it is still found.
var fenceRe = regexp.MustCompile("(?s)```(?:json)?[ \\t]*\\r?\\n(.*?)```")

// ExtractJSON finds the JSON object produced by an agent in its raw stdout.
// It accepts, in order: a raw object; a Claude Code envelope (object with a
// "structured_output" object or a "result" string, itself processed
// recursively); NDJSON lines; a fenced ```json block; and finally the first
// balanced object containing the marker key. The marker is a key that must be
// present at the top level (e.g. "schema_version").
//
// Candidates that fail to parse are retried on a repaired copy, because agents
// regularly emit an unescaped backslash or a literal newline inside a string.
// When such a candidate carries the marker but stays unparsable, the returned
// error describes the syntax error instead of claiming nothing was found.
func ExtractJSON(raw []byte, marker string) ([]byte, error) {
	return extract(bytes.TrimSpace(raw), marker, 0)
}

func extract(raw []byte, marker string, depth int) ([]byte, error) {
	if depth > 4 || len(raw) == 0 {
		return nil, ErrNoJSON
	}
	var malformed error
	if obj, fixed, ok := parseObject(raw); ok {
		if _, has := obj[marker]; has {
			return fixed, nil
		}
		if so, ok := obj["structured_output"]; ok && len(so) > 0 && so[0] == '{' {
			if out, err := extract(so, marker, depth+1); err == nil {
				return out, nil
			} else {
				malformed = keepMalformed(malformed, err)
			}
		}
		if res, ok := obj["result"]; ok {
			var s string
			if json.Unmarshal(res, &s) == nil {
				if out, err := extract([]byte(strings.TrimSpace(s)), marker, depth+1); err == nil {
					return out, nil
				} else {
					malformed = keepMalformed(malformed, err)
				}
			}
		}
	}
	// NDJSON: try each line.
	if bytes.Count(raw, []byte("\n")) > 0 {
		for _, line := range bytes.Split(raw, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) > 1 && line[0] == '{' {
				if _, _, ok := parseObject(line); ok {
					if out, err := extract(line, marker, depth+1); err == nil {
						return out, nil
					} else {
						malformed = keepMalformed(malformed, err)
					}
				}
			}
		}
	}
	// Fenced block.
	for _, m := range fenceRe.FindAllSubmatch(raw, -1) {
		body := bytes.TrimSpace(m[1])
		if len(body) == 0 || bytes.Equal(body, raw) {
			continue
		}
		if out, err := extract(body, marker, depth+1); err == nil {
			return out, nil
		} else {
			malformed = keepMalformed(malformed, err)
		}
	}
	// Balanced-brace scan.
	for start := bytes.IndexByte(raw, '{'); start >= 0 && start < len(raw); {
		if end := matchBrace(raw, start); end >= 0 {
			candidate := raw[start : end+1]
			if obj, fixed, ok := parseObject(candidate); ok {
				if _, has := obj[marker]; has {
					return fixed, nil
				}
			} else if bytes.Contains(candidate, []byte(`"`+marker+`"`)) {
				malformed = keepMalformed(malformed, syntaxError(candidate))
			}
		}
		// A brace that does not close — or closes on a candidate we rejected —
		// must not stop the scan: the report may start at a later brace.
		next := bytes.IndexByte(raw[start+1:], '{')
		if next < 0 {
			break
		}
		start += 1 + next
	}
	if malformed != nil {
		return nil, malformed
	}
	return nil, ErrNoJSON
}

// keepMalformed prefers a syntax error over the generic "not found" one, so
// the caller learns that a report was present but unparsable.
func keepMalformed(current, err error) error {
	if current != nil || errors.Is(err, ErrNoJSON) {
		return current
	}
	return err
}

// syntaxError describes why a candidate carrying the marker did not parse.
func syntaxError(candidate []byte) error {
	var obj map[string]json.RawMessage
	err := json.Unmarshal(candidate, &obj)
	if err == nil {
		return ErrNoJSON
	}
	return fmt.Errorf("malformed JSON report in agent output: %w", err)
}

// parseObject parses raw as a JSON object. On failure it retries once on a
// repaired copy and returns the bytes that actually parsed.
func parseObject(raw []byte) (map[string]json.RawMessage, []byte, bool) {
	if obj, ok := asObject(raw); ok {
		return obj, raw, true
	}
	if fixed, changed := repairJSON(raw); changed {
		if obj, ok := asObject(fixed); ok {
			return obj, fixed, true
		}
	}
	return nil, nil, false
}

func asObject(raw []byte) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// repairJSON escapes the two things agents get wrong inside JSON strings: a
// backslash that does not start a valid escape (`OA\Property` in PHP or Java
// namespaces, `\d` in a regexp) and a literal control character. It reports
// whether anything was changed, so the strict parse stays authoritative.
func repairJSON(raw []byte) ([]byte, bool) {
	var out bytes.Buffer
	out.Grow(len(raw) + len(raw)/16)
	changed, inStr := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inStr {
			if c == '"' {
				inStr = true
			}
			out.WriteByte(c)
			continue
		}
		switch {
		case c == '"':
			inStr = false
			out.WriteByte(c)
		case c == '\\':
			if i+1 < len(raw) && isValidEscape(raw, i+1) {
				out.WriteByte(c)
				out.WriteByte(raw[i+1])
				i++
				continue
			}
			out.WriteString(`\\`)
			changed = true
		case c < 0x20:
			out.WriteString(controlEscape(c))
			changed = true
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes(), changed
}

func isValidEscape(raw []byte, i int) bool {
	switch raw[i] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	case 'u':
		if i+4 >= len(raw) {
			return false
		}
		for _, h := range raw[i+1 : i+5] {
			if !isHex(h) {
				return false
			}
		}
		return true
	}
	return false
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func controlEscape(c byte) string {
	switch c {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	}
	return fmt.Sprintf(`\u%04x`, c)
}

// matchBrace returns the index of the brace closing the one at start, honoring
// JSON strings, or -1.
func matchBrace(b []byte, start int) int {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
