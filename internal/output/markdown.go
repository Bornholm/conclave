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
	if m.Since != nil {
		writeSince(&b, review, opts)
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
		if f.PreviousID != "" {
			fmt.Fprintf(&b, "**Still present** from the previous review (%s)  \n", f.PreviousID)
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
			if f.PreviousID != "" {
				fmt.Fprintf(&b, ", still present from the previous review (%s)", f.PreviousID)
			}
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

// writeSince renders what became of the previous review's findings: the
// fixed and withdrawn ones with their evidence, since they leave the review,
// and those nobody checked. The still-present ones are findings below.
func writeSince(b *strings.Builder, review *domain.ConsolidatedReview, opts Options) {
	prev := review.Meta.Since
	byID := map[string]domain.PreviousStatus{}
	for _, p := range review.PreviousFindings {
		byID[p.ID] = p
	}
	groups := map[domain.PreviousStatusKind][]int{}
	var unchecked []int
	for i := range prev.Findings {
		p, ok := byID[domain.PreviousID(i)]
		if !ok {
			unchecked = append(unchecked, i)
			continue
		}
		groups[p.Status] = append(groups[p.Status], i)
	}
	b.WriteString("\n### Since the previous review\n\n")
	against := fmt.Sprintf("revision `%s`", short(prev.HeadSHA))
	if prev.RunID != "" {
		against = fmt.Sprintf("run `%s`, %s", prev.RunID, against)
	}
	fmt.Fprintf(b, "Measured against %s: %d fixed, %d withdrawn, %d still present, %d not checked.\n",
		against, len(groups[domain.PreviousFixed]), len(groups[domain.PreviousWithdrawn]),
		len(groups[domain.PreviousStillPresent]), len(unchecked))
	for _, sec := range []struct {
		kind  domain.PreviousStatusKind
		title string
	}{{domain.PreviousFixed, "Fixed"}, {domain.PreviousWithdrawn, "Withdrawn"}} {
		if len(groups[sec.kind]) == 0 {
			continue
		}
		fmt.Fprintf(b, "\n**%s**\n\n", sec.title)
		for _, i := range groups[sec.kind] {
			p := byID[domain.PreviousID(i)]
			writePrevious(b, p.ID, prev.Findings[i])
			if e := strings.TrimSpace(p.Evidence); e != "" {
				b.WriteString(": " + escape(e))
			}
			if opts.ShowAttribution && len(p.ReportedBy) > 0 {
				fmt.Fprintf(b, " (%s)", strings.Join(p.ReportedBy, ", "))
			}
			b.WriteString("\n")
			writeClaims(b, p.Claims, opts)
		}
	}
	if len(unchecked) > 0 {
		b.WriteString("\n**Not checked**\n\n")
		for _, i := range unchecked {
			writePrevious(b, domain.PreviousID(i), prev.Findings[i])
			fmt.Fprintf(b, ", %s\n", prev.Findings[i].Severity)
		}
	}
}

// writeClaims renders, under a previous finding, the statuses its reviewers
// gave it that the one kept does not show.
func writeClaims(b *strings.Builder, claims []domain.PreviousClaim, opts Options) {
	for _, c := range claims {
		b.WriteString("  - also said " + string(c.Status))
		if e := strings.TrimSpace(c.Evidence); e != "" {
			b.WriteString(": " + escape(e))
		}
		if opts.ShowAttribution && len(c.ReportedBy) > 0 {
			fmt.Fprintf(b, " (%s)", strings.Join(c.ReportedBy, ", "))
		}
		b.WriteString("\n")
	}
}

func writePrevious(b *strings.Builder, id string, f domain.ConsolidatedFinding) {
	loc := f.File
	if f.StartLine > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.StartLine)
	}
	fmt.Fprintf(b, "- %s **%s** (`%s`)", id, escape(f.Title), loc)
}
