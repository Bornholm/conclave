// Command testagent is a fake reviewer/lead used by the test suite. Its
// behaviour is selected by the first argument or the CONCLAVE_TESTAGENT_MODE
// environment variable. It reads the prompt from stdin, a file given by
// CONCLAVE_TESTAGENT_PROMPT_FILE or the remaining arguments.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	mode := os.Getenv("CONCLAVE_TESTAGENT_MODE")
	args := os.Args[1:]
	if mode == "" && len(args) > 0 {
		mode, args = args[0], args[1:]
	}
	prompt := readPrompt(args)
	id := os.Getenv("CONCLAVE_TESTAGENT_ID")
	if id == "" {
		id = "testagent"
	}
	if strings.Contains(prompt, "You are the lead reviewer") && mode == "valid" {
		mode = "lead"
	}
	switch mode {
	case "valid":
		fmt.Print(validReport(id))
	case "lead":
		fmt.Print(leadReport())
	case "envelope":
		inner := strings.ReplaceAll(validReport(id), `"`, `\"`)
		inner = strings.ReplaceAll(inner, "\n", `\n`)
		fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"result":"Here is the review:\n\n`+"```json\\n"+`%s\n`+"```"+`"}`, inner)
	case "invalid-json":
		fmt.Print("{this is not json")
	case "empty":
	case "stderr":
		fmt.Fprintln(os.Stderr, "agent log line")
		fmt.Print(validReport(id))
	case "too-large":
		fmt.Print(strings.Repeat("x", 4<<20))
	case "exit-1":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	case "timeout":
		time.Sleep(time.Minute)
	case "wrong-reviewer":
		fmt.Print(validReport("impostor"))
	case "echo-prompt":
		fmt.Fprint(os.Stderr, prompt)
		fmt.Print(validReport(id))
	case "echo-env":
		for _, kv := range os.Environ() {
			fmt.Fprintln(os.Stderr, kv)
		}
		fmt.Print(validReport(id))
	case "pi-json":
		rep := strings.ReplaceAll(strings.ReplaceAll(validReport(id), "\\", "\\\\"), `"`, `\"`)
		rep = strings.ReplaceAll(rep, "\n", `\n`)
		fmt.Println(`{"type":"session","version":3}`)
		fmt.Println(`{"type":"tool_execution_start","toolCallId":"t0","toolName":"read","args":{"path":"changed.txt"}}`)
		fmt.Println(`{"type":"tool_execution_end","toolCallId":"t0","toolName":"read","result":{"content":[]}}`)
		fmt.Println(`{"type":"message_end","message":{"role":"assistant","provider":"openrouter","model":"fake/model","content":[{"type":"text","text":"` + rep + `"}]}}`)
	case "lead-impostor":
		fmt.Print(strings.Replace(leadReport(), `"reported_by": ["r1"]`, `"reported_by": ["ghost"]`, 1))
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", mode)
		os.Exit(2)
	}
}

func readPrompt(args []string) string {
	if f := os.Getenv("CONCLAVE_TESTAGENT_PROMPT_FILE"); f != "" {
		data, _ := os.ReadFile(f)
		return string(data)
	}
	if len(args) > 0 {
		return strings.Join(args, " ")
	}
	data, _ := io.ReadAll(os.Stdin)
	return string(data)
}

func validReport(id string) string {
	return `{
  "schema_version": "1",
  "reviewer": {"id": "` + id + `", "model": "fake"},
  "summary": "Found one concurrency issue.",
  "findings": [
    {"id": "f1", "category": "concurrency", "severity": "high", "confidence": 0.9,
     "file": "changed.txt", "start_line": 2, "end_line": 2,
     "title": "Unsynchronized access", "description": "Line changed without lock.",
     "evidence": "changed.txt:2", "suggestion": "Add a mutex."},
    {"id": "f2", "category": "style", "severity": "banana", "confidence": 2,
     "file": "../etc/passwd", "start_line": 0, "end_line": 0,
     "title": "Bad finding", "description": "Should be rejected."}
  ],
  "questions": [{"question": "Is this intentional?"}],
  "verdict": "request_changes"
}`
}

func leadReport() string {
	return `{
  "schema_version": "1",
  "summary": "Consolidated: one real issue.",
  "verdict": "request_changes",
  "findings": [
    {"category": "concurrency", "severity": "high", "confidence": 0.9,
     "file": "changed.txt", "start_line": 2, "end_line": 2,
     "title": "Unsynchronized access", "description": "Confirmed in code.",
     "suggestion": "Add a mutex.", "reported_by": ["r1"]}
  ],
  "failed_reviewers": []
}`
}
