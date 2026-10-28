package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/gitrepo"
)

// SinceLast asks for the most recent successful review of the same pull
// request as the previous one.
const SinceLast = "last"

// loadPrevious finds the review an incremental review is measured against.
// since is SinceLast, a run id under base, a run directory or the path of a
// review.json. The review must be of the same pull request, in the same
// repository: measuring one pull request against another's findings would
// mark them all as fixed.
func loadPrevious(base string, repo domain.Repository, pr *domain.PullRequest, since string) (*domain.PreviousReview, error) {
	path, err := previousPath(base, repo, pr, since)
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
	if review.Meta.PRNumber != pr.Number || review.Meta.Branch != localBranch(pr) {
		return nil, fmt.Errorf("--since: %s reviewed %s, not %s", path, reviewed(review.Meta.PRNumber, review.Meta.Branch), reviewed(pr.Number, localBranch(pr)))
	}
	if review.Meta.PRURL != "" && pr.WebURL != "" && review.Meta.PRURL != pr.WebURL {
		return nil, fmt.Errorf("--since: %s reviewed %s, not %s", path, review.Meta.PRURL, pr.WebURL)
	}
	m, hasManifest := runManifest(filepath.Dir(filepath.Dir(path)))
	if hasManifest && !sameRepository(m, repo) {
		return nil, fmt.Errorf("--since: %s is a review of %s/%s, not %s/%s", path, m.Owner, m.Repo, repo.Owner, repo.Name)
	}
	// Outside a run directory, the pull request's URL is all that ties the
	// review to a repository, and only when both sides have one: Conclave
	// always writes it, but a forge may give none, Redmine for one.
	if !hasManifest && (review.Meta.PRURL == "" || pr.WebURL == "") {
		return nil, fmt.Errorf("--since: %s is outside a run directory and there is no pull request URL to compare: nothing tells it is a review of %s/%s; pass its run directory instead", path, repo.Owner, repo.Name)
	}
	if review.Meta.HeadSHA == "" {
		return nil, fmt.Errorf("--since: %s does not say which revision it reviewed", path)
	}
	// A review.json outside a run directory and without its own run id has
	// none: it is left empty rather than named after whatever directory
	// holds the file.
	runID := review.Meta.RunID
	if runID == "" && hasManifest {
		runID = m.ID
	}
	return &domain.PreviousReview{RunID: runID, HeadSHA: review.Meta.HeadSHA, Findings: review.Findings}, nil
}

// runManifest reads the manifest of a run directory, if dir is one.
func runManifest(dir string) (domain.RunManifest, bool) {
	var m domain.RunManifest
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || json.Unmarshal(data, &m) != nil {
		return m, false
	}
	return m, true
}

// sameRepository reports whether a run was of repo. A manifest that names no
// repository, from a version that did not record it, is not held against it.
func sameRepository(m domain.RunManifest, repo domain.Repository) bool {
	return (m.Owner == "" || m.Owner == repo.Owner) && (m.Repo == "" || m.Repo == repo.Name)
}

// reviewed names what a review was of: a pull request, or local commits.
func reviewed(number int64, branch string) string {
	if number == 0 && strings.Contains(branch, "..") {
		return "local commits " + branch
	}
	if number == 0 {
		return "local branch " + branch
	}
	return fmt.Sprintf("pull request #%d", number)
}

func previousPath(base string, repo domain.Repository, pr *domain.PullRequest, since string) (string, error) {
	if since == SinceLast {
		return lastReview(base, repo, pr.Number, localBranch(pr))
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
// pull request number of repo under base, or, for a local review (number 0),
// of the same branch or range. Run ids start with their UTC start time, so they sort
// chronologically.
func lastReview(base string, repo domain.Repository, number int64, branch string) (string, error) {
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
		m, ok := runManifest(filepath.Join(base, name))
		if !ok || m.PRNumber != number || m.Branch != branch || m.Status != domain.RunSucceeded || !sameRepository(m, repo) {
			continue
		}
		path := filepath.Join(base, name, "final", "review.json")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("--since last: no successful review of %s under %s", reviewed(number, branch), base)
}

// sinceDiff is the diff from the previous review's head to this one, and a
// note for the prompt when it is not the plain increment: a head that did not
// move, a branch rewritten since, a previous head that cannot be found.
func (a *App) sinceDiff(ctx context.Context, r *run) {
	prev := r.previous
	head := r.pr.Head.SHA
	if prev.HeadSHA == head {
		r.sinceNote = "The head has not moved since the previous review: nothing changed in the code. Re-check each previous finding all the same, and weigh the author's responses."
		return
	}
	// The previous head may be missing from this clone for reasons other than
	// a force push: pruned, or reviewed from another clone. The pull request
	// ref still holds it when the branch was only extended.
	prRef := gitrepo.PullRequestRef(r.pr.Number)
	if r.pr.Local {
		prRef = ""
	}
	if err := r.git.EnsureCommit(ctx, prev.HeadSHA, prRef, r.cfg.Forge.Remote, r.pr.Head.CloneURL); err != nil {
		a.logger().Warn("previous head unavailable", "since", prev.HeadSHA, "error", err)
		r.sinceNote = fmt.Sprintf("The previous review's head %s is not available, here or from the remote: there is no diff since it. The branch may have been rewritten since. Compare each previous finding with the code at HEAD.", prev.HeadSHA)
		return
	}
	if !r.git.IsAncestor(ctx, prev.HeadSHA, head) {
		r.sinceNote = fmt.Sprintf("The branch was rewritten since the previous review (%s is not an ancestor of %s), so the diff since it also carries changes that came from the base.", prev.HeadSHA, head)
	}
	if !r.cfg.IncludeDiff() {
		// The configuration keeps diffs out of the prompt: the agents get the
		// command, as for the pull request's own diff.
		r.sinceByGit = true
		return
	}
	diff, trunc, err := r.git.Diff(ctx, prev.HeadSHA, head, r.cfg.Review.Limits.MaxDiffBytes, r.cfg.Review.DiffExclude...)
	if err != nil {
		a.logger().Warn("diff since the previous review unavailable", "since", prev.HeadSHA, "error", err)
		r.sinceNote = "The diff since the previous review could not be computed: compare each previous finding with the code at HEAD."
		return
	}
	r.sinceDiffText, r.sinceDiffTrunc = diff, trunc
}

// reconcilePrevious makes the statuses and the findings' links agree. A
// finding that continues a previous one says the problem is still there,
// whatever status that previous finding was given: the status becomes
// still-present, since the finding is in the review either way and a "fixed"
// next to it would tell the reader to stop looking. A link to a previous
// finding with no status gives it one.
func reconcilePrevious(review *domain.ConsolidatedReview) []string {
	linked := map[string][]string{}
	var order []string
	for _, f := range review.Findings {
		if f.PreviousID == "" {
			continue
		}
		if _, ok := linked[f.PreviousID]; !ok {
			order = append(order, f.PreviousID)
		}
		linked[f.PreviousID] = append(linked[f.PreviousID], f.ReportedBy...)
	}
	var warnings []string
	seen := map[string]bool{}
	for i, p := range review.PreviousFindings {
		seen[p.ID] = true
		by, ok := linked[p.ID]
		if !ok || p.Status == domain.PreviousStillPresent {
			continue
		}
		// The status replaced is kept in the warning and among the claims,
		// so the review still shows what it claimed and on what grounds.
		w := fmt.Sprintf("previous finding %s is %s, but a finding still continues it: it is reported still present", p.ID, p.Status)
		if len(p.ReportedBy) > 0 {
			w += fmt.Sprintf(" (%s said %s", strings.Join(p.ReportedBy, ", "), p.Status)
		} else {
			w += fmt.Sprintf(" (%s", p.Status)
		}
		if p.Evidence != "" {
			w += ": " + p.Evidence
		}
		warnings = append(warnings, w+")")
		replaced := domain.PreviousClaim{Status: p.Status, Evidence: p.Evidence, ReportedBy: p.ReportedBy}
		review.PreviousFindings[i] = domain.PreviousStatus{ID: p.ID, Status: domain.PreviousStillPresent, ReportedBy: dedupe(by),
			Claims: append([]domain.PreviousClaim{replaced}, p.Claims...)}
	}
	for _, id := range order {
		if !seen[id] {
			review.PreviousFindings = append(review.PreviousFindings, domain.PreviousStatus{ID: id, Status: domain.PreviousStillPresent, ReportedBy: dedupe(linked[id])})
		}
	}
	sort.SliceStable(review.PreviousFindings, func(i, j int) bool {
		a, _ := domain.PreviousIndex(review.PreviousFindings[i].ID)
		b, _ := domain.PreviousIndex(review.PreviousFindings[j].ID)
		return a < b
	})
	return warnings
}

func dedupe(ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
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
		i, ok := domain.PreviousIndex(p.ID)
		if !ok || i >= len(prev.Findings) {
			warnings = append(warnings, fmt.Sprintf("previous finding %s is still present but is not one of the previous review's findings: nothing could be put back", p.ID))
			continue
		}
		f := prev.Findings[i]
		// A finding of this review on the same lines is the same problem
		// reported again without its link: it gets the link, rather than the
		// review showing the problem twice.
		if j := sameProblem(review.Findings, f); j >= 0 {
			review.Findings[j].PreviousID = p.ID
			carried[p.ID] = true
			warnings = append(warnings, fmt.Sprintf("previous finding %s is still present and finding %q, on the same lines, carried no link: it is linked to %s", p.ID, review.Findings[j].Title, p.ID))
			continue
		}
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

// sameProblem is the index of an unlinked finding of findings in the same file
// as prev, on lines that overlap its own, or -1.
func sameProblem(findings []domain.ConsolidatedFinding, prev domain.ConsolidatedFinding) int {
	end := func(f domain.ConsolidatedFinding) int { return max(f.EndLine, f.StartLine) }
	for i, f := range findings {
		if f.PreviousID == "" && f.File == prev.File && f.StartLine > 0 && prev.StartLine > 0 &&
			f.StartLine <= end(prev) && prev.StartLine <= end(f) {
			return i
		}
	}
	return -1
}
