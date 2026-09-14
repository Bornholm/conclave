package consolidation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/domain"
)

// PlanOutcome is the result of one agent planning one issue.
type PlanOutcome struct {
	ID     string
	Report *domain.PlanReport
	Err    error
}

// ParseLeadPlan decodes and validates the consolidated plan. Steps that
// cannot be acted on are dropped with a warning, and reported_by is
// restricted to planners that actually succeeded.
func ParseLeadPlan(raw []byte, issue *domain.Issue, succeeded []string, maxSteps int) (*domain.ConsolidatedPlan, []string, error) {
	data, err := agent.ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var plan domain.ConsolidatedPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, nil, fmt.Errorf("decode lead plan: %w", err)
	}
	if plan.SchemaVersion != domain.PlanSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", plan.SchemaVersion)
	}
	if plan.Number != 0 && plan.Number != issue.Number {
		return nil, nil, fmt.Errorf("plan is about issue #%d, expected #%d", plan.Number, issue.Number)
	}
	plan.Number = issue.Number
	if plan.Confidence < 0 || plan.Confidence > 1 {
		plan.Confidence = 0
	}
	lim := agent.PlanLimits{MaxSteps: maxSteps, MaxFieldBytes: agent.DefaultLimits.MaxFieldBytes}

	var warnings []string
	if effort := domain.NormalizeEffort(string(plan.Effort)); effort == "" {
		if strings.TrimSpace(string(plan.Effort)) != "" {
			warnings = append(warnings, fmt.Sprintf("lead: unknown effort %q, dropped", plan.Effort))
		}
		plan.Effort = ""
	} else {
		plan.Effort = effort
	}
	plan.Summary = agent.TruncateField(plan.Summary, lim.MaxFieldBytes)
	plan.Understanding = agent.TruncateField(plan.Understanding, lim.MaxFieldBytes)
	plan.Approach = agent.TruncateField(plan.Approach, lim.MaxFieldBytes)
	if plan.Approach == "" {
		return nil, warnings, fmt.Errorf("the plan states no approach")
	}
	var stepWarnings []string
	plan.Steps, stepWarnings = agent.NormalizePlanSteps(plan.Steps, "step", lim, "lead: ")
	warnings = append(warnings, stepWarnings...)
	if len(plan.Steps) == 0 {
		return nil, warnings, fmt.Errorf("the plan holds no usable step")
	}
	plan.Alternatives = agent.NormalizePlanAlternatives(plan.Alternatives, lim.MaxFieldBytes)
	plan.Risks = agent.NormalizePlanRisks(plan.Risks, lim.MaxFieldBytes)
	plan.Tests = agent.NormalizePlanLines(plan.Tests, lim.MaxFieldBytes)
	plan.OpenQuestions = agent.NormalizePlanLines(plan.OpenQuestions, lim.MaxFieldBytes)

	known := make(map[string]bool, len(succeeded))
	for _, id := range succeeded {
		known[id] = true
	}
	var by []string
	seen := map[string]bool{}
	for _, id := range plan.ReportedBy {
		id = strings.TrimSpace(id)
		if known[id] && !seen[id] {
			by = append(by, id)
			seen[id] = true
		}
	}
	plan.ReportedBy = by
	plan.Title, plan.WebURL = issue.Title, issue.WebURL
	return &plan, warnings, nil
}

// FallbackPlan builds a plan without a lead: the most confident plan wins
// whole, and the approaches the others proposed become alternatives. Merging
// steps that belong to different approaches would produce a plan nobody
// wrote, so the fallback never does it.
func FallbackPlan(issue *domain.Issue, outcomes []PlanOutcome) *domain.ConsolidatedPlan {
	var best *PlanOutcome
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
	plan := &domain.ConsolidatedPlan{
		SchemaVersion: domain.PlanSchemaVersion,
		Number:        issue.Number,
		Title:         issue.Title,
		WebURL:        issue.WebURL,
		Summary:       fmt.Sprintf("Deterministic consolidation (lead unavailable): the plan of %s, the most confident of %s.", best.ID, joinIDs(contributors)),
		Understanding: rep.Understanding,
		Approach:      rep.Approach,
		Alternatives:  append([]domain.PlanAlternative(nil), rep.Alternatives...),
		Steps:         append([]domain.PlanStep(nil), rep.Steps...),
		Tests:         append([]string(nil), rep.Tests...),
		Risks:         append([]domain.PlanRisk(nil), rep.Risks...),
		Effort:        rep.Effort,
		Confidence:    rep.Confidence,
		ReportedBy:    contributors,
	}
	// Nothing the other planners raised is lost: their approach is recorded
	// as an alternative and their questions are unioned.
	seenQuestion := map[string]bool{}
	for _, q := range rep.OpenQuestions {
		seenQuestion[strings.ToLower(q)] = true
		plan.OpenQuestions = append(plan.OpenQuestions, q)
	}
	for i := range outcomes {
		o := &outcomes[i]
		if o.Report == nil || o.ID == best.ID {
			continue
		}
		plan.Alternatives = append(plan.Alternatives, domain.PlanAlternative{
			Approach: o.Report.Approach,
			WhyNot:   fmt.Sprintf("proposed by %s, not retained: %s was more confident", o.ID, best.ID),
		})
		for _, q := range o.Report.OpenQuestions {
			if k := strings.ToLower(q); !seenQuestion[k] {
				seenQuestion[k] = true
				plan.OpenQuestions = append(plan.OpenQuestions, q)
			}
		}
	}
	return plan
}

// FailedPlanners lists the planners without a usable plan.
func FailedPlanners(outcomes []PlanOutcome) []domain.FailedReviewer {
	var out []domain.FailedReviewer
	for _, o := range outcomes {
		if o.Report == nil {
			reason := "no plan"
			if o.Err != nil {
				reason = o.Err.Error()
			}
			out = append(out, domain.FailedReviewer{ID: o.ID, Reason: reason})
		}
	}
	return out
}

func joinIDs(ids []string) string {
	if len(ids) == 0 {
		return "no agent"
	}
	return strings.Join(ids, ", ")
}
