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
