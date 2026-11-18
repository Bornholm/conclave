package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
)

// ListChecks implements forge.CheckReader. GitLab reports its own CI jobs
// and the external integrations alike as commit statuses, and the endpoint
// returns the latest of each name by default, so the pipelines are not read
// separately. GitLab attaches no per-line annotations to a job.
func (c *Client) ListChecks(ctx context.Context, repo domain.Repository, sha string) ([]domain.Check, error) {
	path := projectPath(repo) + "/repository/commits/" + url.PathEscape(sha) + "/statuses"
	var out []domain.Check
	seen := map[string]bool{}
	for page := 1; page <= maxPages; page++ {
		var raw []commitStatus
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		resp, err := c.get(ctx, path, q, &raw)
		if err != nil {
			return nil, fmt.Errorf("list commit statuses: %w", err)
		}
		for _, s := range raw {
			if s.Name == "" || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			out = append(out, mapStatus(s))
		}
		if len(raw) == 0 || strings.TrimSpace(resp.Header.Get("X-Next-Page")) == "" {
			break
		}
	}
	return out, nil
}

func mapStatus(s commitStatus) domain.Check {
	ch := domain.Check{
		Name: s.Name, Source: domain.CheckSourceStatus,
		State: forge.StatusState(s.Status), Conclusion: s.Status,
		URL: s.TargetURL, Summary: forge.CutText(s.Description, forge.MaxCheckSummaryBytes),
	}
	// A job the pipeline is allowed to fail does not fail the pipeline, so
	// it is no reason to refuse a review: GitHub calls that outcome neutral.
	if ch.State == domain.CheckFailure && s.AllowFailure {
		ch.State, ch.Conclusion = domain.CheckSuccess, s.Status+" (allowed)"
	}
	return ch
}
