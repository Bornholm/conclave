package config

import (
	"fmt"
	"strings"
)

// EventStreamMinOutputBytes is the output cap below which an event-stream
// agent is likely to be truncated: the stream carries one event per token and
// the full content of every file the agent reads.
const EventStreamMinOutputBytes = 32 << 20

// MaxArgBytes is the Linux limit on the size of a single command-line argument
// (MAX_ARG_STRLEN, 32 pages). A prompt over that size makes the exec fail with
// E2BIG before the agent starts, which the kernel reports as "argument list
// too long". Other platforms cap the whole argument vector instead, at a size
// a prompt alone does not reach.
const MaxArgBytes = 128 << 10

// Warnings lists configuration problems that do not make the file invalid but
// are known to break a run. They are reported by `conclave config validate`
// and logged when a run starts.
func Warnings(cfg *Config) []string {
	var out []string
	if n := len(cfg.Reviewers()); cfg.Review.Quorum > 0 && n > 0 && cfg.Review.Quorum >= n && cfg.ReviewStragglerGrace() > 0 {
		out = append(out, fmt.Sprintf(
			"review.quorum is %d with %d reviewers: no reviewer is ever stopped as a straggler, every one is waited for; set it below %d, or set straggler_grace: 0",
			cfg.Review.Quorum, n, n))
	}
	for _, a := range cfg.Agents {
		// A diff over max_inline_diff_bytes goes to a file, so the prompt
		// carries at most the smaller of the two limits.
		if inline := min(cfg.Review.Limits.MaxDiffBytes, cfg.InlineDiffBytes()); a.Input == InputArgument && inline >= MaxArgBytes {
			out = append(out, fmt.Sprintf(
				"agent %s: input is argument but the prompt may inline %d bytes of diff (the smaller of max_diff_bytes and max_inline_diff_bytes), over the %d-byte limit for a single argument; a diff over that size fails the exec with \"argument list too long\" before the agent starts, set input: stdin or file, or lower max_inline_diff_bytes",
				a.ID, inline, MaxArgBytes))
		}
		if a.Input == InputArgument {
			// The lead is handed the question, the context and every answer
			// it has to consolidate, so its prompt is the larger of the two.
			askPrompt := cfg.Ask.Limits.MaxQuestionBytes + cfg.Ask.Limits.MaxContextBytes
			what := "max_question_bytes plus max_context_bytes"
			if a.Role == RoleLead {
				askPrompt += len(cfg.Respondents()) * cfg.Ask.Limits.MaxAnswerBytes
				what = "the question, the context and one answer per respondent"
			}
			if askPrompt >= MaxArgBytes {
				out = append(out, fmt.Sprintf(
					"agent %s: input is argument but an ask prompt may reach %d bytes (%s), over the %d-byte limit for a single argument; `conclave ask` fails the exec before the agent starts, set input: stdin or file, or lower ask.limits",
					a.ID, askPrompt, what, MaxArgBytes))
			}
		}
		if !emitsPiEventStream(a.Command) {
			continue
		}
		if a.Output != OutputPiJSON {
			out = append(out, fmt.Sprintf(
				"agent %s: command asks pi for the json event stream but output is %q; set output: pi-json or the report will be searched in the raw stream",
				a.ID, a.Output))
		}
		if limit := cfg.EffectiveMaxOutputBytes(a); limit < EventStreamMinOutputBytes {
			out = append(out, fmt.Sprintf(
				"agent %s: max_output_bytes is %d for a json event stream; the run will likely fail on truncation, %d is a safer value",
				a.ID, limit, EventStreamMinOutputBytes))
		}
	}
	return out
}

// emitsPiEventStream reports whether the command asks pi for its JSON event
// stream (`--mode json`), as opposed to a single JSON result.
func emitsPiEventStream(command []string) bool {
	if len(command) == 0 || !strings.HasSuffix(command[0], "pi") {
		return false
	}
	for i, arg := range command {
		if arg == "--mode=json" {
			return true
		}
		if arg == "--mode" && i+1 < len(command) && command[i+1] == "json" {
			return true
		}
	}
	return false
}
