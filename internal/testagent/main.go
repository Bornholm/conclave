// Command testagent is a fake reviewer/lead used by the test suite. Its
// behaviour is selected by the first argument or the CONCLAVE_TESTAGENT_MODE
// environment variable. It reads the prompt from stdin, a file given by
// CONCLAVE_TESTAGENT_PROMPT_FILE or the remaining arguments.
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
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
	case "triage":
		fmt.Print(triageReport(id, issueNumber(prompt)))
	case "triage-bad-label":
		fmt.Print(strings.Replace(triageReport(id, issueNumber(prompt)), `"type/bug"`, `"invented"`, 1))
	case "triage-no-evidence":
		rep := strings.Replace(triageReport(id, issueNumber(prompt)), `"still-present"`, `"fixed"`, 1)
		fmt.Print(strings.Replace(rep, `"evidence": "changed.txt:2"`, `"evidence": ""`, 1))
	case "triage-lead":
		fmt.Print(triageLead(issueNumbers(prompt)))
	case "triage-lead-ghost":
		fmt.Print(strings.Replace(triageLead(issueNumbers(prompt)), `"reported_by": ["r1"]`, `"reported_by": ["ghost"]`, 1))
	case "plan":
		fmt.Print(planReport(id, issueNumber(prompt)))
	case "plan-no-steps":
		fmt.Print(strings.Replace(planReport(id, issueNumber(prompt)), planSteps, `"steps": [{"id": "s0", "title": "", "details": ""}],`, 1))
	case "plan-lead":
		fmt.Print(planLead(issueNumber(prompt)))
	case "plan-lead-ghost":
		fmt.Print(strings.Replace(planLead(issueNumber(prompt)), `"reported_by": ["r1"]`, `"reported_by": ["ghost"]`, 1))
	case "ask":
		fmt.Print(answerReport(id))
	case "ask-empty":
		fmt.Print(strings.Replace(answerReport(id), `"answer": "`+askAnswer+`"`, `"answer": "  "`, 1))
	case "ask-lead":
		fmt.Print(askLead())
	case "ask-lead-badconf":
		fmt.Print(strings.Replace(askLead(), `"confidence": 0.9`, `"confidence": 2`, 1))
	case "ask-lead-ghost":
		fmt.Print(strings.Replace(askLead(), `"by": ["r1"]`, `"by": ["ghost"]`, 1))
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

// issueNumber reads the issue number out of the triage prompt, so the fake
// agent answers about the issue it was actually asked about.
func issueNumber(prompt string) string {
	m := regexp.MustCompile(`"number" must be (\d+)`).FindStringSubmatch(prompt)
	if len(m) == 2 {
		return m[1]
	}
	return "1"
}

// issueNumbers reads the batch the lead prompt lists.
func issueNumbers(prompt string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^- #(\d+): `).FindAllStringSubmatch(prompt, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		out = []string{"1"}
	}
	return out
}

func triageReport(id, number string) string {
	return `{
  "schema_version": "1",
  "reviewer": {"id": "` + id + `", "model": "fake"},
  "number": ` + number + `,
  "labels": ["type/bug", "nope"],
  "status": "still-present",
  "confidence": 0.8,
  "summary": "The described behaviour is still in the code.",
  "evidence": "changed.txt:2"
}`
}

func triageLead(numbers []string) string {
	var entries []string
	for i, n := range numbers {
		status, extra := "still-present", `"evidence": "changed.txt:2"`
		if i%2 == 1 {
			status, extra = "needs-info", `"evidence": "none", "question": "Which version?"`
		}
		entries = append(entries, `{"number": `+n+`, "labels": ["type/bug"], "status": "`+status+`",
      "confidence": 0.9, "summary": "Checked in the worktree.", `+extra+`, "reported_by": ["r1"]}`)
	}
	return `{
  "schema_version": "1",
  "summary": "Triaged ` + fmt.Sprint(len(numbers)) + ` issue(s).",
  "issues": [` + strings.Join(entries, ",\n    ") + `]
}`
}

// planSteps is extracted so a mode can replace the whole block.
const planSteps = `  "steps": [
    {"id": "s1", "title": "Add the retry loop", "details": "Wrap the call in changed.txt.",
     "files": ["changed.txt", "../etc/passwd"], "validation": "go test ./..."},
    {"id": "s2", "title": "Document the new flag", "details": "Mention it in the README.",
     "depends_on": ["s1", "s9"]}
  ],`

func planReport(id, number string) string {
	return `{
  "schema_version": "1",
  "reviewer": {"id": "` + id + `", "model": "fake"},
  "number": ` + number + `,
  "understanding": "The retry logic lives in changed.txt and stops after the first failure.",
  "approach": "Wrap the call in a bounded retry loop, as the rest of the package already does.",
  "alternatives": [{"approach": "Retry in the caller", "why_not": "every caller would repeat it"}],
` + planSteps + `
  "tests": ["a unit test covering the second attempt"],
  "risks": [{"description": "a slow call is now retried", "mitigation": "cap the total duration"}],
  "open_questions": ["How many attempts should be the default?"],
  "effort": "small",
  "confidence": 0.8
}`
}

func planLead(number string) string {
	return `{
  "schema_version": "1",
  "number": ` + number + `,
  "summary": "Add a bounded retry around the failing call.",
  "understanding": "Checked in the worktree: changed.txt holds the call.",
  "approach": "Bounded retry loop in changed.txt.",
  "steps": [
    {"id": "s1", "title": "Add the retry loop", "details": "Confirmed in the code.",
     "files": ["changed.txt"], "validation": "go test ./..."}
  ],
  "tests": ["a unit test covering the second attempt"],
  "risks": [{"description": "a slow call is now retried"}],
  "open_questions": ["How many attempts should be the default?"],
  "effort": "small",
  "confidence": 0.9,
  "reported_by": ["r1"]
}`
}

// askAnswer is extracted so a mode can replace the answer itself.
const askAnswer = "The retry lives in `changed.txt` and stops after the first failure."

func answerReport(id string) string {
	return `{
  "schema_version": "1",
  "reviewer": {"id": "` + id + `", "model": "fake"},
  "answer": "` + askAnswer + `",
  "key_points": ["It gives up after one attempt.", "  "],
  "references": [{"source": "changed.txt", "line": 2, "note": "the call"}, {"source": ""}],
  "caveats": ["Read only the checked-out revision."],
  "open_questions": ["How many attempts are wanted?"],
  "confidence": 0.8
}`
}

func askLead() string {
	return `{
  "schema_version": "1",
  "answer": "Checked in the worktree: the call in changed.txt is not retried.",
  "key_points": ["One attempt, no retry."],
  "disagreements": [{"topic": "How many attempts", "positions": [
    {"by": ["r1"], "position": "three"}, {"by": ["ghost"], "position": "ten"}]}],
  "references": [{"source": "changed.txt", "line": 2}],
  "caveats": ["Read only the checked-out revision."],
  "open_questions": ["How many attempts are wanted?"],
  "confidence": 0.9,
  "reported_by": ["r1", "ghost"]
}`
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
