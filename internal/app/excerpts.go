package app

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

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
	excerptsMaxBytes = 64 << 10
	excerptFileMax   = 2 << 20
)

type lineRange struct{ start, end int }

// excerpts returns the lines the findings cite, at the head commit, with a
// few lines around them, merged per file. Past excerptsMaxBytes, the ranges
// left are counted in omitted rather than given.
func (r *run) excerpts(ctx context.Context, reports []domain.AgentReport) (out []prompt.Excerpt, omitted int) {
	ranges := map[string][]lineRange{}
	for _, rep := range reports {
		for _, f := range rep.Findings {
			if f.File == "" || f.StartLine < 1 {
				continue
			}
			end := max(f.EndLine, f.StartLine)
			end = min(end, f.StartLine+excerptMaxLines-1)
			ranges[f.File] = append(ranges[f.File], lineRange{max(1, f.StartLine-excerptContext), end + excerptContext})
		}
	}
	files := make([]string, 0, len(ranges))
	for file := range ranges {
		files = append(files, file)
	}
	sort.Strings(files)
	size := 0
	for _, file := range files {
		merged := mergeRanges(ranges[file])
		content, err := r.git.FileAt(ctx, r.pr.Head.SHA, file, excerptFileMax)
		if err != nil || bytes.IndexByte(content, 0) >= 0 {
			// Gone, too large or binary: the lead reads it itself if it must.
			omitted += len(merged)
			continue
		}
		lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
		for _, lr := range merged {
			if lr.start > len(lines) {
				continue
			}
			lr.end = min(lr.end, len(lines))
			var b strings.Builder
			for i := lr.start; i <= lr.end; i++ {
				fmt.Fprintf(&b, "%6d  %s\n", i, lines[i-1])
			}
			if size+b.Len() > excerptsMaxBytes {
				omitted++
				continue
			}
			size += b.Len()
			out = append(out, prompt.Excerpt{File: file, Start: lr.start, End: lr.end, Text: b.String()})
		}
	}
	return out, omitted
}

// mergeRanges sorts ranges and joins the ones that overlap or touch.
func mergeRanges(in []lineRange) []lineRange {
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	var out []lineRange
	for _, lr := range in {
		if n := len(out); n > 0 && lr.start <= out[n-1].end+1 {
			out[n-1].end = max(out[n-1].end, lr.end)
			continue
		}
		out = append(out, lr)
	}
	return out
}
