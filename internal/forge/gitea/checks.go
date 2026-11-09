package gitea

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
)

type combinedStatus struct {
	Statuses []commitStatus `json:"statuses"`
}

type commitStatus struct {
	Context     string `json:"context"`
	Status      string `json:"status"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// checkPageSize is the page size asked of the combined status endpoint.
const checkPageSize = 50

// checkMaxPages bounds the pages read: a commit with more statuses than
// that is not a CI, it is a loop.
const checkMaxPages = 10

// ListChecks implements forge.CheckReader. Gitea and Forgejo report CI,
// their own Actions included, as commit statuses; the combined status keeps
// the latest of each context. They carry no annotations.
func (c *Client) ListChecks(ctx context.Context, repo domain.Repository, sha string) ([]domain.Check, error) {
	path := repoPath(repo) + "/commits/" + url.PathEscape(sha) + "/status"
	var out []domain.Check
	seen := map[string]bool{}
	for page := 1; page <= checkMaxPages; page++ {
		var status combinedStatus
		q := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(checkPageSize)}}
		if _, err := c.get(ctx, path, q, &status); err != nil {
			return nil, fmt.Errorf("get commit status: %w", err)
		}
		// A server that ignores page and limit returns the same statuses
		// on every page: a page that brings no new context is the end.
		added := 0
		for _, s := range status.Statuses {
			if seen[s.Context] {
				continue
			}
			seen[s.Context] = true
			added++
			out = append(out, domain.Check{
				Name: s.Context, Source: domain.CheckSourceStatus, State: forge.StatusState(s.Status), Conclusion: s.Status,
				URL: s.TargetURL, Summary: forge.CutText(s.Description, forge.MaxCheckSummaryBytes),
			})
		}
		if added == 0 || len(status.Statuses) < checkPageSize {
			break
		}
	}
	return out, nil
}
