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
	cfg.Review.Limits.MaxInlineDiffBytes = 1 << 20
	applyDefaults(cfg)
	got := strings.Join(Warnings(cfg), "\n")
	for _, want := range []string{
		`agent lead: command asks pi for the json event stream but output is "auto"`,
		"agent lead: max_output_bytes is 1048576",
		"agent equals: max_output_bytes is 1048576",
		"agent arg: input is argument but the prompt may inline 1048576 bytes of diff",
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

func TestWarningsOnAQuorumThatStopsNoReviewer(t *testing.T) {
	cfg := &Config{Agents: []AgentConfig{{ID: "r1", Role: RoleReviewer}, {ID: "r2", Role: RoleReviewer}, {ID: "lead", Role: RoleLead}}}
	cfg.Review.Quorum = 2
	if got := strings.Join(Warnings(cfg), "\n"); !strings.Contains(got, "review.quorum is 2 with 2 reviewers") {
		t.Errorf("missing the quorum warning in:\n%s", got)
	}
	cfg.Review.Quorum = 1
	if got := strings.Join(Warnings(cfg), "\n"); strings.Contains(got, "review.quorum") {
		t.Errorf("unexpected quorum warning in:\n%s", got)
	}
}
