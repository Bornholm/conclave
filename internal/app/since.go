package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// SinceLast asks for the most recent successful review of the same pull
// request as the previous one.
const SinceLast = "last"

// loadPrevious finds the review an incremental review is measured against.
// since is SinceLast, a run id under base, a run directory or the path of a
// review.json. The review must be of the same pull request: measuring one
// pull request against another's findings would mark them all as fixed.
func loadPrevious(base string, number int64, since string) (*domain.PreviousReview, error) {
	path, err := previousPath(base, number, since)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--since: %w", err)
	}
	var review domain.ConsolidatedReview
	if err := json.Unmarshal(data, &review); err != nil {
		return nil, fmt.Errorf("--since: %s is not a review: %w", path, err)
	}
	if review.Meta.PRNumber != number {
		return nil, fmt.Errorf("--since: %s reviewed pull request #%d, not #%d", path, review.Meta.PRNumber, number)
	}
	if review.Meta.HeadSHA == "" {
		return nil, fmt.Errorf("--since: %s does not say which revision it reviewed", path)
	}
	runID := review.Meta.RunID
	if runID == "" {
		runID = filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	return &domain.PreviousReview{RunID: runID, HeadSHA: review.Meta.HeadSHA, Findings: review.Findings}, nil
}

func previousPath(base string, number int64, since string) (string, error) {
	if since == SinceLast {
		return lastReview(base, number)
	}
	if info, err := os.Stat(since); err == nil {
		if info.IsDir() {
			return filepath.Join(since, "final", "review.json"), nil
		}
		return since, nil
	}
	path := filepath.Join(base, since, "final", "review.json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("--since %s: no such run directory or review under %s", since, base)
	}
	return path, nil
}

// lastReview returns the review.json of the most recent successful review of
// pull request number under base. Run ids start with their UTC start time, so
// they sort chronologically.
func lastReview(base string, number int64) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("--since last: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		var m domain.RunManifest
		data, err := os.ReadFile(filepath.Join(base, name, "manifest.json"))
		if err != nil || json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.PRNumber != number || m.Status != domain.RunSucceeded {
			continue
		}
		path := filepath.Join(base, name, "final", "review.json")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("--since last: no successful review of pull request #%d under %s", number, base)
}

// sinceDiff is the diff from the previous review's head to this one, and a
// note for the prompt when it is not the plain increment: a head that did not
// move, a branch rewritten since, a previous head that is gone.
func (a *App) sinceDiff(ctx context.Context, r *run) {
	prev := r.previous
	head := r.pr.Head.SHA
	switch {
	case prev.HeadSHA == head:
		r.sinceNote = "The head has not moved since the previous review: nothing changed in the code. Re-check each previous finding all the same, and weigh the author's responses."
		return
	case !r.git.HasCommit(ctx, prev.HeadSHA):
		r.sinceNote = fmt.Sprintf("The previous review's head %s is no longer available, most likely rewritten by a force push: there is no diff since it. Compare each previous finding with the code at HEAD.", prev.HeadSHA)
		return
	case !r.git.IsAncestor(ctx, prev.HeadSHA, head):
		r.sinceNote = fmt.Sprintf("The branch was rewritten since the previous review (%s is not an ancestor of %s), so the diff below also carries changes that came from the base.", prev.HeadSHA, head)
	}
	diff, trunc, err := r.git.Diff(ctx, prev.HeadSHA, head, r.cfg.Review.Limits.MaxDiffBytes, r.cfg.Review.DiffExclude...)
	if err != nil {
		a.logger().Warn("diff since the previous review unavailable", "since", prev.HeadSHA, "error", err)
		r.sinceNote = "The diff since the previous review could not be computed: compare each previous finding with the code at HEAD."
		return
	}
	r.sinceDiffText, r.sinceDiffTrunc = diff, trunc
}

// keepStillPresent puts back, from the previous review, a finding the
// consolidation says is still present but carries in no finding of its own:
// the status would otherwise say the problem is there while the review shows
// nothing of it. A critical or high one in scope also holds the verdict at
// request_changes, as it would have had the lead kept it.
func keepStillPresent(review *domain.ConsolidatedReview, prev *domain.PreviousReview, changed map[string]bool) []string {
	if prev == nil {
		return nil
	}
	carried := map[string]bool{}
	for _, f := range review.Findings {
		if f.PreviousID != "" {
			carried[f.PreviousID] = true
		}
	}
	var warnings []string
	for _, p := range review.PreviousFindings {
		if p.Status != domain.PreviousStillPresent || carried[p.ID] {
			continue
		}
		i := previousIndex(p.ID)
		if i < 0 || i >= len(prev.Findings) {
			continue
		}
		f := prev.Findings[i]
		f.PreviousID = p.ID
		f.ReportedBy = append([]string(nil), p.ReportedBy...)
		f.OutOfScope = len(changed) > 0 && !changed[f.File]
		review.Findings = append(review.Findings, f)
		carried[p.ID] = true
		warnings = append(warnings, fmt.Sprintf("previous finding %s is still present but no finding carried it: it is kept as the previous review wrote it", p.ID))
		if !f.OutOfScope && (f.Severity == domain.SeverityCritical || f.Severity == domain.SeverityHigh) && review.Verdict != domain.VerdictRequestChanges {
			review.Verdict = domain.VerdictRequestChanges
			warnings = append(warnings, fmt.Sprintf("previous finding %s is %s and still present: the verdict is request_changes", p.ID, f.Severity))
		}
	}
	return warnings
}

// previousIndex is the index of a previous finding id, -1 when malformed.
func previousIndex(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "P"))
	if err != nil {
		return -1
	}
	return n - 1
}
