package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/prompt"
)

// The lines the lead is given around each finding. Measured on real runs,
// the lead spent most of its time in turns that only read the cited lines
// again, each turn costing it close to a minute of reasoning: given here, a
// finding the reviewers located right is checked without a tool call.
const (
	excerptContext   = 6
	excerptMaxLines  = 60
	excerptLineWidth = 240
	excerptsMaxBytes = 64 << 10
	excerptFileMax   = 2 << 20
	// excerptsListed bounds the ranges a note names.
	excerptsListed = 20
)

// lineRange is a range of lines, with the rank of the most severe finding
// citing it.
type lineRange struct{ start, end, rank int }

// excerpts returns the lines the findings cite, at the head commit.
func (r *run) excerpts(ctx context.Context, log *slog.Logger, reports []domain.AgentReport) ([]prompt.Excerpt, []string) {
	read := func(file string) ([]byte, error) {
		content, err := r.git.FileAt(ctx, r.pr.Head.SHA, file, excerptFileMax)
		if err != nil {
			// The findings' paths were checked to exist, but a directory does.
			log.Warn("cannot give the lead the lines a finding cites", "file", file, "error", err)
		}
		return content, err
	}
	return collectExcerpts(reports, read)
}

// collectExcerpts returns the cited lines with a few lines around them,
// merged per file and cut into ranges of at most excerptMaxLines lines. The
// budget of excerptsMaxBytes goes to the most severe findings first; a range
// that does not fit leaves room for a smaller one after it. The notes tell
// the lead what it was not given, by file and line.
func collectExcerpts(reports []domain.AgentReport, read func(file string) ([]byte, error)) ([]prompt.Excerpt, []string) {
	ranges := map[string][]lineRange{}
	// cited keeps each finding's own lines, for the notes.
	cited := map[string][]lineRange{}
	for _, rep := range reports {
		for _, f := range rep.Findings {
			if f.File == "" || f.StartLine < 1 {
				continue
			}
			last := max(f.EndLine, f.StartLine)
			start := max(1, f.StartLine-excerptContext)
			end := min(last+excerptContext, start+excerptMaxLines-1)
			ranges[f.File] = append(ranges[f.File], lineRange{start, end, f.Severity.Rank()})
			cited[f.File] = append(cited[f.File], lineRange{f.StartLine, last, 0})
		}
	}
	type chunk struct {
		file string
		lineRange
		text string
	}
	var chunks []chunk
	var notes []string
	lengths := map[string]int{}
	files := make([]string, 0, len(ranges))
	for file := range ranges {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		content, err := read(file)
		if err != nil || bytes.IndexByte(content, 0) >= 0 {
			notes = append(notes, fmt.Sprintf("%s is not given (not a file at head, binary or over %d MB): read it in the worktree if it matters.", file, excerptFileMax>>20))
			continue
		}
		lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
		merged := mergeRanges(ranges[file])
		lengths[file] = len(lines)
		seen := map[lineRange]bool{}
		for _, c := range cited[file] {
			if !seen[c] && c.start > len(lines) {
				notes = append(notes, fmt.Sprintf("%s:%d-%d is cited past the end of the file, which has %d lines: the finding citing it may be wrong.", file, c.start, c.end, len(lines)))
			}
			seen[c] = true
		}
		for _, lr := range merged {
			if lr.start > len(lines) {
				continue
			}
			lr.end = min(lr.end, len(lines))
			for start := lr.start; start <= lr.end; start += excerptMaxLines {
				c := chunk{file: file, lineRange: lineRange{start, min(lr.end, start+excerptMaxLines-1), lr.rank}}
				var b strings.Builder
				for i := c.start; i <= c.end; i++ {
					line := lines[i-1]
					if len(line) > excerptLineWidth {
						line = strings.ToValidUTF8(line[:excerptLineWidth], "") + " […]"
					}
					fmt.Fprintf(&b, "%6d  %s\n", i, line)
				}
				c.text = b.String()
				chunks = append(chunks, c)
			}
		}
	}
	// The most severe findings first, then in file order, for the budget.
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].rank < chunks[j].rank })
	size := 0
	var omitted []string
	kept := chunks[:0]
	for _, c := range chunks {
		if size+len(c.text) > excerptsMaxBytes {
			omitted = append(omitted, fmt.Sprintf("%s:%d-%d", c.file, c.start, c.end))
			continue
		}
		size += len(c.text)
		kept = append(kept, c)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].file != kept[j].file {
			return kept[i].file < kept[j].file
		}
		return kept[i].start < kept[j].start
	})
	out := make([]prompt.Excerpt, 0, len(kept))
	given := map[string][]lineRange{}
	for _, c := range kept {
		out = append(out, prompt.Excerpt{File: c.file, Start: c.start, End: c.end, Text: c.text})
		given[c.file] = append(given[c.file], c.lineRange)
	}
	// Once the budget is spent: a citation is noted when the lines given hold
	// its start but not its end. One whose start is not given at all is in
	// the ranges left out for size.
	for _, file := range files {
		seen := map[lineRange]bool{}
		for _, c := range cited[file] {
			if seen[c] || c.start > lengths[file] {
				continue
			}
			seen[c] = true
			end := min(c.end, lengths[file])
			// Merged ranges neither overlap nor touch: the cited lines are all
			// given when one range holds them.
			for _, lr := range mergeRanges(given[file]) {
				if lr.start <= c.start && c.start <= lr.end && lr.end < end {
					notes = append(notes, fmt.Sprintf("%s:%d-%d is cited, only lines %d-%d are given: read the rest in the worktree if it matters.", file, c.start, c.end, lr.start, lr.end))
				}
			}
		}
	}
	if len(omitted) > 0 {
		// omitted is in budget order: the most severe findings' ranges first.
		list := strings.Join(omitted[:min(len(omitted), excerptsListed)], ", ")
		if more := len(omitted) - excerptsListed; more > 0 {
			list += fmt.Sprintf(" and %d more", more)
		}
		notes = append(notes, fmt.Sprintf("Left out for size, the most severe findings' first: %s. Read them in the worktree if they matter.", list))
	}
	return out, notes
}

// mergeRanges returns the ranges sorted, with the ones that overlap or touch
// joined under the rank of the most severe.
func mergeRanges(in []lineRange) []lineRange {
	in = slices.Clone(in)
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	var out []lineRange
	for _, lr := range in {
		if n := len(out); n > 0 && lr.start <= out[n-1].end+1 {
			out[n-1].end = max(out[n-1].end, lr.end)
			out[n-1].rank = min(out[n-1].rank, lr.rank)
			continue
		}
		out = append(out, lr)
	}
	return out
}

// leadPrompt renders the lead prompt, without the excerpts when they would
// put a prompt passed as an argument over the single-argument limit the
// runner enforces on Linux: they save the lead reads, they must not cost it
// the run.
func leadPrompt(in prompt.LeadInput, input string) ([]byte, bool, error) {
	p, err := prompt.Lead(in)
	if err != nil || input != config.InputArgument || runtime.GOOS != "linux" || len(p) < config.MaxArgBytes || len(in.Excerpts) == 0 {
		return p, false, err
	}
	in.Excerpts, in.ExcerptNotes = nil, nil
	p, err = prompt.Lead(in)
	return p, true, err
}
