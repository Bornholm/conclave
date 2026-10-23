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
	"strconv"
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
	case "approving":
		fmt.Print(approvingReport(id))
	case "timeout-approving":
		// Wrote an approving report in time, then was stopped by the review.
		fmt.Print(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(approvingReport(id)) + `}]}}` + "\n")
		time.Sleep(time.Minute)
	case "lead":
		fmt.Print(leadReport())
	case "envelope":
		inner := strings.ReplaceAll(validReport(id), `"`, `\"`)
		inner = strings.ReplaceAll(inner, "\n", `\n`)
		fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"result":"Here is the review:\n\n`+"```json\\n"+`%s\n`+"```"+`"}`, inner)
	case "invalid-json":
		fmt.Print("{this is not json")
	case "prose-then-report":
		// Ends its first run on prose, like a model that forgot the output
		// contract, and returns the report when asked for it alone.
		if strings.Contains(prompt, "ended without the JSON report") {
			fmt.Print(validReport(id))
		} else {
			fmt.Print("I reviewed the change: B should stay lowercase. Now let me write the JSON.")
		}
	case "cut-short":
		// Stops after a word, like a model whose context window the prompt
		// filled, and would return a report if asked again.
		if strings.Contains(prompt, "ended without the JSON report") {
			fmt.Print(validReport(id))
		} else {
			fmt.Print("Let")
		}
	case "pi-api-error":
		// A Pi stream whose provider refused every call: Pi still exits 0,
		// and would return a report if asked again.
		if strings.Contains(prompt, "ended without the JSON report") {
			fmt.Print(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(validReport(id)) + `}]}}` + "\n")
		} else {
			fmt.Print(`{"type":"message_end","message":{"role":"assistant","content":[],"provider":"p","model":"m","stopReason":"error","errorMessage":"429: budget exceeded"}}` + "\n" +
				`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"429: budget exceeded"}` + "\n")
		}
	case "empty":
	case "stderr":
		fmt.Fprintln(os.Stderr, "agent log line")
		fmt.Print(validReport(id))
	case "too-large":
		fmt.Print(strings.Repeat("x", 4<<20))
	case "exit-1":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	case "exit-1-stdout":
		// Fails like Claude Code at its usage limit: the reason is in the
		// JSON envelope on standard output, standard error stays empty.
		fmt.Print(`{"type":"result","subtype":"success","is_error":true,"result":"You've hit your session limit"}`)
		os.Exit(1)
	case "timeout":
		time.Sleep(time.Minute)
	case "timeout-with-report":
		// An agent that paced itself: it wrote its report in time, then kept
		// going and was stopped. The report is on standard output already.
		if strings.Contains(prompt, "ended without the JSON report") {
			fmt.Print(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(validReport(id)) + `}]}}` + "\n")
			return
		}
		fmt.Print(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(validReport(id)) + `}]}}` + "\n")
		fmt.Fprint(os.Stderr, "still exploring\n")
		time.Sleep(time.Minute)
	case "prose-then-timeout":
		// Ends normally with prose and no report, so it is asked again; the
		// retry then runs out of time in turn.
		if strings.Contains(prompt, "ended without the JSON report") {
			time.Sleep(time.Minute)
			return
		}
		fmt.Print(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"I reviewed the diff but I am describing it in prose instead of writing the report."}]}}` + "\n")
	case "timeout-no-report":
		// Stopped in the middle of its work, with prose rather than a report.
		fmt.Print(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"I am still reading the changed files and have not written the report yet."}]}}` + "\n")
		time.Sleep(time.Minute)
	case "timeout-silent":
		// Stopped before it wrote anything at all.
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
	case "lead-cut":
		// Ends normally with a consolidation cut inside its last array.
		rep := leadReport()
		fmt.Print(rep[:len(rep)-len("]\n}")])
	case "triage-cut":
		// Ends normally, but the report stops inside its last string: what a
		// provider error cutting the stream short leaves behind.
		rep := triageReport(id, issueNumber(prompt))
		fmt.Print(rep[:len(rep)-len("\"\n}")])
	case "triage-lead-cut":
		rep := triageLead(issueNumbers(prompt))
		fmt.Print(rep[:len(rep)-len("]\n}")])
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
		// Every attribution names an agent that never answered.
		ghost := strings.ReplaceAll(askLead(), `["r1"]`, `["ghost"]`)
		fmt.Print(strings.Replace(ghost, `"reported_by": ["r1", "ghost"]`, `"reported_by": ["ghost"]`, 1))
	case "pi-json":
		rep := strings.ReplaceAll(strings.ReplaceAll(validReport(id), "\\", "\\\\"), `"`, `\"`)
		rep = strings.ReplaceAll(rep, "\n", `\n`)
		fmt.Println(`{"type":"session","version":3}`)
		fmt.Println(`{"type":"tool_execution_start","toolCallId":"t0","toolName":"read","args":{"path":"changed.txt"}}`)
		fmt.Println(`{"type":"tool_execution_end","toolCallId":"t0","toolName":"read","result":{"content":[]}}`)
		fmt.Println(`{"type":"message_end","message":{"role":"assistant","provider":"openrouter","model":"fake/model","content":[{"type":"text","text":"` + rep + `"}]}}`)
	case "pi-truncated":
		// A run stopped while the model was writing its report: the halves
		// arrive as deltas, the object never closes, and no message_end ever
		// comes. The cut falls inside a string, which is what a real stop
		// produces.
		// Cut inside the description of the first finding, which leaves the
		// verdict and the first finding's other fields readable: the report is
		// then repairable, and shorter than the one a complete run writes.
		rep := validReport(id)
		marker := `"description": "Line changed without lock."`
		i := strings.Index(rep, marker)
		if i < 0 {
			fmt.Fprint(os.Stderr, "pi-truncated: marker missing from validReport\n")
			os.Exit(2)
		}
		cut := i + len(`"description": "Line changed`)
		if cut >= len(rep) {
			// Never silently emit a complete report: the test would pass
			// without ever exercising the repair.
			fmt.Fprint(os.Stderr, "pi-truncated: the cut does not truncate\n")
			os.Exit(2)
		}
		fmt.Println(`{"type":"session","version":3}`)
		// The real stream opens the message before its deltas; a fixture that
		// skipped it would exercise a path the agent never produces.
		fmt.Println(`{"type":"message_start","message":{"role":"assistant","content":[]}}`)
		fmt.Println(`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"thinking it over. "}}`)
		fmt.Println(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":` + strconv.Quote(rep[:cut]) + `}}`)
		time.Sleep(time.Minute)
	case "lead-timeout":
		// A lead that writes its consolidation as a complete message, then is
		// stopped before the run ends.
		rep := strings.ReplaceAll(strings.ReplaceAll(leadReport(), "\\", "\\\\"), `"`, `\"`)
		rep = strings.ReplaceAll(rep, "\n", `\n`)
		fmt.Println(`{"type":"message_start","message":{"role":"assistant","content":[]}}`)
		fmt.Println(`{"type":"message_end","message":{"role":"assistant","provider":"fake","model":"fake/lead","content":[{"type":"text","text":"` + rep + `"}]}}`)
		time.Sleep(time.Minute)
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

// approvingReport is a complete reviewer report that found nothing.
func approvingReport(id string) string {
	return `{"schema_version":"1","reviewer":{"id":"` + id + `","model":"fake"},"summary":"Nothing to change.","findings":[],"verdict":"approve"}`
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
