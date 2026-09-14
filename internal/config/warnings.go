package config

import (
	"fmt"
	"strings"
)

// EventStreamMinOutputBytes is the output cap below which an event-stream
// agent is likely to be truncated: the stream carries one event per token and
// the full content of every file the agent reads.
const EventStreamMinOutputBytes = 32 << 20

// Warnings lists configuration problems that do not make the file invalid but
// are known to break a run. They are reported by `conclave config validate`
// and logged when a run starts.
func Warnings(cfg *Config) []string {
	var out []string
	for _, a := range cfg.Agents {
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
