package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// PlanLimits bound one plan report.
type PlanLimits struct {
	MaxSteps      int
	MaxFieldBytes int
}

// ParsePlanReport decodes and validates the plan of one issue. A step that
// cannot be acted on is dropped with a warning; a report without a single
// usable step is an error, because that is not a plan.
func ParsePlanReport(raw []byte, agentID string, number int64, lim PlanLimits) (*domain.PlanReport, []string, error) {
	data, err := ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var rep domain.PlanReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, nil, fmt.Errorf("decode plan report: %w", err)
	}
	if rep.SchemaVersion != domain.PlanSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", rep.SchemaVersion)
	}
	if rep.Reviewer.ID != agentID {
		return nil, nil, fmt.Errorf("reviewer id %q does not match agent %q", rep.Reviewer.ID, agentID)
	}
	if rep.Number != number {
		return nil, nil, fmt.Errorf("report is about issue #%d, expected #%d", rep.Number, number)
	}
	if rep.Confidence < 0 || rep.Confidence > 1 {
		return nil, nil, fmt.Errorf("confidence %v out of [0,1]", rep.Confidence)
	}
	if lim.MaxFieldBytes <= 0 {
		lim.MaxFieldBytes = DefaultLimits.MaxFieldBytes
	}

	var warnings []string
	if effort := domain.NormalizeEffort(string(rep.Effort)); effort == "" {
		if strings.TrimSpace(string(rep.Effort)) != "" {
			warnings = append(warnings, fmt.Sprintf("unknown effort %q, dropped", truncate(string(rep.Effort), 60)))
		}
		rep.Effort = ""
	} else {
		rep.Effort = effort
	}
	rep.Understanding = truncate(strings.TrimSpace(rep.Understanding), lim.MaxFieldBytes)
	rep.Approach = truncate(strings.TrimSpace(rep.Approach), lim.MaxFieldBytes)
	if rep.Approach == "" {
		return nil, warnings, fmt.Errorf("the plan states no approach")
	}
	var stepWarnings []string
	rep.Steps, stepWarnings = NormalizePlanSteps(rep.Steps, agentID, lim, "")
	warnings = append(warnings, stepWarnings...)
	if len(rep.Steps) == 0 {
		return nil, warnings, fmt.Errorf("the plan holds no usable step")
	}
	rep.Alternatives = normalizeAlternatives(rep.Alternatives, lim.MaxFieldBytes)
	rep.Risks = normalizeRisks(rep.Risks, lim.MaxFieldBytes)
	rep.Tests = normalizeLines(rep.Tests, lim.MaxFieldBytes)
	rep.OpenQuestions = normalizeLines(rep.OpenQuestions, lim.MaxFieldBytes)
	return &rep, warnings, nil
}

// NormalizePlanSteps validates the steps of a plan: a step needs a title and
// details, ids are made unique, paths are checked for shape but not for
// existence (a step may create a file), and depends_on may only name a step
// that comes before it.
func NormalizePlanSteps(steps []domain.PlanStep, agentID string, lim PlanLimits, prefix string) ([]domain.PlanStep, []string) {
	if lim.MaxFieldBytes <= 0 {
		lim.MaxFieldBytes = DefaultLimits.MaxFieldBytes
	}
	var out []domain.PlanStep
	var warnings []string
	seen := map[string]bool{}
	for i, s := range steps {
		if lim.MaxSteps > 0 && len(out) >= lim.MaxSteps {
			warnings = append(warnings, fmt.Sprintf("%sdropped steps beyond the limit of %d", prefix, lim.MaxSteps))
			break
		}
		s.Title = truncate(strings.TrimSpace(s.Title), 512)
		s.Details = truncate(strings.TrimSpace(s.Details), lim.MaxFieldBytes)
		if s.Title == "" || s.Details == "" {
			warnings = append(warnings, fmt.Sprintf("%sstep %d (%q) rejected: a step needs a title and details", prefix, i+1, truncate(s.Title, 60)))
			continue
		}
		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" || seen[s.ID] {
			s.ID = fmt.Sprintf("%s-%d", stepPrefix(agentID), len(out)+1)
		}
		seen[s.ID] = true
		s.Validation = truncate(strings.TrimSpace(s.Validation), lim.MaxFieldBytes)
		var files []string
		for _, p := range s.Files {
			clean, err := cleanPath(p)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%sstep %s: path %q dropped: %v", prefix, s.ID, truncate(p, 60), err))
				continue
			}
			files = append(files, clean)
		}
		s.Files = files
		var deps []string
		for _, d := range s.DependsOn {
			d = strings.TrimSpace(d)
			if d == s.ID || !seen[d] {
				warnings = append(warnings, fmt.Sprintf("%sstep %s: depends_on %q is not an earlier step, dropped", prefix, s.ID, truncate(d, 60)))
				continue
			}
			deps = append(deps, d)
		}
		s.DependsOn = deps
		out = append(out, s)
	}
	return out, warnings
}

// stepPrefix keeps generated step ids short and traceable to their author.
func stepPrefix(agentID string) string {
	if agentID == "" {
		return "step"
	}
	return agentID
}

func normalizeAlternatives(as []domain.PlanAlternative, max int) []domain.PlanAlternative {
	var out []domain.PlanAlternative
	for _, a := range as {
		a.Approach = truncate(strings.TrimSpace(a.Approach), max)
		a.WhyNot = truncate(strings.TrimSpace(a.WhyNot), max)
		if a.Approach == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func normalizeRisks(rs []domain.PlanRisk, max int) []domain.PlanRisk {
	var out []domain.PlanRisk
	for _, r := range rs {
		r.Description = truncate(strings.TrimSpace(r.Description), max)
		r.Mitigation = truncate(strings.TrimSpace(r.Mitigation), max)
		if r.Description == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

func normalizeLines(lines []string, max int) []string {
	var out []string
	for _, l := range lines {
		if l = truncate(strings.TrimSpace(l), max); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// NormalizePlanLines is the exported form used to validate the lead output.
func NormalizePlanLines(lines []string, max int) []string { return normalizeLines(lines, max) }

// NormalizePlanAlternatives is the exported form used to validate the lead output.
func NormalizePlanAlternatives(as []domain.PlanAlternative, max int) []domain.PlanAlternative {
	return normalizeAlternatives(as, max)
}

// NormalizePlanRisks is the exported form used to validate the lead output.
func NormalizePlanRisks(rs []domain.PlanRisk, max int) []domain.PlanRisk {
	return normalizeRisks(rs, max)
}
