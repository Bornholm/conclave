package consolidation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/domain"
)

// TriageOutcome is the result of one agent on one issue.
type TriageOutcome struct {
	Number int64
	Agent  string
	Report *domain.TriageReport
	Err    error
}

// ParseLeadTriage decodes and validates the consolidated triage. Entries for
// an issue that was not in the batch are dropped, labels the forge does not
// define are dropped, and reported_by is restricted to reviewers that
// actually succeeded.
func ParseLeadTriage(raw []byte, issues []domain.Issue, succeeded []string, known map[string]string, maxLabels int) (*domain.ConsolidatedTriage, []string, error) {
	data, err := agent.ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var triage domain.ConsolidatedTriage
	if err := json.Unmarshal(data, &triage); err != nil {
		return nil, nil, fmt.Errorf("decode lead triage: %w", err)
	}
	if triage.SchemaVersion != domain.TriageSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", triage.SchemaVersion)
	}
	byNumber := make(map[int64]domain.Issue, len(issues))
	for _, i := range issues {
		byNumber[i.Number] = i
	}
	knownReviewers := make(map[string]bool, len(succeeded))
	for _, id := range succeeded {
		knownReviewers[id] = true
	}

	var warnings []string
	kept := triage.Issues[:0]
	seen := map[int64]bool{}
	for _, entry := range triage.Issues {
		issue, ok := byNumber[entry.Number]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("lead: issue #%d was not in the batch, dropped", entry.Number))
			continue
		}
		if seen[entry.Number] {
			warnings = append(warnings, fmt.Sprintf("lead: issue #%d appears twice, keeping the first", entry.Number))
			continue
		}
		seen[entry.Number] = true

		status := domain.NormalizeStatus(string(entry.Status))
		if status == "" {
			warnings = append(warnings, fmt.Sprintf("lead: issue #%d has an unknown status %q, dropped", entry.Number, entry.Status))
			continue
		}
		entry.Status = status
		if entry.Confidence < 0 || entry.Confidence > 1 {
			entry.Confidence = 0
		}
		var labelWarnings []string
		entry.Labels, labelWarnings = agent.NormalizeLabels(entry.Labels, known, maxLabels, fmt.Sprintf("lead #%d: ", entry.Number))
		warnings = append(warnings, labelWarnings...)
		entry.Summary = agent.TruncateField(entry.Summary, agent.DefaultLimits.MaxFieldBytes)
		entry.Evidence = agent.TruncateField(entry.Evidence, agent.DefaultLimits.MaxFieldBytes)
		entry.Question = agent.TruncateField(entry.Question, agent.DefaultLimits.MaxFieldBytes)
		if err := agent.CheckTriageStatusFields(entry.Status, entry.Evidence, entry.Question, entry.DuplicateOf, entry.Number); err != nil {
			warnings = append(warnings, fmt.Sprintf("lead: issue #%d %v, downgraded to needs-info", entry.Number, err))
			entry.Status = domain.TriageNeedsInfo
			if entry.Question == "" {
				entry.Question = "The triage could not be substantiated; a maintainer should confirm whether this is still valid."
			}
		}
		var by []string
		seenBy := map[string]bool{}
		for _, id := range entry.ReportedBy {
			id = strings.TrimSpace(id)
			if knownReviewers[id] && !seenBy[id] {
				by = append(by, id)
				seenBy[id] = true
			}
		}
		entry.ReportedBy = by

		entry.Title = issue.Title
		entry.WebURL = issue.WebURL
		entry.State = issue.State
		entry.CurrentLabels = issue.Labels
		kept = append(kept, entry)
	}
	triage.Issues = kept
	SortTriaged(triage.Issues)
	return &triage, warnings, nil
}

// FallbackTriage builds a triage without a lead: per issue, the report with
// the highest confidence wins, and the labels every report agrees on are kept.
func FallbackTriage(issues []domain.Issue, outcomes []TriageOutcome) *domain.ConsolidatedTriage {
	byNumber := map[int64][]TriageOutcome{}
	for _, o := range outcomes {
		if o.Report != nil {
			byNumber[o.Number] = append(byNumber[o.Number], o)
		}
	}
	triage := &domain.ConsolidatedTriage{
		SchemaVersion: domain.TriageSchemaVersion,
		Summary:       "Deterministic consolidation (lead unavailable).",
		Issues:        []domain.TriagedIssue{},
	}
	for _, issue := range issues {
		reports := byNumber[issue.Number]
		if len(reports) == 0 {
			continue
		}
		best := reports[0]
		for _, o := range reports[1:] {
			if o.Report.Confidence > best.Report.Confidence {
				best = o
			}
		}
		entry := domain.TriagedIssue{
			Number: issue.Number, Title: issue.Title, WebURL: issue.WebURL, State: issue.State,
			CurrentLabels: issue.Labels,
			Labels:        unionLabels(reports),
			Status:        best.Report.Status, Confidence: best.Report.Confidence,
			Summary: best.Report.Summary, Evidence: best.Report.Evidence,
			DuplicateOf: best.Report.DuplicateOf, Question: best.Report.Question,
		}
		for _, o := range reports {
			entry.ReportedBy = append(entry.ReportedBy, o.Agent)
		}
		sort.Strings(entry.ReportedBy)
		triage.Issues = append(triage.Issues, entry)
	}
	SortTriaged(triage.Issues)
	return triage
}

// unionLabels keeps every label at least one report proposed, in order.
func unionLabels(outcomes []TriageOutcome) []string {
	var out []string
	seen := map[string]bool{}
	for _, o := range outcomes {
		for _, l := range o.Report.Labels {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	return out
}

// SortTriaged orders by status, what needs action first, then by issue number.
func SortTriaged(is []domain.TriagedIssue) {
	sort.SliceStable(is, func(i, j int) bool {
		a, b := is[i], is[j]
		if a.Status.Rank() != b.Status.Rank() {
			return a.Status.Rank() < b.Status.Rank()
		}
		return a.Number < b.Number
	})
}

// FailedIssues lists the issues no agent could triage.
func FailedIssues(issues []domain.Issue, outcomes []TriageOutcome) []domain.FailedIssue {
	ok := map[int64]bool{}
	reasons := map[int64]string{}
	for _, o := range outcomes {
		if o.Report != nil {
			ok[o.Number] = true
		} else if o.Err != nil && reasons[o.Number] == "" {
			reasons[o.Number] = fmt.Sprintf("%s: %v", o.Agent, o.Err)
		}
	}
	var out []domain.FailedIssue
	for _, i := range issues {
		if ok[i.Number] {
			continue
		}
		reason := reasons[i.Number]
		if reason == "" {
			reason = "no report"
		}
		out = append(out, domain.FailedIssue{Number: i.Number, Reason: reason})
	}
	return out
}
