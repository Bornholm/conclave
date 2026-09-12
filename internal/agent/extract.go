// Package agent runs one configured agent and turns its output into a report.
package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// ErrNoJSON is returned when no candidate JSON object is found in the output.
var ErrNoJSON = errors.New("no JSON object found in agent output")

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// ExtractJSON finds the JSON object produced by an agent in its raw stdout.
// It accepts, in order: a raw object; a Claude Code envelope (object with a
// "structured_output" object or a "result" string, itself processed
// recursively); NDJSON lines; a fenced ```json block; and finally the first
// balanced object containing the marker key. The marker is a key that must be
// present at the top level (e.g. "schema_version").
func ExtractJSON(raw []byte, marker string) ([]byte, error) {
	return extract(bytes.TrimSpace(raw), marker, 0)
}

func extract(raw []byte, marker string, depth int) ([]byte, error) {
	if depth > 4 || len(raw) == 0 {
		return nil, ErrNoJSON
	}
	if obj, ok := asObject(raw); ok {
		if _, has := obj[marker]; has {
			return raw, nil
		}
		if so, ok := obj["structured_output"]; ok && len(so) > 0 && so[0] == '{' {
			if out, err := extract(so, marker, depth+1); err == nil {
				return out, nil
			}
		}
		if res, ok := obj["result"]; ok {
			var s string
			if json.Unmarshal(res, &s) == nil {
				if out, err := extract([]byte(strings.TrimSpace(s)), marker, depth+1); err == nil {
					return out, nil
				}
			}
		}
	}
	// NDJSON: try each line.
	if bytes.Count(raw, []byte("\n")) > 0 {
		for _, line := range bytes.Split(raw, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) > 1 && line[0] == '{' {
				if _, ok := asObject(line); ok {
					if out, err := extract(line, marker, depth+1); err == nil {
						return out, nil
					}
				}
			}
		}
	}
	// Fenced block.
	for _, m := range fenceRe.FindAllSubmatch(raw, -1) {
		if out, err := extract(m[1], marker, depth+1); err == nil {
			return out, nil
		}
	}
	// Balanced-brace scan.
	for start := bytes.IndexByte(raw, '{'); start >= 0 && start < len(raw); {
		end := matchBrace(raw, start)
		if end < 0 {
			break
		}
		candidate := raw[start : end+1]
		if obj, ok := asObject(candidate); ok {
			if _, has := obj[marker]; has {
				return candidate, nil
			}
		}
		next := bytes.IndexByte(raw[start+1:], '{')
		if next < 0 {
			break
		}
		start += 1 + next
	}
	return nil, ErrNoJSON
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
