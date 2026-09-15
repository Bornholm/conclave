package config

import (
	"strings"
	"testing"
)

func TestWarnings(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{ID: "ok", Command: []string{"pi", "--mode", "json"}, Output: OutputPiJSON, MaxOutputBytes: 33554432},
			{ID: "lead", Command: []string{"pi", "--print", "--mode", "json", "--plan"}, Output: OutputAuto},
			{ID: "equals", Command: []string{"pi", "--mode=json"}, Output: OutputPiJSON},
			{ID: "claude", Command: []string{"claude", "--output-format", "json"}, Output: OutputAuto},
			{ID: "arg", Command: []string{"opencode", "run"}, Input: InputArgument, Output: OutputAuto},
		},
	}
	applyDefaults(cfg)
	got := strings.Join(Warnings(cfg), "\n")
	for _, want := range []string{
		`agent lead: command asks pi for the json event stream but output is "auto"`,
		"agent lead: max_output_bytes is 1048576",
		"agent equals: max_output_bytes is 1048576",
		"agent arg: input is argument but max_diff_bytes is 5242880",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"agent ok:", "agent claude:", `agent equals: command asks`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, got)
		}
	}
}
