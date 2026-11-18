package gitlab

import (
	"errors"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// mapState gives GitLab's "opened" the name the rest of conclave reads.
func mapState(s string) string {
	if s == "opened" {
		return "open"
	}
	return s
}

func mapMergeRequest(raw mergeRequest, repo domain.Repository) (*domain.PullRequest, error) {
	head := raw.SHA
	if head == "" {
		head = raw.DiffRefs.HeadSHA
	}
	if raw.IID == 0 || head == "" {
		return nil, errors.New("unexpected GitLab response: missing iid or head SHA")
	}
	pr := &domain.PullRequest{
		Number:      raw.IID,
		Title:       raw.Title,
		Description: raw.Description,
		State:       mapState(raw.State),
		WebURL:      raw.WebURL,
		Author:      raw.Author.Username,
		Labels:      raw.Labels,
		// The base lives in the repository the remote already points at, so
		// it needs no clone URL. A fork head is reachable through the merge
		// request ref, which is in that same repository.
		Base: domain.RepositoryRef{
			Owner: repo.Owner, Name: repo.Name,
			Branch: raw.TargetBranch, SHA: raw.DiffRefs.BaseSHA,
		},
		Head: domain.RepositoryRef{
			Owner: repo.Owner, Name: repo.Name,
			Branch: raw.SourceBranch, SHA: head,
			IsFork: raw.SourceProjectID != 0 && raw.SourceProjectID != raw.TargetProjectID,
		},
	}
	if raw.Draft {
		pr.State = "draft"
	}
	if pr.Head.IsFork {
		pr.Head.Owner, pr.Head.Name = "", ""
	}
	return pr, nil
}

func mapIssue(it issue) domain.Issue {
	return domain.Issue{
		Number: it.IID, Title: it.Title, Description: it.Description, WebURL: it.WebURL,
		State: mapState(it.State), Author: it.Author.Username, Labels: it.Labels,
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, ClosedAt: it.ClosedAt,
	}
}

func mapDiff(d mergeRequestDiff) domain.ChangedFile {
	f := domain.ChangedFile{Path: d.NewPath, Status: domain.FileModified}
	switch {
	case d.NewFile:
		f.Status = domain.FileAdded
	case d.DeletedFile:
		f.Status, f.Path = domain.FileDeleted, d.OldPath
	case d.RenamedFile:
		f.Status, f.PreviousPath = domain.FileRenamed, d.OldPath
	}
	if f.Path == "" {
		f.Path = d.OldPath
	}
	f.Additions, f.Deletions = countLines(d.Diff)
	return f
}

// countLines counts the added and removed lines of a unified diff. GitLab
// reports no per-file counts, and the hunk headers only bound the hunks.
func countLines(diff string) (added, removed int) {
	for len(diff) > 0 {
		line := diff
		if i := strings.IndexByte(diff, '\n'); i >= 0 {
			line, diff = diff[:i], diff[i+1:]
		} else {
			diff = ""
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func mapNote(n note) domain.Comment {
	c := domain.Comment{
		Kind: domain.CommentKindComment, Author: n.Author.Username,
		CreatedAt: n.CreatedAt, Body: n.Body,
	}
	if n.Type != "DiffNote" || n.Position == nil {
		return c
	}
	c.Kind = domain.CommentKindInline
	c.Path, c.Line = n.Position.NewPath, n.Position.NewLine
	if c.Path == "" {
		c.Path, c.Line = n.Position.OldPath, n.Position.OldLine
	}
	return c
}
