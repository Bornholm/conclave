package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bornholm/conclave/internal/domain"
)

// Limits bound the accepted report.
type Limits struct {
	MaxFindings   int
	MaxFieldBytes int
}

// DefaultLimits are used when none are given.
var DefaultLimits = Limits{MaxFindings: 200, MaxFieldBytes: 16 << 10}

// FileChecker reports whether a path exists in the reviewed tree.
type FileChecker func(path string) bool

// ParseReport decodes and validates a reviewer report. Invalid findings are
// dropped with a warning; structural problems return an error.
func ParseReport(raw []byte, agentID string, changed map[string]bool, exists FileChecker, lim Limits) (*domain.AgentReport, []string, error) {
	data, err := ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var rep domain.AgentReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, nil, fmt.Errorf("decode report: %w", err)
	}
	if rep.SchemaVersion != domain.ReportSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", rep.SchemaVersion)
	}
	if rep.Reviewer.ID != agentID {
		return nil, nil, fmt.Errorf("reviewer id %q does not match agent %q", rep.Reviewer.ID, agentID)
	}
	if !rep.Verdict.Valid() {
		return nil, nil, fmt.Errorf("invalid verdict %q", rep.Verdict)
	}
	if lim.MaxFindings <= 0 {
		lim = DefaultLimits
	}
	rep.Summary = truncate(rep.Summary, lim.MaxFieldBytes)
	var warnings []string
	kept := rep.Findings[:0]
	for i, f := range rep.Findings {
		if len(kept) >= lim.MaxFindings {
			warnings = append(warnings, fmt.Sprintf("dropped findings beyond the limit of %d", lim.MaxFindings))
			break
		}
		f, err := normalizeFinding(f, changed, exists, lim)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("finding %d (%q) rejected: %v", i, truncate(f.Title, 60), err))
			continue
		}
		if f.ID == "" {
			f.ID = fmt.Sprintf("%s-%d", agentID, i+1)
		}
		kept = append(kept, f)
	}
	rep.Findings = kept
	qs := rep.Questions[:0]
	for _, q := range rep.Questions {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		q.Question = truncate(q.Question, lim.MaxFieldBytes)
		if q.File != "" {
			if p, err := cleanPath(q.File); err == nil {
				q.File = p
			} else {
				q.File = ""
			}
		}
		qs = append(qs, q)
	}
	rep.Questions = qs
	return &rep, warnings, nil
}

func normalizeFinding(f domain.Finding, changed map[string]bool, exists FileChecker, lim Limits) (domain.Finding, error) {
	f.Severity = domain.Severity(strings.ToLower(strings.TrimSpace(string(f.Severity))))
	if !f.Severity.Valid() {
		return f, fmt.Errorf("unknown severity %q", f.Severity)
	}
	f.Category = domain.NormalizeCategory(string(f.Category))
	if f.Confidence < 0 || f.Confidence > 1 {
		return f, fmt.Errorf("confidence %v out of [0,1]", f.Confidence)
	}
	p, err := cleanPath(f.File)
	if err != nil {
		return f, err
	}
	f.File = p
	if f.StartLine < 1 {
		return f, fmt.Errorf("start_line %d must be >= 1", f.StartLine)
	}
	if f.EndLine < f.StartLine {
		f.EndLine = f.StartLine
	}
	if strings.TrimSpace(f.Title) == "" {
		return f, errors.New("empty title")
	}
	if exists != nil && !exists(f.File) {
		return f, fmt.Errorf("file %q does not exist at head", f.File)
	}
	// A finding outside the pull request is kept but flagged: it may be a
	// pre-existing problem worth knowing about, yet it must not drive the verdict.
	f.OutOfScope = len(changed) > 0 && !changed[f.File]
	f.Title = truncate(f.Title, 512)
	f.Description = truncate(f.Description, lim.MaxFieldBytes)
	f.Evidence = truncate(f.Evidence, lim.MaxFieldBytes)
	f.Suggestion = truncate(f.Suggestion, lim.MaxFieldBytes)
	return f, nil
}

// cleanPath validates a repository-relative path.
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	if p == "" {
		return "", errors.New("empty file path")
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, ":") && strings.Index(p, ":") < strings.Index(p, "/") && strings.Index(p, ":") == 1 {
		return "", fmt.Errorf("absolute path %q", p)
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
		return "", fmt.Errorf("path %q escapes the repository", p)
	}
	return c, nil
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return CutRunes(s, max) + "…"
}

// CutRunes cuts s to at most max bytes without leaving a partial rune at the
// end. Cutting mid-rune leaves invalid UTF-8 in the prompts and in the
// artifacts, where the JSON encoder silently turns it into U+FFFD.
//
// It backs off by at most three bytes, the longest a trailing partial rune
// can be. That bound is the whole point: walking back until the prefix is
// valid would delete everything between an earlier bad byte and the cut,
// which is far worse than the broken rune it repairs. A byte that is invalid
// for some other reason is left where it is, as it was before any cutting.
func CutRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	for i := 0; i < utf8.UTFMax && len(cut) > 0; i++ {
		// A complete rune at the end, valid or not, stops the back-off.
		// Only the byte-by-byte remains of one that the cut split are
		// dropped, and RuneError with a width of one byte is exactly that.
		if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}

// ChangedSet builds the lookup of files touched by the pull request.
func ChangedSet(files []domain.ChangedFile) map[string]bool {
	m := make(map[string]bool, len(files))
	for _, f := range files {
		m[path.Clean(f.Path)] = true
	}
	return m
}

// SortedKeys returns the map keys in order, for deterministic output.
func SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NormalizeFindingFields validates and normalizes the common finding fields
// without checking membership in the changed file set.
func NormalizeFindingFields(f domain.Finding, exists FileChecker) (domain.Finding, error) {
	return normalizeFinding(f, nil, exists, DefaultLimits)
}
