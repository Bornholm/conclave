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
// The whole search runs twice. The first pass parses strictly, so a run where
// every agent behaved pays for no repair and, when the output holds both a
// garbled candidate and an intact report, the intact one is the one returned.
// Only if that pass finds nothing does the second one allow repairs, because
// agents regularly emit an unescaped backslash, a literal newline or an
// unescaped quote inside a string. When a candidate carries the marker but
// stays unparsable either way, the returned error describes the syntax error
// instead of claiming nothing was found.
func ExtractJSON(raw []byte, marker string) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	out, strictErr := extract(raw, marker, 0, false)
	if strictErr == nil {
		return out, nil
	}
	out, err := extract(raw, marker, 0, true)
	if err == nil {
		return out, nil
	}
	// The strict pass names the syntax error precisely; the repairing one only
	// reports that its guesses did not land.
	if errors.Is(err, ErrNoJSON) && !errors.Is(strictErr, ErrNoJSON) {
		return nil, strictErr
	}
	return nil, err
}

func extract(raw []byte, marker string, depth int, repair bool) ([]byte, error) {
	if depth > 4 || len(raw) == 0 {
		return nil, ErrNoJSON
	}
	var malformed error
	// The whole input read as a single object. Repairing it is the riskiest
	// reading there is: the fixer is free to run a string across what is in
	// fact the boundary between two documents, welding an NDJSON stream into
	// one object that no agent ever wrote. So it is tried first when parsing
	// strictly, and kept for last once repairs are allowed, by which time the
	// narrower candidates below have had their chance.
	whole := func() ([]byte, error) {
		obj, fixed, ok := parseObject(raw, repair)
		if !ok {
			return nil, ErrNoJSON
		}
		if _, has := obj[marker]; has {
			return fixed, nil
		}
		var err error
		if so, ok := obj["structured_output"]; ok && len(so) > 0 && so[0] == '{' {
			out, e := extract(so, marker, depth+1, repair)
			if e == nil {
				return out, nil
			}
			err = keepMalformed(err, e)
		}
		if res, ok := obj["result"]; ok {
			var s string
			if json.Unmarshal(res, &s) == nil {
				out, e := extract([]byte(strings.TrimSpace(s)), marker, depth+1, repair)
				if e == nil {
					return out, nil
				}
				err = keepMalformed(err, e)
			}
		}
		if err != nil {
			return nil, err
		}
		return nil, ErrNoJSON
	}
	if !repair {
		if out, err := whole(); err == nil {
			return out, nil
		} else {
			malformed = keepMalformed(malformed, err)
		}
	}
	// NDJSON: try each line.
	if bytes.Count(raw, []byte("\n")) > 0 {
		for _, line := range bytes.Split(raw, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) > 1 && line[0] == '{' {
				if _, _, ok := parseObject(line, repair); ok {
					if out, err := extract(line, marker, depth+1, repair); err == nil {
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
		if out, err := extract(body, marker, depth+1, repair); err == nil {
			return out, nil
		} else {
			malformed = keepMalformed(malformed, err)
		}
	}
	// Balanced-brace scan.
	for start := bytes.IndexByte(raw, '{'); start >= 0 && start < len(raw); {
		if end := matchBrace(raw, start); end >= 0 {
			candidate := raw[start : end+1]
			if obj, fixed, ok := parseObject(candidate, repair); ok {
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
	if repair {
		if out, err := whole(); err == nil {
			return out, nil
		} else {
			malformed = keepMalformed(malformed, err)
		}
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

// parseObject parses raw as a JSON object and returns the bytes that actually
// parsed. Unless repair is set, that strict parse is all it does. When it is,
// a failure is retried on repaired copies, in order of how safe the repair is:
// escaping backslashes and control characters first, then the riskier guess
// about unescaped quotes.
func parseObject(raw []byte, repair bool) (map[string]json.RawMessage, []byte, bool) {
	if obj, ok := asObject(raw); ok {
		return obj, raw, true
	}
	if !repair {
		return nil, nil, false
	}
	fixed, changed := repairJSON(raw)
	if changed {
		if obj, ok := asObject(fixed); ok {
			return obj, fixed, true
		}
	}
	if quoted, changed := repairQuotes(fixed); changed {
		if obj, ok := asObject(quoted); ok {
			return obj, quoted, true
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

// maxFixerSteps bounds the backtracking below, which is exponential in the
// worst case. A report that needs more attempts than this is reported as
// malformed rather than parsed slowly.
const maxFixerSteps = 1 << 15

// repairQuotes escapes the quotes agents leave unescaped inside a JSON string,
// which happens as soon as they quote code (`x != "" {`, `map[string]string{"a":
// "b"}`) or speech. Deciding locally whether a quote closes its string is not
// possible — a literal quote is regularly followed by `,`, `:` or `}`, exactly
// like a closing one. So the document is walked as a grammar instead: at every
// quote that could close a string, the rest of the document is parsed, and the
// reading that makes the whole document fit is the one kept. Quotes skipped
// along the way are the literals, and they are escaped.
//
// This is still a guess, so it only runs after the strict parse and repairJSON
// have both failed, and its result is handed to the strict parser again.
func repairQuotes(raw []byte) ([]byte, bool) {
	f := &quoteFixer{raw: raw}
	_, ok := f.value(0, func(i int) (int, bool) {
		if i = f.skipSpace(i); i != len(f.raw) {
			return 0, false
		}
		return i, true
	})
	if !ok || len(f.escapes) == 0 {
		return raw, false
	}
	out := make([]byte, 0, len(raw)+len(f.escapes))
	prev := 0
	for _, p := range f.escapes {
		out = append(out, raw[prev:p]...)
		out = append(out, '\\', '"')
		prev = p + 1
	}
	return append(out, raw[prev:]...), true
}

// cont is the rest of the parse that follows the value being read. Returning
// false makes the value try its next possible reading, if it has one.
type cont func(i int) (int, bool)

// quoteFixer parses raw loosely, recording the positions of the quotes that
// turned out to be string contents rather than delimiters.
type quoteFixer struct {
	raw     []byte
	escapes []int
	steps   int
}

func (f *quoteFixer) budget() bool {
	f.steps++
	return f.steps <= maxFixerSteps
}

func (f *quoteFixer) skipSpace(i int) int {
	for ; i < len(f.raw); i++ {
		switch f.raw[i] {
		case ' ', '\t', '\r', '\n':
			continue
		}
		break
	}
	return i
}

func (f *quoteFixer) value(i int, k cont) (int, bool) {
	if !f.budget() {
		return 0, false
	}
	if i = f.skipSpace(i); i >= len(f.raw) {
		return 0, false
	}
	switch f.raw[i] {
	case '{':
		return f.object(i+1, k)
	case '[':
		return f.array(i+1, k)
	case '"':
		return f.str(i, k)
	}
	// A scalar. It is matched strictly: a loose reading here would let the
	// fixer close a string too early and take the remaining text for a value,
	// producing bytes that parse as something the agent never wrote.
	j, ok := f.scalar(i)
	if !ok {
		return 0, false
	}
	return k(j)
}

// scalar returns the end of the literal starting at i, or false if what starts
// there is not `true`, `false`, `null` or a JSON number.
func (f *quoteFixer) scalar(i int) (int, bool) {
	for _, lit := range []string{"true", "false", "null"} {
		if bytes.HasPrefix(f.raw[i:], []byte(lit)) {
			return i + len(lit), true
		}
	}
	j := i
	if j < len(f.raw) && f.raw[j] == '-' {
		j++
	}
	digits := j
	for ; j < len(f.raw) && f.raw[j] >= '0' && f.raw[j] <= '9'; j++ {
	}
	if j == digits {
		return 0, false
	}
	if j < len(f.raw) && f.raw[j] == '.' {
		j++
		frac := j
		for ; j < len(f.raw) && f.raw[j] >= '0' && f.raw[j] <= '9'; j++ {
		}
		if j == frac {
			return 0, false
		}
	}
	if j < len(f.raw) && (f.raw[j] == 'e' || f.raw[j] == 'E') {
		j++
		if j < len(f.raw) && (f.raw[j] == '+' || f.raw[j] == '-') {
			j++
		}
		exp := j
		for ; j < len(f.raw) && f.raw[j] >= '0' && f.raw[j] <= '9'; j++ {
		}
		if j == exp {
			return 0, false
		}
	}
	return j, true
}

func (f *quoteFixer) object(i int, k cont) (int, bool) {
	if i = f.skipSpace(i); i < len(f.raw) && f.raw[i] == '}' {
		return k(i + 1)
	}
	var member cont
	member = func(i int) (int, bool) {
		if !f.budget() {
			return 0, false
		}
		if i = f.skipSpace(i); i >= len(f.raw) || f.raw[i] != '"' {
			return 0, false
		}
		return f.str(i, func(j int) (int, bool) {
			if j = f.skipSpace(j); j >= len(f.raw) || f.raw[j] != ':' {
				return 0, false
			}
			return f.value(j+1, func(m int) (int, bool) {
				if m = f.skipSpace(m); m >= len(f.raw) {
					return 0, false
				}
				switch f.raw[m] {
				case ',':
					return member(m + 1)
				case '}':
					return k(m + 1)
				}
				return 0, false
			})
		})
	}
	return member(i)
}

func (f *quoteFixer) array(i int, k cont) (int, bool) {
	if i = f.skipSpace(i); i < len(f.raw) && f.raw[i] == ']' {
		return k(i + 1)
	}
	var element cont
	element = func(i int) (int, bool) {
		if !f.budget() {
			return 0, false
		}
		return f.value(i, func(m int) (int, bool) {
			if m = f.skipSpace(m); m >= len(f.raw) {
				return 0, false
			}
			switch f.raw[m] {
			case ',':
				return element(m + 1)
			case ']':
				return k(m + 1)
			}
			return 0, false
		})
	}
	return element(i)
}

// str reads the string opening at i, trying each quote that could close it in
// turn: the earliest one first, so a well-formed string costs a single
// attempt. Every quote the successful reading stepped over is recorded as a
// literal to escape.
func (f *quoteFixer) str(i int, k cont) (int, bool) {
	mark, skipped := len(f.escapes), 0
	for j := i + 1; j < len(f.raw); j++ {
		if f.raw[j] == '\\' {
			j++
			continue
		}
		if f.raw[j] != '"' {
			continue
		}
		if !f.budget() {
			break
		}
		if end, ok := k(j + 1); ok {
			return end, true
		}
		// Drop what the failed reading recorded, then treat this quote as a
		// literal and look for the next candidate.
		f.escapes = append(f.escapes[:mark+skipped], j)
		skipped++
	}
	f.escapes = f.escapes[:mark]
	return 0, false
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
