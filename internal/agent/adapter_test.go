package agent

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/config"
)

// reportJSON is a minimal valid reviewer report.
const reportJSON = `{"schema_version":"1","reviewer":{"id":"r1"},"summary":"s","findings":[],"verdict":"approve"}`

const piStream = `{"type":"session","version":3,"id":"x"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"prompt"}]}}
{"type":"tool_execution_start","toolCallId":"t0","toolName":"read","args":{"path":"a.go"}}
{"type":"tool_execution_end","toolCallId":"t0","toolName":"read","result":{"content":[{"type":"text","text":"package a"}]}}
{"type":"message_end","message":{"role":"assistant","provider":"openrouter","model":"moonshotai/kimi-k2.6","content":[{"type":"thinking","thinking":"hmm"},{"type":"toolCall","name":"read"}]}}
{"type":"message_end","message":{"role":"assistant","provider":"openrouter","model":"moonshotai/kimi-k2.6","content":[{"type":"text","text":"Here: {\"schema_version\":\"1\",\"reviewer\":{\"id\":\"r1\"},\"summary\":\"s\",\"findings\":[],\"verdict\":\"approve\"}"}]}}
{"type":"agent_end"}
`

func TestAdaptPiJSON(t *testing.T) {
	ad, err := Adapt(config.OutputPiJSON, []byte(piStream))
	if err != nil {
		t.Fatal(err)
	}
	if ad.Model != "openrouter/moonshotai/kimi-k2.6" || ad.ToolCalls != 1 {
		t.Errorf("model=%q tools=%d", ad.Model, ad.ToolCalls)
	}
	rep, _, err := ParseReport(ad.Report, "r1", nil, nil, Limits{})
	if err != nil || rep.Verdict != "approve" {
		t.Errorf("report: %v %+v", err, rep)
	}
	tr := string(ad.Trace)
	if !strings.Contains(tr, `"tool":"read"`) || !strings.Contains(tr, `"type":"assistant"`) || strings.Contains(tr, "package a") {
		t.Errorf("trace: %s", tr)
	}
	if !strings.Contains(string(ad.Transcript), "[reasoning]\nhmm") || !strings.Contains(string(ad.Transcript), "schema_version") {
		t.Errorf("transcript: %s", ad.Transcript)
	}
	if _, err := Adapt(config.OutputPiJSON, []byte(`{"type":"agent_start"}`)); err == nil {
		t.Error("expected error without assistant text")
	}
	// An agent that only reasoned before stopping still leaves a transcript
	// for the retry to hand back.
	thinkingOnly := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"three findings in pricing.go"}]}}`
	ad, err = Adapt(config.OutputPiJSON, []byte(thinkingOnly))
	if err == nil || ad == nil || !strings.Contains(string(ad.Transcript), "three findings in pricing.go") {
		t.Errorf("a run without text must fail but keep its transcript: %v %+v", err, ad)
	}
	// A provider refusal is told apart from a run that wrote nothing.
	refused := `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"429: over budget"}}
{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"429: over budget"}`
	if _, err := Adapt(config.OutputPiJSON, []byte(refused)); !errors.Is(err, ErrModelAPI) || !strings.Contains(err.Error(), "429: over budget") {
		t.Errorf("a refused run must report the API error: %v", err)
	}
	if _, err := Adapt("xml", nil); err == nil {
		t.Error("expected unsupported format error")
	}
}

// TestAdaptPiJSONStoppedMidMessage: a run killed while the model was still
// writing leaves no message_end, only deltas. An agent told to conclude has
// usually written its JSON by then, so the deltas are read as the report.
func TestAdaptPiJSONStoppedMidMessage(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"let me conclude. "}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Here is the report: "}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":` + strconv.Quote(reportJSON) + `}}`,
	}, "\n")
	ad, err := Adapt(config.OutputPiJSON, []byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if !ad.Partial {
		t.Error("a report recovered from deltas must be marked partial")
	}
	if !strings.Contains(string(ad.Report), "Here is the report") || !strings.Contains(string(ad.Report), "schema_version") {
		t.Errorf("report: %s", ad.Report)
	}
	rep, _, err := ParseReport(ad.Report, "r1", nil, nil, Limits{})
	if err != nil || rep.Verdict != "approve" {
		t.Errorf("report: %v %+v", err, rep)
	}

	// A completed message supersedes its deltas, and its report is not partial.
	completed := `{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"half a senten"}}
` + piStream
	ad, err = Adapt(config.OutputPiJSON, []byte(completed))
	if err != nil {
		t.Fatal(err)
	}
	if ad.Partial || strings.Contains(string(ad.Report), "half a senten") {
		t.Errorf("a completed message must win: partial=%v report=%s", ad.Partial, ad.Report)
	}

	// A tool call ends the message that asked for it: its deltas are gone.
	newTurn := `{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"stale"}}
{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{}}
{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"fresh"}}`
	ad, err = Adapt(config.OutputPiJSON, []byte(newTurn))
	if err != nil {
		t.Fatal(err)
	}
	if string(ad.Report) != "fresh" {
		t.Errorf("report = %q, want only the text after the tool call", ad.Report)
	}
}

func TestAdaptAutoDetectsClaudeModel(t *testing.T) {
	env := `{"type":"result","result":"{}","modelUsage":{"claude-haiku-4-5":{"inputTokens":900,"outputTokens":20},"claude-opus-4-8":{"inputTokens":100,"outputTokens":3000}}}`
	ad, err := Adapt(config.OutputAuto, []byte(env))
	if err != nil || ad.Model != "claude-opus-4-8" || string(ad.Report) != env {
		t.Errorf("%v %+v", err, ad)
	}
	ad, _ = Adapt("", []byte("plain"))
	if ad.Model != "" {
		t.Error("no model expected")
	}
}

func TestTailKeepsTheEndOnARuneBoundary(t *testing.T) {
	got := string(tail([]byte("aébc"), 3))
	if got != "bc" {
		t.Errorf("tail = %q, want the end cut on a rune boundary", got)
	}
	if got := string(tail([]byte("abc"), 10)); got != "abc" {
		t.Errorf("tail = %q", got)
	}
}
