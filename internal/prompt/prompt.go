// Package prompt renders the reviewer and lead prompts.
package prompt

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/bornholm/conclave/internal/domain"
)

//go:embed templates/*.tmpl
var files embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"join":    strings.Join,
	"minutes": minutes,
	"pid":     domain.PreviousID,
	"clip":    clip,
}).ParseFS(files, "templates/*.tmpl"))

// clip shortens s to about n bytes, on a rune boundary, on one line: a
// previous finding is recalled, not restated, and its full text is a read
// away in the run directory.
func clip(n int, s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Since is what an incremental review adds to the prompts: the previous
// review, and what changed since.
type Since struct {
	Previous *domain.PreviousReview
	// Note says why the diff is missing or is not the plain increment.
	Note      string
	Diff      string
	Truncated bool
	// DiffFile, when set, is the path of the diff relative to the worktree:
	// too large to inline, Diff is then left out.
	DiffFile string
	// ByGit, when the configuration keeps diffs out of the prompt, gives
	// the agents the git command instead.
	ByGit bool
}

// TimeBudget is the wall-clock time an agent has before Conclave stops it.
// The zero value leaves the budget out of the prompt.
type TimeBudget struct {
	Duration time.Duration
	// Deadline is when the agent is stopped, in UTC.
	Deadline time.Time
}

// NewTimeBudget starts a budget of d at now.
func NewTimeBudget(now time.Time, d time.Duration) TimeBudget {
	return TimeBudget{Duration: d, Deadline: now.Add(d).UTC().Truncate(time.Second)}
}

// minutes renders a budget the way a person would say it.
func minutes(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Round(time.Second)/time.Second))
	}
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case m == 1:
		return "1 minute"
	default:
		return fmt.Sprintf("%d minutes", m)
	}
}

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
	// DiffExcluded lists the path patterns left out of the diff.
	DiffExcluded []string
	// DiffFile, when set, is the path of the diff relative to the worktree:
	// the diff is too large to inline and Diff is then left out.
	DiffFile  string
	DiffIndex DiffIndex
	// DiffSince says the review is incremental: the diff is given as a file
	// whatever its size, and the changes since the previous review are where
	// the reviewer starts.
	DiffSince bool
	// DiffLarge says the diff is too large to inline.
	DiffLarge   bool
	MaxFindings int
	Budget      TimeBudget
	// Since is set for an incremental review.
	Since Since
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
	Budget             TimeBudget
	Since              Since
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

// ReviewerRetryInput feeds the prompt of a reviewer run again because its
// first run returned no usable report.
type ReviewerRetryInput struct {
	AgentID      string
	Worktree     string
	PR           *domain.PullRequest
	MergeBaseSHA string
	HeadSHA      string
	// Problem says in one sentence what was wrong with the first run.
	Problem string
	// Transcript is the end of what the agent wrote in its first run.
	Transcript  string
	MaxFindings int
	Budget      TimeBudget
	Since       Since
}

// ReviewerRetry renders the prompt asking a reviewer for the report its
// previous run did not produce.
func ReviewerRetry(in ReviewerRetryInput) ([]byte, error) {
	data := struct {
		ReviewerRetryInput
		common
	}{in, commonFields(ReviewerSchema)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "reviewer-retry.tmpl", data); err != nil {
		return nil, fmt.Errorf("render reviewer retry prompt: %w", err)
	}
	return buf.Bytes(), nil
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
	Budget      TimeBudget
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
	Budget             TimeBudget
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
	Budget      TimeBudget
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
	Budget            TimeBudget
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

// AskInput feeds the ask template for one question.
type AskInput struct {
	AgentID     string
	Worktree    string
	Specialties []string
	// HasProject says whether the worktree holds the project the question is
	// about. When false the worktree is an empty scratch directory.
	HasProject bool
	Branch     string
	HeadSHA    string
	Question   string
	Context    string
	Budget     TimeBudget
}

// LeadAskInput feeds the lead ask template.
type LeadAskInput struct {
	AgentID              string
	Worktree             string
	HasProject           bool
	Branch               string
	HeadSHA              string
	Question             string
	Context              string
	Reports              []domain.AnswerReport
	SucceededRespondents []string
	Budget               TimeBudget
}

type askCommon struct {
	Schema        string
	SchemaVersion string
}

// Ask renders the prompt for answering one question.
func Ask(in AskInput) ([]byte, error) {
	data := struct {
		AskInput
		askCommon
	}{in, askCommon{Schema: AskSchema, SchemaVersion: domain.AnswerSchemaVersion}}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "ask.tmpl", data); err != nil {
		return nil, fmt.Errorf("render ask prompt: %w", err)
	}
	return buf.Bytes(), nil
}

// LeadAsk renders the prompt consolidating the answers to one question.
func LeadAsk(in LeadAskInput) ([]byte, error) {
	reports, err := json.MarshalIndent(in.Reports, "", "  ")
	if err != nil {
		return nil, err
	}
	data := struct {
		LeadAskInput
		askCommon
		ReportsJSON string
	}{in, askCommon{Schema: LeadAskSchema, SchemaVersion: domain.AnswerSchemaVersion}, string(reports)}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "lead-ask.tmpl", data); err != nil {
		return nil, fmt.Errorf("render lead ask prompt: %w", err)
	}
	return buf.Bytes(), nil
}
