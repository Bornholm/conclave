package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/artifact"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/output"
	"github.com/bornholm/conclave/internal/prompt"
)

// CommentRequest asks for the comment that publishes a review.
type CommentRequest struct {
	// Result is the review, and the run whose directory keeps the comment.
	Result *ReviewResult
	// PR is the pull request reviewed: its title and description set the
	// language of the comment.
	PR               *domain.PullRequest
	Round, MaxRounds int
}

// Comment is what a writer made of a review.
type Comment struct {
	Body     string
	Language string
	Warnings []string
}

// WriteComment has the loop's writer turn a review into the comment
// published on the pull request, in the pull request's language. The
// comment is kept in the run directory, next to the review.
func (a *App) WriteComment(ctx context.Context, req CommentRequest) (*Comment, error) {
	cfg := a.Config
	log := a.logger()
	review := req.Result.Review
	if review == nil {
		return nil, errors.New("no review to write a comment for")
	}
	store, err := artifact.Open(filepath.Dir(req.Result.RunDir), filepath.Base(req.Result.RunDir))
	if err != nil {
		return nil, err
	}
	// The writer needs what the author is told, not how the run went.
	var md bytes.Buffer
	if err := output.Markdown(&md, review, output.Options{}); err != nil {
		return nil, err
	}
	wtBase := a.WorktreeBase
	if wtBase == "" {
		wtBase = filepath.Join(os.TempDir(), "conclave")
	}
	if err := os.MkdirAll(wtBase, 0o755); err != nil {
		return nil, fmt.Errorf("create working directory: %w", err)
	}
	dir, err := os.MkdirTemp(wtBase, "comment-")
	if err != nil {
		return nil, fmt.Errorf("create working directory: %w", err)
	}
	defer os.RemoveAll(dir)
	workdir, tmp := filepath.Join(dir, "work"), filepath.Join(dir, "tmp")
	for _, d := range []string{workdir, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("create working directory: %w", err)
		}
	}

	writer := cfg.LoopWriter()
	p, err := prompt.Comment(prompt.CommentInput{
		AgentID: writer.ID, Worktree: workdir, PR: req.PR, Review: md.String(),
		Verdict: review.Verdict, Round: req.Round, MaxRounds: req.MaxRounds,
		Incremental: review.Meta.Since != nil, MaxBytes: cfg.Loop.MaxCommentBytes,
	})
	if err != nil {
		return nil, err
	}
	name := "comment-" + writer.ID
	_ = store.Write("prompts", name+".txt", p)
	log.Info("writer started", "agent", writer.ID, "round", req.Round)
	runner := &agent.Runner{Process: a.process(), TempDir: tmp}
	res := runner.Run(ctx, cfg, writer, workdir, p)
	if res.Result != nil {
		_ = store.Write("raw", name+".stdout", res.Result.Stdout)
		_ = store.Write("raw", name+".stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		return nil, fmt.Errorf("writer %s: %w", writer.ID, res.Err)
	}
	ex := domain.AgentExecution{ID: writer.ID, Role: writer.Role, Worktree: workdir}
	ad, err := a.adapt(&run{store: store}, writer, res.Result.Stdout, &ex)
	if err != nil {
		return nil, fmt.Errorf("writer %s: %w", writer.ID, err)
	}
	rep, warnings, err := agent.ParseCommentReport(ad.Report, cfg.Loop.MaxCommentBytes)
	if err != nil {
		return nil, fmt.Errorf("writer %s: invalid comment: %w", writer.ID, err)
	}
	warnings = append(ex.Warnings, warnings...)
	_ = store.WriteJSON("final", "comment.json", rep)
	_ = store.Write("final", "comment.md", []byte(rep.Body+"\n"))
	if missing := missingFindings(review, rep.Body); len(missing) > 0 {
		return nil, fmt.Errorf("writer %s: the comment leaves out the finding(s) at %s, rated medium or above", writer.ID, strings.Join(missing, ", "))
	}
	log.Info("writer finished", "agent", writer.ID, "language", rep.Language, "bytes", len(rep.Body), "duration", res.Duration.Round(time.Millisecond))
	return &Comment{Body: rep.Body, Language: rep.Language, Warnings: warnings}, nil
}

// missingFindings returns the files of the findings rated medium or above
// that a comment does not name. The writer is told to keep them all, and a
// comment published under the user's account that silently drops one says
// less than the decision it comes with. The file path is the one part of a
// finding no translation changes.
func missingFindings(review *domain.ConsolidatedReview, body string) []string {
	var missing []string
	for _, f := range review.Findings {
		if f.Severity.Rank() > domain.SeverityMedium.Rank() || f.File == "" || strings.Contains(body, f.File) || slices.Contains(missing, f.File) {
			continue
		}
		missing = append(missing, f.File)
	}
	return missing
}
