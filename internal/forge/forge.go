// Package forge defines the abstraction over pull request hosting services.
package forge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/domain"
)

// ErrTooManyFiles is returned when a pull request exceeds the configured file limit.
var ErrTooManyFiles = errors.New("pull request exceeds the maximum number of changed files")

// Forge reads pull request data from a hosting service.
type Forge interface {
	// Name returns the provider identifier (github, gitea).
	Name() string
	GetPullRequest(ctx context.Context, repo domain.Repository, number int64) (*domain.PullRequest, error)
	// ListChangedFiles returns all changed files, following pagination. It
	// returns ErrTooManyFiles when more than maxFiles are present.
	ListChangedFiles(ctx context.Context, repo domain.Repository, number int64, maxFiles int) ([]domain.ChangedFile, error)
	// GetIssue loads one issue of the repository.
	GetIssue(ctx context.Context, repo domain.Repository, number int64) (*domain.Issue, error)
	// ListDiscussion returns the conversation comments, review summaries and
	// inline review comments of the pull request, oldest first, at most
	// maxComments entries.
	ListDiscussion(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error)
	// ListLabels returns the labels defined on the repository.
	ListLabels(ctx context.Context, repo domain.Repository) ([]domain.Label, error)
	// ListIssues returns the issues matching the query, pull requests excluded.
	ListIssues(ctx context.Context, repo domain.Repository, q IssueQuery) ([]domain.Issue, error)
	// ListIssueComments returns the comments of an issue, oldest first.
	ListIssueComments(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error)
	// AddIssueLabels attaches labels to an issue. It only ever adds: a label
	// a human put there is never removed by conclave.
	AddIssueLabels(ctx context.Context, repo domain.Repository, number int64, labels []domain.Label) error
	// ListReferences returns the commits, pull requests and issues that
	// mention the issue, oldest first. It is best-effort: a forge that does
	// not expose a timeline returns an empty slice and no error.
	ListReferences(ctx context.Context, repo domain.Repository, number int64, max int) ([]domain.Reference, error)
}

// IssueQuery selects the issues a triage run works on.
type IssueQuery struct {
	// State is open (default), closed or all.
	State string
	// Labels keeps only the issues carrying all of them.
	Labels []string
	// Since keeps only the issues updated at or after this time.
	Since time.Time
	// Limit caps the number of issues returned, 0 for no cap.
	Limit int
}

// Issue states accepted by IssueQuery.
const (
	StateOpen   = "open"
	StateClosed = "closed"
	StateAll    = "all"
)

var issueRefRe = regexp.MustCompile(`(?:^|[^\w/&])(?:([\w.-]+)/([\w.-]+))?#(\d+)\b`)

// IssueReferences extracts every issue number referenced as #N or
// owner/repo#N in text, not only those introduced by a closing keyword: a
// description that says "tracked separately (#16)" matters to a reviewer as
// much as "fixes #12". Only references to the given repository (or bare #N)
// are returned, deduplicated and in order of appearance.
func IssueReferences(text string, repo domain.Repository) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, m := range issueRefRe.FindAllStringSubmatch(text, -1) {
		if m[1] != "" && !(strings.EqualFold(m[1], repo.Owner) && strings.EqualFold(m[2], repo.Name)) {
			continue
		}
		n, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil || n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// LoadAssociatedIssues fetches the issues referenced by the pull request
// description and discussion, at most maxIssues of them. A missing issue or
// a reference to another pull request is skipped, other errors are returned.
func LoadAssociatedIssues(ctx context.Context, f Forge, repo domain.Repository, pr *domain.PullRequest, maxIssues int) ([]domain.Issue, error) {
	text := pr.Description
	for _, c := range pr.Discussion {
		text += "\n" + c.Body
	}
	var issues []domain.Issue
	for _, n := range IssueReferences(text, repo) {
		if n == pr.Number {
			continue
		}
		if maxIssues > 0 && len(issues) >= maxIssues {
			break
		}
		issue, err := f.GetIssue(ctx, repo, n)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return issues, fmt.Errorf("load issue #%d: %w", n, err)
		}
		issues = append(issues, *issue)
	}
	return issues, nil
}

// ErrNotFound is returned when a resource does not exist.
var ErrNotFound = errors.New("not found")
