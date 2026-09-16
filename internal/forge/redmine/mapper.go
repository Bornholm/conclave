package redmine

import (
	"fmt"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// mapIssueToPullRequest converts a Redmine issue (with changesets and journals)
// into a domain.PullRequest surrogate. Redmine has no native PR concept, so the
// issue serves as the closest analogue: its changesets are the "diff" and the
// issue itself is the "pull request description".
func mapIssueToPullRequest(raw issue, repo domain.Repository) (*domain.PullRequest, error) {
	if raw.ID == 0 {
		return nil, fmt.Errorf("unexpected Redmine response: missing issue id")
	}
	pr := &domain.PullRequest{
		Number:      int64(raw.ID),
		Title:       raw.Subject,
		Description: raw.Description,
		Author:      raw.Author.Name,
		WebURL:      "", // built by caller from base URL
		Base: domain.RepositoryRef{
			Owner: repo.Owner,
			Name:  repo.Name,
		},
		Head: domain.RepositoryRef{
			Owner: repo.Owner,
			Name:  repo.Name,
			SHA:   fmt.Sprintf("issue-%d", raw.ID),
		},
	}
	// Derive state
	switch strings.ToLower(raw.Status.Name) {
	case "closed", "resolved", "rejected", "done":
		pr.State = "closed"
	case "new", "in progress", "feedback":
		pr.State = "open"
	default:
		pr.State = "open"
	}
	// Labels: use tracker and status as meaningful labels
	if raw.Tracker.Name != "" {
		pr.Labels = append(pr.Labels, raw.Tracker.Name)
	}
	if raw.Status.Name != "" {
		pr.Labels = append(pr.Labels, raw.Status.Name)
	}
	// Set the merge-base to a synthetic value since Redmine has no base branch
	pr.MergeBaseSHA = fmt.Sprintf("issue-%d-base", raw.ID)
	return pr, nil
}

// mapIssue converts a Redmine issue into a domain.Issue.
func mapIssue(raw issue) domain.Issue {
	issue := domain.Issue{
		Number:      int64(raw.ID),
		Title:       raw.Subject,
		Description: raw.Description,
		Author:      raw.Author.Name,
		State:       raw.Status.Name,
		CreatedAt:   raw.CreatedOn,
		UpdatedAt:   raw.UpdatedOn,
		ClosedAt:    raw.ClosedOn,
	}
	// Tracker and status serve as "labels"
	if raw.Tracker.Name != "" {
		issue.Labels = append(issue.Labels, raw.Tracker.Name)
	}
	if raw.Status.Name != "" {
		issue.Labels = append(issue.Labels, raw.Status.Name)
	}
	return issue
}
