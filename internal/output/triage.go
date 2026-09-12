package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// TriageJSON writes the triage as indented JSON.
func TriageJSON(w io.Writer, triage *domain.ConsolidatedTriage) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(triage)
}

var statusHeadings = map[domain.TriageStatus]string{
	domain.TriageStillPresent: "Still present",
	domain.TriageFixed:        "Fixed",
	domain.TriageObsolete:     "Obsolete",
	domain.TriageDuplicate:    "Duplicates",
	domain.TriageNeedsInfo:    "Needs information",
}

// TriageMarkdown writes the triage as a Markdown document, grouped by status.
func TriageMarkdown(w io.Writer, triage *domain.ConsolidatedTriage, opts Options) error {
	var b strings.Builder
	m := triage.Meta
	b.WriteString("## Conclave triage\n\n")
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
	if m.Total > 0 {
		fmt.Fprintf(&b, "**Issues:** %d triaged of %d", m.Succeeded, m.Total)
		if !m.LeadUsed && m.LeadID != "" {
			b.WriteString(" (lead unavailable, deterministic consolidation)")
		}
		b.WriteString("  \n")
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
	if s := strings.TrimSpace(triage.Summary); s != "" {
		b.WriteString("\n" + s + "\n")
	}

	if len(triage.Issues) == 0 {
		b.WriteString("\n_No issue was triaged._\n")
	}
	var current domain.TriageStatus = "none"
	for _, issue := range triage.Issues {
		if issue.Status != current {
			current = issue.Status
			heading := statusHeadings[current]
			if heading == "" {
				heading = string(current)
			}
			fmt.Fprintf(&b, "\n### %s (%d)\n", heading, countStatus(triage.Issues, current))
		}
		fmt.Fprintf(&b, "\n#### #%d %s\n\n", issue.Number, escape(issue.Title))
		if issue.WebURL != "" {
			fmt.Fprintf(&b, "**Link:** %s  \n", issue.WebURL)
		}
		writeLabels(&b, issue)
		fmt.Fprintf(&b, "**Confidence:** %d%%  \n", int(issue.Confidence*100+0.5))
		if opts.ShowAttribution && len(issue.ReportedBy) > 0 {
			fmt.Fprintf(&b, "**Reported by:** %s  \n", strings.Join(issue.ReportedBy, ", "))
		}
		if issue.DuplicateOf > 0 {
			fmt.Fprintf(&b, "**Duplicate of:** #%d  \n", issue.DuplicateOf)
		}
		if s := strings.TrimSpace(issue.Summary); s != "" {
			b.WriteString("\n" + s + "\n")
		}
		if e := strings.TrimSpace(issue.Evidence); e != "" {
			b.WriteString("\n**Evidence:** " + e + "\n")
		}
		if q := strings.TrimSpace(issue.Question); q != "" {
			b.WriteString("\n**Question:** " + q + "\n")
		}
	}

	if len(triage.Failed) > 0 {
		b.WriteString("\n### Not triaged\n\n")
		for _, f := range triage.Failed {
			fmt.Fprintf(&b, "- #%d: %s\n", f.Number, f.Reason)
		}
	}
	if len(triage.Warnings) > 0 {
		b.WriteString("\n### Warnings\n\n")
		for _, w := range triage.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	if m.RunID != "" {
		fmt.Fprintf(&b, "\n---\n_Run `%s`_\n", m.RunID)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeLabels shows the proposed labels and marks the ones that would be added.
func writeLabels(b *strings.Builder, issue domain.TriagedIssue) {
	if len(issue.Labels) == 0 && len(issue.CurrentLabels) == 0 {
		return
	}
	if len(issue.Labels) == 0 {
		fmt.Fprintf(b, "**Labels:** none proposed (current: %s)  \n", strings.Join(issue.CurrentLabels, ", "))
		return
	}
	fmt.Fprintf(b, "**Labels:** %s", strings.Join(issue.Labels, ", "))
	if added := issue.AddedLabels(); len(added) > 0 {
		fmt.Fprintf(b, " (to add: %s)", strings.Join(added, ", "))
	}
	b.WriteString("  \n")
}

func countStatus(issues []domain.TriagedIssue, status domain.TriageStatus) int {
	n := 0
	for _, i := range issues {
		if i.Status == status {
			n++
		}
	}
	return n
}
