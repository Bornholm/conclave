// Package consolidation merges reviewer reports deterministically and
// validates the lead's consolidated output.
package consolidation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// ReviewerOutcome is the result of one reviewer, successful or not.
type ReviewerOutcome struct {
	ID     string
	Report *domain.AgentReport
	Err    error
}

var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeText lowercases and collapses punctuation for fuzzy matching.
func NormalizeText(s string) string {
	return strings.TrimSpace(nonAlnumRe.ReplaceAllString(strings.ToLower(s), " "))
}

// Fingerprint identifies a finding by file, start line, category and normalized title.
func Fingerprint(f domain.Finding) string {
	value := strings.Join([]string{
		path.Clean(f.File), strconv.Itoa(f.StartLine), string(f.Category), NormalizeText(f.Title),
	}, ":")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// Normalize flattens the successful reports into attributed findings sorted
// by severity, file and line.
func Normalize(outcomes []ReviewerOutcome) []domain.NormalizedFinding {
	var out []domain.NormalizedFinding
	for _, o := range outcomes {
		if o.Report == nil {
			continue
		}
		for _, f := range o.Report.Findings {
			f.File = path.Clean(f.File)
			out = append(out, domain.NormalizedFinding{Finding: f, ReviewerID: o.ID, Fingerprint: Fingerprint(f)})
		}
	}
	SortFindings(out)
	return out
}

// SortFindings orders in-scope before out-of-scope, then by severity,
// file, line, confidence desc and reviewer.
func SortFindings(fs []domain.NormalizedFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.OutOfScope != b.OutOfScope {
			return !a.OutOfScope
		}
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() < b.Severity.Rank()
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		return a.ReviewerID < b.ReviewerID
	})
}

// Group clusters findings that describe the same problem: same file, same
// category, overlapping line ranges and similar titles. Findings from the same
// reviewer are never merged together.
func Group(findings []domain.NormalizedFinding) []domain.FindingGroup {
	var groups []domain.FindingGroup
	for _, f := range findings {
		placed := false
		for gi := range groups {
			g := &groups[gi]
			if sameReviewer(g, f.ReviewerID) {
				continue
			}
			if similar(g.Findings[0], f) {
				g.Findings = append(g.Findings, f)
				g.ReportedBy = append(g.ReportedBy, f.ReviewerID)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, domain.FindingGroup{
				ID:         fmt.Sprintf("g%d", len(groups)+1),
				Findings:   []domain.NormalizedFinding{f},
				ReportedBy: []string{f.ReviewerID},
			})
		}
	}
	for i := range groups {
		sort.Strings(groups[i].ReportedBy)
	}
	return groups
}

func sameReviewer(g *domain.FindingGroup, id string) bool {
	for _, r := range g.ReportedBy {
		if r == id {
			return true
		}
	}
	return false
}

func similar(a, b domain.NormalizedFinding) bool {
	if a.File != b.File || a.Category != b.Category || a.OutOfScope != b.OutOfScope {
		return false
	}
	if a.Fingerprint == b.Fingerprint {
		return true
	}
	if !overlap(a.StartLine, a.EndLine, b.StartLine, b.EndLine, 3) {
		return false
	}
	return TitleSimilarity(a.Title, b.Title) >= 0.4
}

func overlap(s1, e1, s2, e2, slack int) bool {
	return s1-slack <= e2 && s2-slack <= e1
}

// TitleSimilarity is the Jaccard index of the normalized word sets.
func TitleSimilarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	inter := 0
	for w := range wa {
		if wb[w] {
			inter++
		}
	}
	union := len(wa) + len(wb) - inter
	return float64(inter) / float64(union)
}

var stopWords = map[string]bool{"the": true, "a": true, "an": true, "of": true, "in": true, "to": true, "is": true, "and": true, "on": true, "for": true, "with": true}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(NormalizeText(s)) {
		if len(w) > 1 && !stopWords[w] {
			m[stem(w)] = true
		}
	}
	return m
}

// stem strips common English suffixes so "concurrently" and "concurrent"
// or "writes" and "write" compare equal.
func stem(w string) string {
	for _, suf := range []string{"ing", "ly", "ed", "es", "s"} {
		if len(w) > len(suf)+2 && strings.HasSuffix(w, suf) {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}
