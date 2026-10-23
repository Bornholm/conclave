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

// ExtractTruncatedJSON closes a JSON object that was cut off mid-write and
// parses it. It exists for one case only: a run stopped while the model was
// still writing its report, where the beginning of the object is intact and
// the end is simply missing.
//
// The repair is deliberately narrow, because a report is a claim about code and
// a wrong claim is worse than none. An unterminated string is closed and the
// containers left open are closed after it; the one member the cut fell in the
// middle of is dropped, since closing around it would leave "note":}, which
// does not parse. Nothing else is removed and nothing is invented. The result
// is returned only when it parses strictly once closed and carries the marker,
// so a candidate cut somewhere hopeless - inside a number or inside an escape
// sequence - still fails. A cut in the middle of a key is not one of them: the
// key is the member the cut fell in, so it is dropped like any other.
//
// It depends on no agent: the report is looked for wherever the run left it,
// bare or inside an envelope, which is what lets the caller apply the same
// repair to every configured agent.
func ExtractTruncatedJSON(raw []byte, marker string) ([]byte, error) {
	// The report is the last object whose opening fields carry the marker and
	// which never closes: it is the one the model was still writing when it was
	// killed. The scan walks braces backwards, which is what finds it last, and
	// stops as soon as a candidate carrying the marker turns out to close - from
	// there the text holds a complete report, so nothing was cut.
	//
	// The walk is bounded by construction: a brace that does not carry the marker
	// within its opening fields is skipped without any re-scan, and an event
	// stream holding no report at all is therefore read in linear time. Walking
	// every brace in full took minutes on 12 MB.
	key := []byte(`"` + marker + `"`)
	first := bytes.IndexByte(raw, '{')
	for pos := len(raw); pos > 0; {
		start := bytes.LastIndexByte(raw[:pos], '{')
		if start < 0 {
			break
		}
		pos = start
		// The marker has to be this object's own key: a nested object carries it
		// too, and judging that one ends the walk on a member instead of the
		// report. The window bounds the work, since every schema puts the marker
		// in its opening fields.
		if bytes.Contains(raw[start:min(start+markerWindow, len(raw))], key) && isTopLevelKey(raw, start, key) {
			if out, err := closeAt(raw[start:], marker); err == nil {
				return out, nil
			}
			if matchBrace(raw, start) >= 0 {
				// This very object closes, marker and all: it is a complete
				// report, so nothing later in the text was cut away.
				return nil, ErrNoJSON
			}
		}
		// An envelope holds the report escaped, so its braces never carry the
		// marker. Only the outermost object can be one.
		if start == first {
			if out, err := closeTruncated(raw[start:], marker); err == nil {
				return out, nil
			}
		}
	}
	return nil, ErrNoJSON
}

// isTopLevelKey reports whether key appears at the first level of the object
// starting at start, rather than inside a nested one.
func isTopLevelKey(b []byte, start int, key []byte) bool {
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
			// A string opening at depth 1 is a key of this object.
			if depth == 1 && bytes.HasPrefix(b[i:], key) {
				return true
			}
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return false
			}
		}
	}
	return false
}

// markerWindow bounds how far into an object the marker is looked for. Every
// report schema puts it in the opening fields, and searching the whole object
// would make the backward walk quadratic again.
const markerWindow = 4096

// closeTruncated applies the repair to one candidate, first as a bare object and
// then as an envelope holding the report escaped.
func closeTruncated(candidate []byte, marker string) ([]byte, error) {
	if out, err := closeAt(candidate, marker); err == nil {
		return out, nil
	}
	if unescaped, ok := unescapeCutEnvelope(candidate); ok {
		return ExtractTruncatedJSON(unescaped, marker)
	}
	return nil, ErrNoJSON
}

// unescapeCutEnvelope decodes the JSON string values of a document that was
// itself cut off, so an escaped report inside it can be repaired.
//
// Only the string values are decoded, and their order is kept: the report is
// then found by the same scan that handles a bare one. A document with no
// string value, or one whose escapes do not decode, produces nothing and the
// caller moves on to the next brace.
func unescapeCutEnvelope(b []byte) ([]byte, bool) {
	var out bytes.Buffer
	found, inStr, key := false, false, false
	start := 0
	emit := func(end int) {
		if key {
			return
		}
		var val string
		if json.Unmarshal(b[start:end], &val) == nil && strings.Contains(val, "{") {
			out.WriteString(val)
			out.WriteByte('\n')
			found = true
		}
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			switch {
			case c == '\\':
				i++
			case c == '"':
				emit(i + 1)
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr, key = true, false
			start = i
			if n := len(bytes.TrimRight(b[:i], " \t\r\n")); n > 0 {
				prev := b[n-1]
				// A key is a string followed by a colon, not a comma.
				key = prev == '{' || prev == ','
			}
		}
	}
	if inStr && b[len(b)-1] != '\\' {
		// The envelope itself was killed mid-value, which is exactly what a
		// timeout on a streaming agent produces. The value still holds the report
		// up to the cut, so it is decoded with the string closed: the repair
		// applied afterwards is what closes the report itself.
		closed := make([]byte, 0, len(b)-start+1)
		closed = append(closed, b[start:]...)
		closed = append(closed, '"')
		var val string
		if json.Unmarshal(closed, &val) == nil && strings.Contains(val, "{") {
			out.WriteString(val)
			out.WriteByte('\n')
			found = true
		}
	}
	return out.Bytes(), found
}

// closeAt closes the object starting at b[0], which must carry the marker.
//
// A candidate that already closes is not a truncation, and says so: returning
// it as repaired would label every complete report as cut off, which is the
// opposite of what the caller needs to know.
func closeAt(candidate []byte, marker string) ([]byte, error) {
	// The caller has already checked that this object carries the marker at its
	// own level; requiring it as the first key would throw away every report
	// written in another order.
	if end := matchBrace(candidate, 0); end >= 0 {
		return nil, ErrNoJSON
	}
	closed, ok := closeUnterminated(candidate)
	if !ok {
		return nil, ErrNoJSON
	}
	// Once closed, the document goes through the ordinary repair pass: a run
	// that was cut mid-message is also a run that never got to fix its
	// escaping, and the two problems arrive together.
	obj, fixed, ok := parseObject(closed, true)
	if !ok {
		return nil, fmt.Errorf("malformed truncated JSON report: %w", ErrNoJSON)
	}
	closed = fixed
	if _, has := obj[marker]; !has {
		return nil, ErrNoJSON
	}
	return closed, nil
}

// closeUnterminated closes the strings and containers left open at the end of
// a truncated JSON document, dropping the last member when the cut fell where
// no bracket can repair it.
//
// A cut after `"note":` or after a comma leaves a document that closing alone
// would turn into `"note":}`, which does not parse. The member being written is
// the only incomplete thing in the document, so it goes, and everything already
// complete is kept. That is the whole risk of this repair: a member the model
// had finished but not yet separated from the next one is indistinguishable
// from one it had not started, and both are dropped.
//
// It reports false for a document it cannot close honestly: one that ends
// inside an escape sequence, where the missing character could turn out to be a
// quote or a backslash, or after a number, where closing it would silently keep
// a truncated value - and a start_line one digit short points at the wrong code.
func closeUnterminated(b []byte) ([]byte, bool) {
	var out bytes.Buffer
	out.Grow(len(b) + 8)
	// stack holds the open containers; members[i] is where the current member
	// of stack[i] began, or -1 when it has none.
	var stack []byte
	var members []int
	// valueStarted records that the member being written has a value, so a
	// trailing comma is a separator rather than an unfinished value.
	valueStarted := false
	// inStr and esc track the string being read, if any.
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			out.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			inStr = true
			if n := len(stack); n > 0 && stack[n-1] == '{' {
				trimmed := bytes.TrimRight(out.Bytes(), " \t\r\n")
				if len(trimmed) > 0 && trimmed[len(trimmed)-1] != '{' && trimmed[len(trimmed)-1] != ',' {
					valueStarted = true
				}
			} else {
				valueStarted = true
			}
		case 't', 'f', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			valueStarted = true
		case '{', '[':
			stack = append(stack, c)
			members = append(members, -1)
			valueStarted = true
		case '}', ']':
			if len(stack) == 0 {
				return nil, false
			}
			stack = stack[:len(stack)-1]
			members = members[:len(members)-1]
			valueStarted = true
		case ',':
			if n := len(stack); n > 0 {
				members[n-1] = i + 1
			}
			valueStarted = false
		}
		out.WriteByte(c)
	}
	if esc {
		// The last character was a backslash starting an escape.
		return nil, false
	}
	if !inStr {
		// The last byte is judged after trailing whitespace: a newline appended
		// by the caller must not hide a number the cut landed in.
		if trimmed := bytes.TrimRight(b, " \t\r\n"); len(trimmed) > 0 {
			switch trimmed[len(trimmed)-1] {
			case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '+', '-', '.', 'e', 'E':
				return nil, false
			}
		}
	}
	if !valueStarted {
		// The member being written was cut before its value: drop it, so the
		// closing brackets attach to something that is complete.
		if len(members) == 0 || members[len(members)-1] < 0 {
			return nil, false
		}
		at := members[len(members)-1]
		trimmed := bytes.TrimRight(b[:at], " \t\r\n")
		if len(trimmed) > 0 && trimmed[len(trimmed)-1] == ',' {
			trimmed = bytes.TrimRight(trimmed[:len(trimmed)-1], " \t\r\n")
		}
		out.Reset()
		out.Write(trimmed)
	} else if inStr {
		out.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out.WriteByte('}')
		} else {
			out.WriteByte(']')
		}
	}
	return out.Bytes(), true
}

// EnsureVerdict adds a conservative verdict to a report that has none, which is
// what a report cut before its last members looks like: the schema puts the
// verdict last, so a truncated one has lost it while keeping the findings.
//
// The verdict added is "comment", never "approve". A partial report must not be
// able to approve a pull request: the findings that would have justified a
// stricter verdict may be exactly the ones the cut destroyed. It returns the
// document unchanged, and false, when it already carries a verdict.
func EnsureVerdict(raw []byte) ([]byte, bool) {
	obj, ok := asObject(bytes.TrimSpace(raw))
	if !ok {
		return raw, false
	}
	if v, has := obj["verdict"]; has {
		// A repaired report that says "approve" is downgraded: the findings that
		// would have justified a stricter verdict may be exactly the ones the cut
		// destroyed, and a partial review must never be able to approve.
		var verdict string
		if json.Unmarshal(v, &verdict) == nil && verdict == "approve" {
			obj["verdict"] = json.RawMessage(`"comment"`)
			out, err := json.Marshal(obj)
			if err != nil {
				return raw, false
			}
			return out, true
		}
		return raw, false
	}
	var rep map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rep); err != nil {
		return raw, false
	}
	rep["verdict"] = json.RawMessage(`"comment"`)
	out, err := json.Marshal(rep)
	if err != nil {
		return raw, false
	}
	return out, true
}

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
