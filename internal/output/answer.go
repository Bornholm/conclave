package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// AnswerJSON writes the consolidated answer as indented JSON.
func AnswerJSON(w io.Writer, answer *domain.ConsolidatedAnswer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(answer)
}

// AnswerMarkdown writes the consolidated answer as a Markdown document. The
// answer comes first: everything else is there to let the reader weigh it.
func AnswerMarkdown(w io.Writer, answer *domain.ConsolidatedAnswer, opts Options) error {
	var b strings.Builder
	m := answer.Meta
	b.WriteString("## Conclave answer\n\n")
	if q := strings.TrimSpace(answer.Question); q != "" {
		b.WriteString("> " + strings.ReplaceAll(q, "\n", "\n> ") + "\n\n")
	}
	if m.Repository != "" {
		fmt.Fprintf(&b, "**Repository:** %s  \n", m.Repository)
	} else if m.Project != "" {
		fmt.Fprintf(&b, "**Project:** %s  \n", m.Project)
	}
	if m.HeadSHA != "" {
		fmt.Fprintf(&b, "**Revision:** `%s`", short(m.HeadSHA))
		if m.Branch != "" {
			fmt.Fprintf(&b, " (%s)", m.Branch)
		}
		b.WriteString("  \n")
	}
	fmt.Fprintf(&b, "**Confidence:** %d%%  \n", int(answer.Confidence*100+0.5))
	if m.RespondentsTotal > 0 {
		fmt.Fprintf(&b, "**Agents:** %d of %d", m.RespondentsSucceeded, m.RespondentsTotal)
		if !m.LeadUsed && m.LeadID != "" {
			b.WriteString(" (lead unavailable, deterministic consolidation)")
		}
		b.WriteString("  \n")
	}
	if opts.ShowAttribution && len(answer.ReportedBy) > 0 {
		fmt.Fprintf(&b, "**Answered by:** %s  \n", strings.Join(answer.ReportedBy, ", "))
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
	if len(answer.KeyPoints) > 0 {
		b.WriteString("\n### In short\n\n")
		for _, p := range answer.KeyPoints {
			fmt.Fprintf(&b, "- %s\n", escape(p))
		}
	}
	if a := strings.TrimSpace(answer.Answer); a != "" {
		b.WriteString("\n### Answer\n\n" + a + "\n")
	}
	if len(answer.Disagreements) > 0 {
		b.WriteString("\n### Where the agents disagreed\n\n")
		for _, d := range answer.Disagreements {
			fmt.Fprintf(&b, "- **%s**\n", escape(d.Topic))
			for _, p := range d.Positions {
				fmt.Fprintf(&b, "  - %s: %s\n", strings.Join(p.By, ", "), escape(p.Position))
			}
		}
	}
	if len(answer.References) > 0 {
		b.WriteString("\n### References\n\n")
		for _, r := range answer.References {
			fmt.Fprintf(&b, "- `%s`", r.Source)
			if r.Line > 0 {
				fmt.Fprintf(&b, ":%d", r.Line)
			}
			if n := strings.TrimSpace(r.Note); n != "" {
				fmt.Fprintf(&b, " — %s", escape(n))
			}
			b.WriteString("\n")
		}
	}
	if len(answer.Caveats) > 0 {
		b.WriteString("\n### Caveats\n\n")
		for _, c := range answer.Caveats {
			fmt.Fprintf(&b, "- %s\n", escape(c))
		}
	}
	if len(answer.OpenQuestions) > 0 {
		b.WriteString("\n### Open questions\n\n")
		for _, q := range answer.OpenQuestions {
			fmt.Fprintf(&b, "- %s\n", escape(q))
		}
	}
	for _, other := range answer.OtherAnswers {
		fmt.Fprintf(&b, "\n### Answer from %s\n\n%s\n", other.AgentID, strings.TrimSpace(other.Answer))
	}
	if opts.ShowFailedAgents && len(answer.Failed) > 0 {
		b.WriteString("\n### Agents that failed\n\n")
		for _, f := range answer.Failed {
			fmt.Fprintf(&b, "- %s: %s\n", f.ID, escape(f.Reason))
		}
	}
	if len(answer.Warnings) > 0 {
		b.WriteString("\n### Warnings\n\n")
		for _, w := range answer.Warnings {
			fmt.Fprintf(&b, "- %s\n", escape(w))
		}
	}
	if m.RunID != "" {
		fmt.Fprintf(&b, "\n---\n_Run `%s`_\n", m.RunID)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
