package gitlab

import (
	"context"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func TestListChecks(t *testing.T) {
	c, _ := newClient(t, "")
	checks, err := c.ListChecks(context.Background(), testRepo, "dddddddddddddddddddddddddddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.Check{}
	for _, ch := range checks {
		got[ch.Name] = ch
	}
	if len(checks) != 4 {
		t.Fatalf("checks: %+v", checks)
	}
	if got["build"].State != domain.CheckSuccess {
		t.Errorf("build: %+v", got["build"])
	}
	// GitLab says "failed", not GitHub's "failure": a failure read as
	// pending would let a review approve over a red pipeline.
	if got["test"].State != domain.CheckFailure || got["test"].Summary != "2 tests failed" {
		t.Errorf("test: %+v", got["test"])
	}
	// allow_failure is GitLab's "neutral".
	if got["lint"].State != domain.CheckSuccess || got["lint"].Conclusion != "failed (allowed)" {
		t.Errorf("lint: %+v", got["lint"])
	}
	// An unrun manual job is pending, never a success.
	if got["deploy"].State != domain.CheckPending {
		t.Errorf("deploy: %+v", got["deploy"])
	}
	if got["build"].Source != domain.CheckSourceStatus {
		t.Errorf("source: %+v", got["build"])
	}
}
