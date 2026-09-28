package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/bornholm/conclave/internal/config"
)

// Adapted is the agent output after format-specific processing.
type Adapted struct {
	// Report is the text in which the JSON report must be searched.
	Report []byte
	// Model is the model detected in the output, if any.
	Model string
	// Trace is a compact JSON-lines record of the agent's tool calls and
	// assistant turns, kept as an artifact. Nil when the format has none.
	Trace []byte
	// ToolCalls counts the tool executions seen in the trace.
	ToolCalls int
	// Transcript is the end of what the agent wrote, reasoning included,
	// bounded by MaxTranscriptBytes. A retry hands it back to the agent when
	// no usable report came out of the run.
	Transcript []byte
}

// MaxTranscriptBytes bounds the transcript kept for a retry: the end of a
// run is where an agent concludes, and the retry prompt must stay small.
const MaxTranscriptBytes = 64 << 10

// Adapt processes raw stdout according to the agent's output format.
func Adapt(format string, stdout []byte) (*Adapted, error) {
	switch format {
	case "", config.OutputAuto:
		return &Adapted{Report: stdout, Model: detectClaudeModel(stdout), Transcript: tail(stdout, MaxTranscriptBytes)}, nil
	case config.OutputPiJSON:
		return adaptPiJSON(stdout)
	default:
		return nil, fmt.Errorf("unsupported output format %q", format)
	}
}

// detectClaudeModel reads the model from a Claude Code JSON envelope, which
// carries per-model usage under "modelUsage".
func detectClaudeModel(stdout []byte) string {
	obj, ok := asObject(bytes.TrimSpace(stdout))
	if !ok {
		return ""
	}
	// Claude Code also lists the auxiliary model (Haiku) it uses for side
	// tasks: the model that produced the most output tokens is the answering one.
	var usage map[string]struct {
		OutputTokens int64 `json:"outputTokens"`
		InputTokens  int64 `json:"inputTokens"`
	}
	raw, ok := obj["modelUsage"]
	if !ok || json.Unmarshal(raw, &usage) != nil || len(usage) == 0 {
		return ""
	}
	names := make([]string, 0, len(usage))
	for k := range usage {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := usage[names[i]], usage[names[j]]
		if a.OutputTokens != b.OutputTokens {
			return a.OutputTokens > b.OutputTokens
		}
		if a.InputTokens != b.InputTokens {
			return a.InputTokens > b.InputTokens
		}
		return names[i] < names[j]
	})
	return names[0]
}

// piEvent covers the fields Conclave uses from `pi --mode json` events.
type piEvent struct {
	Type     string          `json:"type"`
	ToolName string          `json:"toolName"`
	Args     json.RawMessage `json:"args"`
	Result   json.RawMessage `json:"result"`
	Message  *struct {
		Role     string `json:"role"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Content  []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
			Name     string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}

// adaptPiJSON extracts the last assistant text from a Pi event stream and
// builds a trace of tool executions and assistant turns. When no assistant
// text is found, the returned Adapted still carries the transcript, so the
// caller can retry the agent with what it wrote.
func adaptPiJSON(stdout []byte) (*Adapted, error) {
	ad := &Adapted{}
	var transcript bytes.Buffer
	var trace bytes.Buffer
	enc := json.NewEncoder(&trace)
	var lastText string
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev piEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "tool_execution_start":
			ad.ToolCalls++
			_ = enc.Encode(map[string]any{"type": "tool_call", "tool": ev.ToolName, "args": ev.Args})
		case "tool_execution_end":
			_ = enc.Encode(map[string]any{"type": "tool_result", "tool": ev.ToolName, "result_bytes": len(ev.Result)})
		case "message_end":
			if ev.Message == nil || ev.Message.Role != "assistant" {
				continue
			}
			if ev.Message.Model != "" {
				ad.Model = ev.Message.Model
				if ev.Message.Provider != "" {
					ad.Model = ev.Message.Provider + "/" + ev.Message.Model
				}
			}
			var kinds []string
			var text string
			for _, c := range ev.Message.Content {
				kinds = append(kinds, c.Type)
				if c.Type == "text" && c.Text != "" {
					text += c.Text
				}
				switch {
				case c.Type == "thinking" && c.Thinking != "":
					transcript.WriteString("[reasoning]\n" + c.Thinking + "\n\n")
				case c.Type == "text" && c.Text != "":
					transcript.WriteString(c.Text + "\n\n")
				}
			}
			if text != "" {
				lastText = text
			}
			entry := map[string]any{"type": "assistant", "model": ad.Model, "content": kinds}
			if text != "" {
				entry["text"] = text
			}
			_ = enc.Encode(entry)
		}
	}
	ad.Trace = trace.Bytes()
	ad.Transcript = tail(transcript.Bytes(), MaxTranscriptBytes)
	if lastText == "" {
		return ad, errors.New("pi-json: no assistant text message in output")
	}
	ad.Report = []byte(lastText)
	return ad, nil
}

// tail keeps the last limit bytes of b, starting on a rune boundary.
func tail(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	cut := b[len(b)-limit:]
	for i := 0; i < len(cut) && i < 4; i++ {
		if utf8.RuneStart(cut[i]) {
			return cut[i:]
		}
	}
	return cut
}
