package consolidation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/domain"
)

// AnswerOutcome is the result of one agent answering the question.
type AnswerOutcome struct {
	ID     string
	Report *domain.AnswerReport
	Err    error
}

// ParseLeadAnswer decodes and validates the consolidated answer. Positions
// and reported_by are restricted to respondents that actually succeeded, so
// the lead cannot attribute a claim to an agent that never spoke.
func ParseLeadAnswer(raw []byte, question string, succeeded []string, lim agent.AnswerLimits) (*domain.ConsolidatedAnswer, []string, error) {
	data, err := agent.ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var ans domain.ConsolidatedAnswer
	if err := json.Unmarshal(data, &ans); err != nil {
		return nil, nil, fmt.Errorf("decode lead answer: %w", err)
	}
	if ans.SchemaVersion != domain.AnswerSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", ans.SchemaVersion)
	}
	if ans.Confidence < 0 || ans.Confidence > 1 {
		ans.Confidence = 0
	}
	ans.Answer = agent.TruncateField(ans.Answer, maxAnswerBytes(lim))
	if ans.Answer == "" {
		return nil, nil, fmt.Errorf("the lead answered nothing")
	}

	known := make(map[string]bool, len(succeeded))
	for _, id := range succeeded {
		known[id] = true
	}
	var warnings []string
	ans.KeyPoints, warnings = agent.NormalizeKeyPoints(ans.KeyPoints, lim, "lead: ")
	refs, refWarnings := agent.NormalizeAnswerReferences(ans.References, lim, "lead: ")
	ans.References = refs
	warnings = append(warnings, refWarnings...)
	ans.Caveats = agent.NormalizeAnswerLines(ans.Caveats, fieldBytes(lim))
	ans.OpenQuestions = agent.NormalizeAnswerLines(ans.OpenQuestions, fieldBytes(lim))

	var disagreements []domain.AnswerDisagreement
	for _, d := range ans.Disagreements {
		d.Topic = agent.TruncateField(d.Topic, fieldBytes(lim))
		var positions []domain.AnswerPosition
		for _, p := range d.Positions {
			p.Position = agent.TruncateField(p.Position, fieldBytes(lim))
			p.By = keepKnown(p.By, known)
			if p.Position == "" || len(p.By) == 0 {
				warnings = append(warnings, fmt.Sprintf("lead: a position on %q was dropped: it names no successful respondent", agent.TruncateField(d.Topic, 60)))
				continue
			}
			positions = append(positions, p)
		}
		if d.Topic == "" || len(positions) == 0 {
			continue
		}
		d.Positions = positions
		disagreements = append(disagreements, d)
	}
	ans.Disagreements = disagreements

	ans.ReportedBy = keepKnown(ans.ReportedBy, known)
	ans.Question = question
	return &ans, warnings, nil
}

// FallbackAnswer builds an answer without a lead: the most confident answer
// wins whole, and the others are published beside it rather than merged.
// Merging two answers that may contradict each other would produce an answer
// nobody wrote, and nothing would say where they parted.
func FallbackAnswer(question string, outcomes []AnswerOutcome) *domain.ConsolidatedAnswer {
	var best *AnswerOutcome
	var contributors []string
	for i := range outcomes {
		o := &outcomes[i]
		if o.Report == nil {
			continue
		}
		contributors = append(contributors, o.ID)
		if best == nil || o.Report.Confidence > best.Report.Confidence {
			best = o
		}
	}
	if best == nil {
		return nil
	}
	sort.Strings(contributors)
	rep := best.Report
	ans := &domain.ConsolidatedAnswer{
		SchemaVersion: domain.AnswerSchemaVersion,
		Question:      question,
		Answer:        rep.Answer,
		KeyPoints:     append([]string(nil), rep.KeyPoints...),
		References:    append([]domain.AnswerReference(nil), rep.References...),
		Confidence:    rep.Confidence,
		ReportedBy:    contributors,
	}
	ans.Caveats = append(ans.Caveats, fmt.Sprintf(
		"Deterministic consolidation (lead unavailable): the answer of %s, the most confident of %s. The other answers are kept below, unreconciled.",
		best.ID, joinIDs(contributors)))
	seenCaveat := map[string]bool{}
	for _, c := range rep.Caveats {
		seenCaveat[strings.ToLower(c)] = true
		ans.Caveats = append(ans.Caveats, c)
	}
	seenQuestion := map[string]bool{}
	for _, q := range rep.OpenQuestions {
		seenQuestion[strings.ToLower(q)] = true
		ans.OpenQuestions = append(ans.OpenQuestions, q)
	}
	for i := range outcomes {
		o := &outcomes[i]
		if o.Report == nil || o.ID == best.ID {
			continue
		}
		ans.OtherAnswers = append(ans.OtherAnswers, domain.AgentAnswer{AgentID: o.ID, Answer: o.Report.Answer})
		for _, c := range o.Report.Caveats {
			if k := strings.ToLower(c); !seenCaveat[k] {
				seenCaveat[k] = true
				ans.Caveats = append(ans.Caveats, c)
			}
		}
		for _, q := range o.Report.OpenQuestions {
			if k := strings.ToLower(q); !seenQuestion[k] {
				seenQuestion[k] = true
				ans.OpenQuestions = append(ans.OpenQuestions, q)
			}
		}
	}
	return ans
}

// FailedRespondents lists the agents without a usable answer.
func FailedRespondents(outcomes []AnswerOutcome) []domain.FailedReviewer {
	var out []domain.FailedReviewer
	for _, o := range outcomes {
		if o.Report == nil {
			reason := "no answer"
			if o.Err != nil {
				reason = o.Err.Error()
			}
			out = append(out, domain.FailedReviewer{ID: o.ID, Reason: reason})
		}
	}
	return out
}

func keepKnown(ids []string, known map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if known[id] && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

func maxAnswerBytes(lim agent.AnswerLimits) int {
	if lim.MaxAnswerBytes > 0 {
		return lim.MaxAnswerBytes
	}
	return agent.DefaultAnswerLimits.MaxAnswerBytes
}

func fieldBytes(lim agent.AnswerLimits) int {
	if lim.MaxFieldBytes > 0 {
		return lim.MaxFieldBytes
	}
	return agent.DefaultAnswerLimits.MaxFieldBytes
}
