package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/forge/httpx"
)

type checkRuns struct {
	CheckRuns []checkRun `json:"check_runs"`
}

type checkRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	DetailsURL string `json:"details_url"`
	Output     struct {
		Title            string `json:"title"`
		Summary          string `json:"summary"`
		AnnotationsCount int    `json:"annotations_count"`
	} `json:"output"`
}

type checkAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title"`
	Message         string `json:"message"`
}

type combinedStatus struct {
	Statuses []commitStatus `json:"statuses"`
}

type commitStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// ListChecks implements forge.CheckReader. GitHub reports CI two ways,
// check runs (GitHub Actions and the apps) and commit statuses (older
// integrations): both are read. The check runs listed are the latest of
// each name, which is GitHub's default filter.
func (c *Client) ListChecks(ctx context.Context, repo domain.Repository, sha string) ([]domain.Check, error) {
	base := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var out []domain.Check
	next := c.http.Resolve(base+"/commits/"+url.PathEscape(sha)+"/check-runs", url.Values{"per_page": {"100"}})
	annotated := 0
	for next != "" {
		resp, err := c.http.Do(ctx, http.MethodGet, next, nil, accept)
		if err != nil {
			return nil, fmt.Errorf("list check runs: %w", err)
		}
		var page checkRuns
		if err := decode(resp.Body, &page); err != nil {
			return nil, fmt.Errorf("list check runs: %w", err)
		}
		for _, run := range page.CheckRuns {
			ch := mapCheckRun(run)
			if ch.State == domain.CheckFailure && run.Output.AnnotationsCount > 0 && annotated < forge.MaxAnnotatedChecks {
				annotated++
				// The annotations add to a check the list already reports:
				// failing to read them must not make the whole CI unknown.
				anns, aerr := c.annotations(ctx, base, run.ID)
				if aerr != nil {
					ch.Summary = strings.TrimSpace(ch.Summary + "\n[annotations not read: " + forge.CutText(aerr.Error(), 200) + "]")
				}
				ch.Annotations = anns
			}
			out = append(out, ch)
		}
		next = httpx.NextLink(resp.Header)
	}
	next = c.http.Resolve(base+"/commits/"+url.PathEscape(sha)+"/status", url.Values{"per_page": {"100"}})
	for next != "" {
		resp, err := c.http.Do(ctx, http.MethodGet, next, nil, accept)
		if err != nil {
			return nil, fmt.Errorf("get commit status: %w", err)
		}
		var status combinedStatus
		if err := decode(resp.Body, &status); err != nil {
			return nil, fmt.Errorf("get commit status: %w", err)
		}
		for _, s := range status.Statuses {
			out = append(out, domain.Check{
				Name: s.Context, Source: domain.CheckSourceStatus, State: forge.StatusState(s.State), Conclusion: s.State,
				URL: s.TargetURL, Summary: forge.CutText(s.Description, forge.MaxCheckSummaryBytes),
			})
		}
		next = httpx.NextLink(resp.Header)
	}
	return out, nil
}

func mapCheckRun(run checkRun) domain.Check {
	ch := domain.Check{Name: run.Name, Source: domain.CheckSourceRun, Conclusion: run.Conclusion, URL: run.HTMLURL}
	if ch.URL == "" {
		ch.URL = run.DetailsURL
	}
	if run.Status != "completed" {
		ch.State, ch.Conclusion = domain.CheckPending, run.Status
	} else {
		ch.State = checkRunState(run.Conclusion)
	}
	summary := strings.TrimSpace(run.Output.Title)
	if s := strings.TrimSpace(run.Output.Summary); s != "" {
		if summary != "" {
			summary += "\n"
		}
		summary += s
	}
	ch.Summary = forge.CutText(summary, forge.MaxCheckSummaryBytes)
	return ch
}

// checkRunState maps the conclusion of a completed check run.
func checkRunState(conclusion string) domain.CheckState {
	switch conclusion {
	case "success", "neutral", "skipped":
		return domain.CheckSuccess
	}
	// failure, timed_out, cancelled, action_required, startup_failure,
	// stale: the check did not pass.
	return domain.CheckFailure
}

func (c *Client) annotations(ctx context.Context, base string, id int64) ([]domain.CheckAnnotation, error) {
	var raw []checkAnnotation
	path := base + "/check-runs/" + strconv.FormatInt(id, 10) + "/annotations"
	if _, err := c.get(ctx, path, url.Values{"per_page": {strconv.Itoa(forge.MaxCheckAnnotations)}}, &raw); err != nil {
		return nil, fmt.Errorf("list check run annotations: %w", err)
	}
	out := make([]domain.CheckAnnotation, 0, len(raw))
	for _, a := range raw {
		msg := strings.TrimSpace(a.Message)
		if t := strings.TrimSpace(a.Title); t != "" && !strings.Contains(msg, t) {
			msg = t + ": " + msg
		}
		out = append(out, domain.CheckAnnotation{
			Path: a.Path, StartLine: a.StartLine, EndLine: a.EndLine, Level: a.AnnotationLevel,
			Message: forge.CutText(msg, forge.MaxAnnotationBytes),
		})
		if len(out) == forge.MaxCheckAnnotations {
			break
		}
	}
	return out, nil
}
