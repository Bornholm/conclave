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
	if _, err := Adapt(config.OutputPiJSON, []byte(`{"type":"agent_start"}`)); err == nil {
		t.Error("expected error without assistant text")
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
