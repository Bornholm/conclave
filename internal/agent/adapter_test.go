package agent

import (
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/config"
)

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
	if _, err := Adapt("xml", nil); err == nil {
		t.Error("expected unsupported format error")
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
