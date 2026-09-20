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
	for _, a := range cfg.Agents {
		if a.Input == InputArgument && cfg.Review.Limits.MaxDiffBytes >= MaxArgBytes {
			out = append(out, fmt.Sprintf(
				"agent %s: input is argument but max_diff_bytes is %d, over the %d-byte limit for a single argument; a diff over that size fails the exec with \"argument list too long\" before the agent starts, set input: stdin or file, or lower max_diff_bytes",
				a.ID, cfg.Review.Limits.MaxDiffBytes, MaxArgBytes))
		}
		if askPrompt := cfg.Ask.Limits.MaxQuestionBytes + cfg.Ask.Limits.MaxContextBytes; a.Input == InputArgument && askPrompt >= MaxArgBytes {
			out = append(out, fmt.Sprintf(
				"agent %s: input is argument but an ask prompt may reach %d bytes (max_question_bytes plus max_context_bytes), over the %d-byte limit for a single argument; `conclave ask` with a large context fails the exec before the agent starts, set input: stdin or file, or lower ask.limits",
				a.ID, askPrompt, MaxArgBytes))
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
