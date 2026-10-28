package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/output"
)

func TestSplitRange(t *testing.T) {
	for _, tc := range []struct {
		rev, base, head string
		isRange         bool
	}{
		{"", "", "", false},
		{"feature", "", "feature", false},
		{"main..feature", "main", "feature", true},
		{"main...feature", "main", "feature", true},
		{"HEAD~3..", "HEAD~3", "HEAD", true},
		{"..feature", "HEAD", "feature", true},
	} {
		base, head, isRange := splitRange(tc.rev)
		if base != tc.base || head != tc.head || isRange != tc.isRange {
			t.Errorf("%q: got %q %q %v", tc.rev, base, head, isRange)
		}
	}
}

func TestReviewLocalCommits(t *testing.T) {
	a, dir := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	// No forge and no remote: a local review needs neither.
	a.Forge = nil
	a.Config.Forge.Remote = "missing"
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ctx := context.Background()

	if _, err := a.Review(ctx, ReviewRequest{Revision: "main"}); err == nil || !strings.Contains(err.Error(), "nothing to review") {
		t.Fatalf("the default branch against itself: %v", err)
	}
	if _, err := a.Review(ctx, ReviewRequest{Revision: "feature", Since: SinceLast}); err == nil || !strings.Contains(err.Error(), "no successful review of local branch feature") {
		t.Fatalf("--since last with no previous review: %v", err)
	}

	first, err := a.Review(ctx, ReviewRequest{Revision: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	m := first.Manifest
	if m.PRNumber != 0 || m.Branch != "feature" || m.Forge != "" || !strings.Contains(m.ID, "-local-feature-") {
		t.Errorf("manifest: %+v", m)
	}
	if first.Review.Meta.Branch != "feature" || first.Review.Meta.PRTitle != "feature" {
		t.Errorf("meta: %+v", first.Review.Meta)
	}
	p, _ := os.ReadFile(filepath.Join(first.RunDir, "prompts", "reviewer-r1.txt"))
	for _, want := range []string{"LOCAL COMMITS", "Base: main (", "Head: feature (", "BEGIN UNTRUSTED COMMIT MESSAGES", "    feature\n", "modified changed.txt (+1 -1)"} {
		if !strings.Contains(string(p), want) {
			t.Errorf("reviewer prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(string(p), "PULL REQUEST\nNumber") {
		t.Error("a local review must not be presented as a pull request")
	}
	lp, _ := os.ReadFile(filepath.Join(first.RunDir, "prompts", "lead.txt"))
	if !strings.Contains(string(lp), "LOCAL COMMITS") {
		t.Errorf("lead prompt:\n%s", lp)
	}
	var md bytes.Buffer
	if err := output.Markdown(&md, first.Review, output.Options{}); err != nil || !strings.Contains(md.String(), "**Local commits:** `feature`") {
		t.Errorf("markdown: %v\n%s", err, md.String())
	}

	// After a fix, the next review of the branch is measured against it.
	git("checkout", "-q", "feature")
	if err := os.WriteFile(filepath.Join(dir, "changed.txt"), []byte("a\nC\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "fix")
	second, err := a.Review(ctx, ReviewRequest{Since: SinceLast})
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.SinceRunID != m.ID || second.Manifest.SinceHeadSHA != m.HeadSHA {
		t.Errorf("--since last found %q at %s, want %q at %s", second.Manifest.SinceRunID, second.Manifest.SinceHeadSHA, m.ID, m.HeadSHA)
	}

	// A range reviews only the commits it names.
	third, err := a.Review(ctx, ReviewRequest{Revision: "HEAD~1..HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if third.Review.Meta.PRTitle != "fix" || third.Manifest.MergeBaseSHA != m.HeadSHA {
		t.Errorf("range: title %q, merge base %s, want %s", third.Review.Meta.PRTitle, third.Manifest.MergeBaseSHA, m.HeadSHA)
	}
	if third.Manifest.Branch != "HEAD~1..feature" {
		t.Errorf("range recorded as %q", third.Manifest.Branch)
	}
	// A review of a few commits is not one of the whole branch: the next
	// review of the branch is measured against the previous one of the
	// branch, and the range's own --since last finds the range's.
	fourth, err := a.Review(ctx, ReviewRequest{Revision: "feature", Since: SinceLast})
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Manifest.SinceRunID != second.Manifest.ID {
		t.Errorf("--since last on the branch found %q, want %q", fourth.Manifest.SinceRunID, second.Manifest.ID)
	}
	fifth, err := a.Review(ctx, ReviewRequest{Revision: "HEAD~1..HEAD", Since: SinceLast})
	if err != nil {
		t.Fatal(err)
	}
	if fifth.Manifest.SinceRunID != third.Manifest.ID {
		t.Errorf("--since last on the range found %q, want %q", fifth.Manifest.SinceRunID, third.Manifest.ID)
	}
}
