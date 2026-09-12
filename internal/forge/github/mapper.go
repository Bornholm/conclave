package github

import (
	"errors"
	"fmt"

	"github.com/bornholm/conclave/internal/domain"
)

func mapPullRequest(raw pullRequest) (*domain.PullRequest, error) {
	if raw.Number == 0 || raw.Head.SHA == "" || raw.Base.SHA == "" {
		return nil, errors.New("unexpected GitHub response: missing number or SHAs")
	}
	if raw.Base.Repo == nil {
		return nil, errors.New("unexpected GitHub response: missing base repository")
	}
	pr := &domain.PullRequest{
		Number:      raw.Number,
		Title:       raw.Title,
		Description: raw.Body,
		State:       raw.State,
		WebURL:      raw.HTMLURL,
		Author:      raw.User.Login,
		Base:        mapRef(raw.Base, raw.Base.Repo),
	}
	if raw.Draft {
		pr.State = "draft"
	}
	if raw.Head.Repo == nil {
		// Head repository deleted: the commit is still reachable via refs/pull/N/head.
		pr.Head = domain.RepositoryRef{Branch: raw.Head.Ref, SHA: raw.Head.SHA, IsFork: true}
	} else {
		pr.Head = mapRef(raw.Head, raw.Head.Repo)
		pr.Head.IsFork = raw.Head.Repo.FullName != raw.Base.Repo.FullName
	}
	for _, l := range raw.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	return pr, nil
}

func mapRef(r ref, repo *repository) domain.RepositoryRef {
	return domain.RepositoryRef{
		Owner:    repo.Owner.Login,
		Name:     repo.Name,
		CloneURL: repo.CloneURL,
		Branch:   r.Ref,
		SHA:      r.SHA,
		IsFork:   repo.Fork,
	}
}

func mapLabel(l label) domain.Label {
	return domain.Label{ID: l.ID, Name: l.Name, Description: l.Description, Color: l.Color}
}

func mapIssueItem(it issueListItem) domain.Issue {
	issue := domain.Issue{
		Number: it.Number, Title: it.Title, Description: it.Body, WebURL: it.HTMLURL,
		State: it.State, Author: it.User.Login,
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, ClosedAt: it.ClosedAt,
	}
	for _, l := range it.Labels {
		issue.Labels = append(issue.Labels, l.Name)
	}
	return issue
}

func mapFile(f pullRequestFile) domain.ChangedFile {
	status := f.Status
	switch status {
	case "added", "modified", "removed", "renamed", "copied", "changed", "unchanged":
		if status == "removed" {
			status = domain.FileDeleted
		}
	default:
		status = fmt.Sprintf("unknown(%s)", status)
	}
	return domain.ChangedFile{
		Path:         f.Filename,
		PreviousPath: f.PreviousFilename,
		Status:       status,
		Additions:    f.Additions,
		Deletions:    f.Deletions,
	}
}
