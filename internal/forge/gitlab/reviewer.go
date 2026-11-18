package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/domain"
)

// CurrentUser implements forge.PullRequestReviewer.
func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	var u user
	if _, err := c.get(ctx, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Username == "" {
		return "", errors.New("unexpected GitLab response: no username")
	}
	return u.Username, nil
}

// ListOpenPullRequests implements forge.PullRequestReviewer.
func (c *Client) ListOpenPullRequests(ctx context.Context, repo domain.Repository) ([]domain.PullRequest, error) {
	var raw []mergeRequest
	if err := c.getAll(ctx, projectPath(repo)+"/merge_requests", url.Values{"state": {"opened"}}, &raw); err != nil {
		return nil, err
	}
	out := make([]domain.PullRequest, 0, len(raw))
	for _, mr := range raw {
		pr, err := mapMergeRequest(mr, repo)
		if err != nil {
			return nil, err
		}
		out = append(out, *pr)
	}
	return out, nil
}

// ListReviews implements forge.PullRequestReviewer. GitLab has no review
// object. What stands for one, as a review does on GitHub, is an approval,
// an inline comment on the diff, or a note carrying the loop's mark, the
// way SubmitReview publishes. A plain comment is conversation and a system
// note the activity log: neither is a review.
func (c *Client) ListReviews(ctx context.Context, repo domain.Repository, number int64) ([]domain.ForgeReview, error) {
	base := fmt.Sprintf("%s/merge_requests/%d", projectPath(repo), number)
	var raw []discussion
	if err := c.getAll(ctx, base+"/discussions", nil, &raw); err != nil {
		return nil, err
	}
	var out []domain.ForgeReview
	for _, d := range raw {
		for _, n := range d.Notes {
			if n.System || n.Type != "DiffNote" && !strings.Contains(n.Body, domain.LoopMarkPrefix) {
				continue
			}
			out = append(out, domain.ForgeReview{Author: n.Author.Username, State: "commented", Body: n.Body, SubmittedAt: n.CreatedAt})
		}
	}
	// Unlike ListDiscussion, an approval that cannot be read is an error:
	// taken for none, the loop would review a merge request a human
	// approved.
	var app approvals
	if _, err := c.get(ctx, base+"/approvals", nil, &app); err != nil {
		return nil, fmt.Errorf("read approvals: %w", err)
	}
	for _, a := range app.ApprovedBy {
		out = append(out, domain.ForgeReview{Author: a.User.Username, State: "approved", SubmittedAt: a.ApprovedAt})
	}
	slices.SortStableFunc(out, byInstant)
	return out, nil
}

// byInstant orders by time, not text: an instance with a time zone set
// writes offsets. A timestamp that does not parse falls back on the text.
func byInstant(a, b domain.ForgeReview) int {
	ta, errA := time.Parse(time.RFC3339, a.SubmittedAt)
	tb, errB := time.Parse(time.RFC3339, b.SubmittedAt)
	if errA != nil || errB != nil {
		return strings.Compare(a.SubmittedAt, b.SubmittedAt)
	}
	return ta.Compare(tb)
}

// SubmitReview implements forge.PullRequestReviewer. GitLab splits what
// GitHub submits in one call: the body goes as a note, and an approval is a
// call of its own, pinned to sha so that GitLab refuses it once the head
// has moved. GitLab's API has no request for changes: that verdict and a
// comment are the note alone, and block nothing.
//
// The note goes first. An approval that fails afterwards leaves a note whose
// mark says approve, which the loop takes as submitted: the merge request
// stays unapproved, the safe side. The other order could leave an approval
// with no review to explain it.
func (c *Client) SubmitReview(ctx context.Context, repo domain.Repository, number int64, sha string, verdict domain.Verdict, body string) error {
	switch verdict {
	case domain.VerdictApprove, domain.VerdictComment, domain.VerdictRequestChanges:
	default:
		return fmt.Errorf("unknown verdict %q", verdict)
	}
	base := fmt.Sprintf("%s/merge_requests/%d", projectPath(repo), number)
	if _, err := c.do(ctx, http.MethodPost, base+"/notes", nil, map[string]any{"body": body}, nil); err != nil {
		return fmt.Errorf("post review note: %w", err)
	}
	if verdict != domain.VerdictApprove {
		return nil
	}
	if _, err := c.do(ctx, http.MethodPost, base+"/approve", nil, map[string]any{"sha": sha}, nil); err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	return nil
}
