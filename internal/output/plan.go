package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// PlanJSON writes the plan as indented JSON.
func PlanJSON(w io.Writer, plan *domain.ConsolidatedPlan) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(plan)
}

// PlanMarkdown writes the implementation plan as a Markdown document, in the
// order a developer works through it.
func PlanMarkdown(w io.Writer, plan *domain.ConsolidatedPlan, opts Options) error {
	var b strings.Builder
	m := plan.Meta
	fmt.Fprintf(&b, "## Conclave plan — #%d %s\n\n", plan.Number, escape(plan.Title))
	if plan.WebURL != "" {
		fmt.Fprintf(&b, "**Link:** %s  \n", plan.WebURL)
	}
	if m.Repository != "" {
		fmt.Fprintf(&b, "**Repository:** %s  \n", m.Repository)
	}
	if m.HeadSHA != "" {
		fmt.Fprintf(&b, "**Revision:** `%s`", short(m.HeadSHA))
		if m.Branch != "" {
			fmt.Fprintf(&b, " (%s)", m.Branch)
		}
		b.WriteString("  \n")
	}
	if plan.Effort != "" {
		fmt.Fprintf(&b, "**Effort:** %s  \n", plan.Effort)
	}
	fmt.Fprintf(&b, "**Confidence:** %d%%  \n", int(plan.Confidence*100+0.5))
	if m.PlannersTotal > 0 {
		fmt.Fprintf(&b, "**Planners:** %d of %d", m.PlannersSucceeded, m.PlannersTotal)
		if !m.LeadUsed && m.LeadID != "" {
			b.WriteString(" (lead unavailable, deterministic consolidation)")
		}
		b.WriteString("  \n")
	}
	if opts.ShowAttribution && len(plan.ReportedBy) > 0 {
		fmt.Fprintf(&b, "**Planned by:** %s  \n", strings.Join(plan.ReportedBy, ", "))
	}
	if len(m.Models) > 0 {
		ids := make([]string, 0, len(m.Models))
		for id := range m.Models {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf("%s (%s)", id, m.Models[id]))
		}
		fmt.Fprintf(&b, "**Models:** %s  \n", strings.Join(parts, ", "))
	}
	if s := strings.TrimSpace(plan.Summary); s != "" {
		b.WriteString("\n" + s + "\n")
	}
	if s := strings.TrimSpace(plan.Understanding); s != "" {
		b.WriteString("\n### What the ticket asks\n\n" + s + "\n")
	}
	if s := strings.TrimSpace(plan.Approach); s != "" {
		b.WriteString("\n### Approach\n\n" + s + "\n")
	}
	if len(plan.Alternatives) > 0 {
		b.WriteString("\n### Alternatives considered\n\n")
		for _, alt := range plan.Alternatives {
			fmt.Fprintf(&b, "- **%s**", escape(alt.Approach))
			if w := strings.TrimSpace(alt.WhyNot); w != "" {
				fmt.Fprintf(&b, " — %s", escape(w))
			}
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "\n### Steps (%d)\n", len(plan.Steps))
	for i, s := range plan.Steps {
		fmt.Fprintf(&b, "\n#### %d. %s\n\n", i+1, escape(s.Title))
		if len(s.Files) > 0 {
			fmt.Fprintf(&b, "**Files:** %s  \n", codeList(s.Files))
		}
		if len(s.DependsOn) > 0 {
			fmt.Fprintf(&b, "**Depends on:** %s  \n", strings.Join(s.DependsOn, ", "))
		}
		if d := strings.TrimSpace(s.Details); d != "" {
			b.WriteString("\n" + d + "\n")
		}
		if v := strings.TrimSpace(s.Validation); v != "" {
			b.WriteString("\n**Done when:** " + v + "\n")
		}
	}

	if len(plan.Tests) > 0 {
		b.WriteString("\n### Tests\n\n")
		for _, t := range plan.Tests {
			fmt.Fprintf(&b, "- %s\n", escape(t))
		}
	}
	if len(plan.Risks) > 0 {
		b.WriteString("\n### Risks\n\n")
		for _, r := range plan.Risks {
			fmt.Fprintf(&b, "- %s", escape(r.Description))
			if m := strings.TrimSpace(r.Mitigation); m != "" {
				fmt.Fprintf(&b, " — _mitigation:_ %s", escape(m))
			}
			b.WriteString("\n")
		}
	}
	if len(plan.OpenQuestions) > 0 {
		b.WriteString("\n### Open questions\n\n")
		for _, q := range plan.OpenQuestions {
			fmt.Fprintf(&b, "- %s\n", escape(q))
		}
	}
	if opts.ShowFailedAgents && len(plan.Failed) > 0 {
		b.WriteString("\n### Planners that failed\n\n")
		for _, f := range plan.Failed {
			fmt.Fprintf(&b, "- %s: %s\n", f.ID, escape(f.Reason))
		}
	}
	if len(plan.Warnings) > 0 {
		b.WriteString("\n### Warnings\n\n")
		for _, w := range plan.Warnings {
			fmt.Fprintf(&b, "- %s\n", escape(w))
		}
	}
	if m.RunID != "" {
		fmt.Fprintf(&b, "\n---\n_Run `%s`_\n", m.RunID)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func codeList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, i := range items {
		quoted = append(quoted, "`"+i+"`")
	}
	return strings.Join(quoted, ", ")
}
