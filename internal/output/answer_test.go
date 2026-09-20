package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func sampleAnswer() *domain.ConsolidatedAnswer {
	return &domain.ConsolidatedAnswer{
		SchemaVersion: "1",
		Question:      "Why does the client\ngive up after one attempt?",
		Answer:        "The call is not retried.\n\n```go\nresp, err := c.Do(req)\n```",
		KeyPoints:     []string{"One attempt, no retry."},
		Disagreements: []domain.AnswerDisagreement{{Topic: "How many attempts",
			Positions: []domain.AnswerPosition{{By: []string{"a"}, Position: "three"}, {By: []string{"b"}, Position: "ten"}}}},
		References:    []domain.AnswerReference{{Source: "internal/http/client.go", Line: 42, Note: "the call"}},
		Caveats:       []string{"Read only the checked-out revision."},
		OpenQuestions: []string{"How many attempts are wanted?"},
		Confidence:    0.8, ReportedBy: []string{"a", "b"},
		OtherAnswers: []domain.AgentAnswer{{AgentID: "b", Answer: "It retries twice."}},
		Failed:       []domain.FailedReviewer{{ID: "pi", Reason: "timed out after 15m"}},
		Warnings:     []string{"a: 1 reference dropped"},
		Meta: domain.AnswerMeta{RunID: "run1", Repository: "acme/proj", Project: "/tmp/proj",
			HeadSHA: "7a88e4f0000", Branch: "main", RespondentsTotal: 3, RespondentsSucceeded: 2,
			LeadUsed: true, Models: map[string]string{"a": "m1"}},
	}
}

func TestAnswerMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := AnswerMarkdown(&buf, sampleAnswer(), Options{ShowAttribution: true, ShowFailedAgents: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"## Conclave answer", "> Why does the client\n> give up after one attempt?",
		"**Repository:** acme/proj", "`7a88e4f` (main)", "**Confidence:** 80%", "**Agents:** 2 of 3",
		"**Answered by:** a, b", "**Models:** a (m1)", "### In short", "### Answer",
		"```go", "### Where the agents disagreed", "  - a: three", "### References",
		"`internal/http/client.go`:42 — the call", "### Caveats", "### Open questions",
		"### Answer from b", "### Agents that failed", "### Warnings", "_Run `run1`_",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q\n%s", want, out)
		}
	}
	buf.Reset()
	AnswerMarkdown(&buf, sampleAnswer(), Options{})
	if strings.Contains(buf.String(), "Answered by") || strings.Contains(buf.String(), "Agents that failed") {
		t.Error("options not honored")
	}
	// Without a project the header falls back to nothing at all rather than
	// naming a repository that does not exist.
	bare := sampleAnswer()
	bare.Meta = domain.AnswerMeta{RunID: "run2"}
	buf.Reset()
	AnswerMarkdown(&buf, bare, Options{})
	if strings.Contains(buf.String(), "**Repository:**") || strings.Contains(buf.String(), "**Revision:**") {
		t.Errorf("a question without a project must not claim one:\n%s", buf.String())
	}
}

func TestAnswerJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := AnswerJSON(&buf, sampleAnswer()); err != nil {
		t.Fatal(err)
	}
	var back domain.ConsolidatedAnswer
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || len(back.Disagreements) != 1 || back.Confidence != 0.8 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}
