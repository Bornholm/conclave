// Package output renders the consolidated review.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// Options tune the rendering.
type Options struct {
	ShowAttribution  bool
	ShowFailedAgents bool
}

// JSON writes the review as indented JSON.
func JSON(w io.Writer, review *domain.ConsolidatedReview) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(review)
}

var verdictLabels = map[domain.Verdict]string{
	domain.VerdictApprove:        "Approved",
	domain.VerdictComment:        "Comments",
	domain.VerdictRequestChanges: "Changes requested",
}

var severityHeadings = map[domain.Severity]string{
	domain.SeverityCritical: "Critical findings",
	domain.SeverityHigh:     "High severity findings",
	domain.SeverityMedium:   "Medium severity findings",
	domain.SeverityLow:      "Low severity findings",
	domain.SeverityInfo:     "Informational",
}

// Markdown writes the review as a Markdown document.
func Markdown(w io.Writer, review *domain.ConsolidatedReview, opts Options) error {
	var b strings.Builder
	m := review.Meta
	b.WriteString("## Conclave review\n\n")
	fmt.Fprintf(&b, "**Verdict:** %s  \n", label(review.Verdict))
	if m.PRNumber != 0 {
		fmt.Fprintf(&b, "**Pull request:** #%d — %s  \n", m.PRNumber, escape(m.PRTitle))
	}
	if m.HeadSHA != "" {
		fmt.Fprintf(&b, "**Revision:** `%s`  \n", short(m.HeadSHA))
	}
	if m.ReviewersTotal > 0 {
		fmt.Fprintf(&b, "**Reviewers:** %d/%d successful", m.ReviewersSucceeded, m.ReviewersTotal)
		if !m.LeadUsed {
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
	if s := strings.TrimSpace(review.Summary); s != "" {
		b.WriteString("\n" + s + "\n")
	}

	var current domain.Severity = "none"
	inScope, outOfScope := 0, 0
	for _, f := range review.Findings {
		if f.OutOfScope {
			outOfScope++
			continue
		}
		inScope++
		if f.Severity != current {
			current = f.Severity
			h := severityHeadings[current]
			if h == "" {
				h = "Other findings"
			}
			fmt.Fprintf(&b, "\n### %s\n", h)
		}
		fmt.Fprintf(&b, "\n#### %s\n\n", escape(f.Title))
		loc := f.File
		if f.StartLine > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.StartLine)
			if f.EndLine > f.StartLine {
				loc += fmt.Sprintf("-%d", f.EndLine)
			}
		}
		fmt.Fprintf(&b, "**Location:** `%s`  \n", loc)
		fmt.Fprintf(&b, "**Category:** %s  \n", f.Category)
		fmt.Fprintf(&b, "**Confidence:** %d%%  \n", int(f.Confidence*100+0.5))
		if opts.ShowAttribution && len(f.ReportedBy) > 0 {
			fmt.Fprintf(&b, "**Reported by:** %s  \n", strings.Join(f.ReportedBy, ", "))
		}
		if d := strings.TrimSpace(f.Description); d != "" {
			b.WriteString("\n" + d + "\n")
		}
		if e := strings.TrimSpace(f.Evidence); e != "" {
			b.WriteString("\n**Evidence:** " + e + "\n")
		}
		if s := strings.TrimSpace(f.Suggestion); s != "" {
			b.WriteString("\n**Suggestion:** " + s + "\n")
		}
	}

	if inScope == 0 {
		b.WriteString("\n_No findings in the pull request scope._\n")
	}
	if outOfScope > 0 {
		b.WriteString("\n### Out of scope\n\nPre-existing issues in files this pull request does not change. They do not affect the verdict.\n")
		for _, f := range review.Findings {
			if !f.OutOfScope {
				continue
			}
			loc := f.File
			if f.StartLine > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.StartLine)
			}
			fmt.Fprintf(&b, "\n- **%s** (`%s`, %s, %s", escape(f.Title), loc, f.Severity, f.Category)
			if opts.ShowAttribution && len(f.ReportedBy) > 0 {
				fmt.Fprintf(&b, ", reported by %s", strings.Join(f.ReportedBy, ", "))
			}
			b.WriteString(")")
			if d := strings.TrimSpace(f.Description); d != "" {
				b.WriteString(" — " + escape(d))
			}
			b.WriteString("\n")
		}
	}

	if len(review.Questions) > 0 {
		b.WriteString("\n### Questions for the author\n\n")
		for _, q := range review.Questions {
			if q.File != "" {
				if q.Line > 0 {
					fmt.Fprintf(&b, "- `%s:%d`: %s\n", q.File, q.Line, strings.TrimSpace(q.Question))
				} else {
					fmt.Fprintf(&b, "- `%s`: %s\n", q.File, strings.TrimSpace(q.Question))
				}
			} else {
				fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(q.Question))
			}
		}
	}

	if opts.ShowFailedAgents && len(review.FailedReviewers) > 0 {
		b.WriteString("\n### Reviewer failures\n\n")
		for _, f := range review.FailedReviewers {
			fmt.Fprintf(&b, "- `%s`: %s\n", f.ID, f.Reason)
		}
	}
	if len(review.Warnings) > 0 {
		b.WriteString("\n### Warnings\n\n")
		for _, w := range review.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	if m.RunID != "" {
		fmt.Fprintf(&b, "\n---\n_Run `%s`_\n", m.RunID)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func label(v domain.Verdict) string {
	if l, ok := verdictLabels[v]; ok {
		return l
	}
	return string(v)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func escape(s string) string {
	return strings.NewReplacer("\n", " ", "\r", "").Replace(strings.TrimSpace(s))
}
