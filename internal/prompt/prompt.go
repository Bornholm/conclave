// Package prompt renders the reviewer and lead prompts.
package prompt

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/bornholm/conclave/internal/domain"
)

//go:embed templates/*.tmpl
var files embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"join": strings.Join,
}).ParseFS(files, "templates/*.tmpl"))

// ReviewerInput feeds the reviewer template.
type ReviewerInput struct {
	AgentID       string
	Worktree      string
	Specialties   []string
	PR            *domain.PullRequest
	MergeBaseSHA  string
	HeadSHA       string
	IncludeFiles  bool
	Diff          string
	DiffTruncated bool
	MaxFindings   int
}

// LeadInput feeds the lead template.
type LeadInput struct {
	AgentID            string
	Worktree           string
	PR                 *domain.PullRequest
	MergeBaseSHA       string
	HeadSHA            string
	SucceededReviewers []string
	FailedReviewers    []domain.FailedReviewer
	Reports            []domain.AgentReport
	Groups             []domain.FindingGroup
	MaxFindings        int
}

type common struct {
	Schema        string
	SchemaVersion string
	Severities    []string
	Categories    []string
}

func commonFields(schema string) common {
	c := common{Schema: schema, SchemaVersion: domain.ReportSchemaVersion}
	for _, s := range domain.Severities {
		c.Severities = append(c.Severities, string(s))
	}
	for _, s := range domain.Categories {
		c.Categories = append(c.Categories, string(s))
	}
	return c
}

// Reviewer renders the prompt given to a reviewer agent.
func Reviewer(in ReviewerInput) ([]byte, error) {
	data := struct {
		ReviewerInput
		common
	}{in, commonFields(ReviewerSchema)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "reviewer.tmpl", data); err != nil {
		return nil, fmt.Errorf("render reviewer prompt: %w", err)
	}
	return buf.Bytes(), nil
}

// Lead renders the prompt given to the lead agent.
func Lead(in LeadInput) ([]byte, error) {
	reports, err := json.MarshalIndent(in.Reports, "", "  ")
	if err != nil {
		return nil, err
	}
	groups, err := json.MarshalIndent(in.Groups, "", "  ")
	if err != nil {
		return nil, err
	}
	data := struct {
		LeadInput
		common
		ReportsJSON string
		GroupsJSON  string
	}{in, commonFields(LeadSchema), string(reports), string(groups)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "lead.tmpl", data); err != nil {
		return nil, fmt.Errorf("render lead prompt: %w", err)
	}
	return buf.Bytes(), nil
}

// TriageInput feeds the triage template for one issue.
type TriageInput struct {
	AgentID     string
	Worktree    string
	Branch      string
	HeadSHA     string
	Issue       *domain.Issue
	Labels      []domain.Label
	MaxLabels   int
	OtherIssues []domain.Issue
}

// LeadTriageInput feeds the lead triage template for a whole batch.
type LeadTriageInput struct {
	AgentID            string
	Worktree           string
	Branch             string
	HeadSHA            string
	Labels             []domain.Label
	MaxLabels          int
	Issues             []domain.Issue
	Reports            []domain.TriageReport
	SucceededReviewers []string
}

type triageCommon struct {
	Schema        string
	SchemaVersion string
	Statuses      []string
}

func triageFields(schema string) triageCommon {
	c := triageCommon{Schema: schema, SchemaVersion: domain.TriageSchemaVersion}
	for _, s := range domain.TriageStatuses {
		c.Statuses = append(c.Statuses, string(s))
	}
	return c
}

// Triage renders the prompt for triaging one issue.
func Triage(in TriageInput) ([]byte, error) {
	data := struct {
		TriageInput
		triageCommon
	}{in, triageFields(TriageSchema)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "triage.tmpl", data); err != nil {
		return nil, fmt.Errorf("render triage prompt: %w", err)
	}
	return buf.Bytes(), nil
}

// LeadTriage renders the prompt consolidating a triage batch.
func LeadTriage(in LeadTriageInput) ([]byte, error) {
	// The lead needs the issue text to check the reports, but not the whole
	// repository context: the worktree is there for that.
	issues, err := json.MarshalIndent(in.Issues, "", "  ")
	if err != nil {
		return nil, err
	}
	reports, err := json.MarshalIndent(in.Reports, "", "  ")
	if err != nil {
		return nil, err
	}
	data := struct {
		LeadTriageInput
		triageCommon
		IssuesJSON  string
		ReportsJSON string
	}{in, triageFields(LeadTriageSchema), string(issues), string(reports)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "lead-triage.tmpl", data); err != nil {
		return nil, fmt.Errorf("render lead triage prompt: %w", err)
	}
	return buf.Bytes(), nil
}

// PlanInput feeds the plan template for one issue.
type PlanInput struct {
	AgentID     string
	Worktree    string
	Branch      string
	HeadSHA     string
	Issue       *domain.Issue
	Specialties []string
	MaxSteps    int
}

// LeadPlanInput feeds the lead plan template.
type LeadPlanInput struct {
	AgentID           string
	Worktree          string
	Branch            string
	HeadSHA           string
	Issue             *domain.Issue
	Reports           []domain.PlanReport
	SucceededPlanners []string
	MaxSteps          int
}

type planCommon struct {
	Schema        string
	SchemaVersion string
	Efforts       []string
}

func planFields(schema string) planCommon {
	c := planCommon{Schema: schema, SchemaVersion: domain.PlanSchemaVersion}
	for _, e := range domain.PlanEfforts {
		c.Efforts = append(c.Efforts, string(e))
	}
	return c
}

// Plan renders the prompt for planning one issue.
func Plan(in PlanInput) ([]byte, error) {
	data := struct {
		PlanInput
		planCommon
	}{in, planFields(PlanSchema)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "plan.tmpl", data); err != nil {
		return nil, fmt.Errorf("render plan prompt: %w", err)
	}
	return buf.Bytes(), nil
}

// LeadPlan renders the prompt consolidating the plans of one issue.
func LeadPlan(in LeadPlanInput) ([]byte, error) {
	// The lead gets the plans and the discussion as JSON: the issue body is
	// rendered in the template, and the worktree carries everything else.
	reports, err := json.MarshalIndent(in.Reports, "", "  ")
	if err != nil {
		return nil, err
	}
	comments, err := json.MarshalIndent(in.Issue.Comments, "", "  ")
	if err != nil {
		return nil, err
	}
	data := struct {
		LeadPlanInput
		planCommon
		ReportsJSON  string
		CommentsJSON string
	}{in, planFields(LeadPlanSchema), string(reports), string(comments)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "lead-plan.tmpl", data); err != nil {
		return nil, fmt.Errorf("render lead plan prompt: %w", err)
	}
	return buf.Bytes(), nil
}
