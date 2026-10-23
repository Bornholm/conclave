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

// TestAdaptPiJSONPrefersTheUnfinishedMessage: a run that finished one message
// with prose and was then killed while writing its report must yield the
// report, not the older prose. The deltas are by construction the newest
// content, since every message_start resets them.
func TestAdaptPiJSONPrefersTheUnfinishedMessage(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"I will start by reading the diff."}]}}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"{\"schema_version\":\"1\",\"summ"}}`,
	}, "\n")
	ad, err := Adapt(config.OutputPiJSON, []byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if !ad.Partial {
		t.Error("the run was stopped mid-message")
	}
	if strings.Contains(string(ad.Report), "I will start by reading") {
		t.Errorf("the report is the older prose, not the message being written: %s", ad.Report)
	}
	if !strings.Contains(string(ad.Report), "schema_version") {
		t.Errorf("the report must be the unfinished message: %s", ad.Report)
	}
}

// TestAdaptPiJSONKeepsACompleteReportOverLaterProse: a run that finished its
// report and was stopped while adding a remark must keep the report. The
// unfinished message is newer, but it is not a report.
func TestAdaptPiJSONKeepsACompleteReportOverLaterProse(t *testing.T) {
	report := `{"schema_version":"1","reviewer":{"id":"r"},"summary":"s","findings":[],"verdict":"comment"}`
	stream := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(report) + `}]}}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Let me also double-check"}}`,
	}, "\n")
	ad, err := Adapt(config.OutputPiJSON, []byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if string(ad.Report) != report {
		t.Errorf("the complete report was replaced: %s", ad.Report)
	}
	if ad.Partial {
		t.Error("the report is complete, not recovered from an unfinished message")
	}
	if !strings.Contains(string(ad.Transcript), "Let me also double-check") {
		t.Errorf("what followed the report belongs in the transcript: %s", ad.Transcript)
	}
}

// TestAdaptPiJSONKeepsACompleteReportOverACutRewrite: a run that finished its
// report and was stopped while writing it a second time keeps the first one.
// The rewrite carries the marker, which is no reason to prefer a cut copy.
func TestAdaptPiJSONKeepsACompleteReportOverACutRewrite(t *testing.T) {
	report := `{"schema_version":"1","reviewer":{"id":"r"},"summary":"s","findings":[],"verdict":"comment"}`
	stream := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(report) + `}]}}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"{\"schema_version\":\"1\",\"summ"}}`,
	}, "\n")
	ad, err := Adapt(config.OutputPiJSON, []byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if string(ad.Report) != report || ad.Partial {
		t.Errorf("the complete report must stand over its cut rewrite: partial=%v %s", ad.Partial, ad.Report)
	}
}

// TestAdaptPiJSONKeepsReasoningOutOfTheReport: a real pi message carries both
// thinking and text deltas (verified on a recorded run: 2 of 17 messages). Mixing
// them put the model's reasoning at the start of the salvaged document.
func TestAdaptPiJSONKeepsReasoningOutOfTheReport(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"Let me check the diff. "}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"{\"schema_version\":"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"Now the findings. "}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"\"1\"}"}}`,
	}, "\n")
	ad, err := Adapt(config.OutputPiJSON, []byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(ad.Report); got != `{"schema_version":"1"}` {
		t.Errorf("report = %q, want only the text deltas", got)
	}
	// The reasoning is not lost: it belongs to the transcript a retry reads.
	if !strings.Contains(string(ad.Transcript), "Let me check the diff") {
		t.Errorf("the reasoning must reach the transcript: %q", ad.Transcript)
	}
}

// TestAdaptPiJSONDoesNotMarkACompleteRunPartial: message_end supersedes the deltas
// of its own message, reasoning included. Leaving the reasoning behind made a run
// that ended normally look like one stopped mid-message, and the salvage then
// published "the agent was stopped while still writing" for a complete report.
func TestAdaptPiJSONDoesNotMarkACompleteRunPartial(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"weighing it up"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"weighing it up"},{"type":"text","text":"{\"schema_version\":\"1\"}"}]}}`,
		`{"type":"agent_settled"}`,
	}, "\n")
	ad, err := Adapt(config.OutputPiJSON, []byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if ad.Partial {
		t.Error("a run that ended normally must not be marked partial")
	}
	if n := strings.Count(string(ad.Transcript), "weighing it up"); n != 1 {
		t.Errorf("the reasoning appears %d times in the transcript, want 1", n)
	}
}
