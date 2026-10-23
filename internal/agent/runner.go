package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/process"
)

// Execution is the raw outcome of running an agent command.
type Execution struct {
	Result   *process.Result
	Err      error
	Duration time.Duration
	// TimedOut reports that the run used its whole budget and was stopped.
	// What it wrote is still in Result, unlike a run that failed.
	TimedOut bool
	// Timeout is the budget that was applied, after clamping to the context.
	Timeout time.Duration
}

// Runner executes configured agents inside their worktree.
type Runner struct {
	Process process.Runner
	// TempDir hosts prompt files for input=file agents.
	TempDir string
}

// Run executes the agent with the prompt and returns its raw result. A
// non-zero exit code, timeout or truncation is reported through Execution.Err
// while Result stays available for artifacts.
func (r *Runner) Run(ctx context.Context, cfg *config.Config, a config.AgentConfig, worktree string, prompt []byte) Execution {
	command := make([]string, len(a.Command))
	for i, arg := range a.Command {
		command[i] = strings.ReplaceAll(arg, config.WorktreePlaceholder, worktree)
	}
	timeout := EffectiveTimeout(ctx, cfg, a)
	req := process.Request{
		Command:        command,
		Dir:            worktree,
		Env:            BuildEnv(a, worktree, r.TempDir, timeout),
		Timeout:        timeout,
		MaxStdoutBytes: cfg.EffectiveMaxOutputBytes(a),
		MaxStderrBytes: cfg.EffectiveMaxOutputBytes(a),
	}
	switch a.Input {
	case config.InputArgument:
		if runtime.GOOS == "linux" && len(prompt) >= config.MaxArgBytes {
			return Execution{Err: fmt.Errorf(
				"prompt is %d bytes, over the %d-byte limit for a single argument: set input to stdin or file for agent %s, or lower max_inline_diff_bytes",
				len(prompt), config.MaxArgBytes, a.ID)}
		}
		req.Command = append(req.Command, string(prompt))
	case config.InputFile:
		f, err := os.CreateTemp(r.TempDir, "prompt-"+a.ID+"-*.txt")
		if err != nil {
			return Execution{Err: fmt.Errorf("write prompt file: %w", err)}
		}
		if _, err := f.Write(prompt); err != nil {
			f.Close()
			return Execution{Err: fmt.Errorf("write prompt file: %w", err)}
		}
		f.Close()
		defer os.Remove(f.Name())
		for i, arg := range req.Command {
			req.Command[i] = strings.ReplaceAll(arg, config.PromptFilePlaceholder, f.Name())
		}
		req.Env = append(req.Env, "CONCLAVE_PROMPT_FILE="+f.Name())
	default:
		req.Stdin = prompt
	}
	start := time.Now()
	res, err := r.Process.Run(ctx, req)
	ex := Execution{Result: res, Duration: time.Since(start), Timeout: timeout}
	ex.TimedOut = res != nil && res.TimedOut
	switch {
	case err != nil:
		ex.Err = err
	case res.TimedOut:
		ex.Err = fmt.Errorf("timed out after %s", timeout.Round(time.Second))
	case res.ExitCode != 0:
		ex.Err = fmt.Errorf("exited with code %d: %s", res.ExitCode, exitReason(res.Stderr, res.Stdout))
	case res.StdoutTruncated:
		ex.Err = fmt.Errorf("output exceeded %d bytes: raise max_output_bytes for agent %s", cfg.EffectiveMaxOutputBytes(a), a.ID)
	case len(res.Stdout) == 0:
		ex.Err = errors.New("empty output")
	}
	return ex
}

// exitReason explains a failed run from the last line of standard error. An
// agent that reports errors on standard output, as Claude Code does in its
// JSON envelope at its usage limit, leaves standard error empty: its "result"
// field, or else the last line of standard output, is used instead.
func exitReason(stderr, stdout []byte) string {
	if len(bytes.TrimSpace(stderr)) > 0 {
		return lastLine(stderr)
	}
	var env struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &env) == nil && strings.TrimSpace(env.Result) != "" {
		return lastLine([]byte(env.Result))
	}
	return lastLine(stdout)
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 200 {
		l = CutRunes(l, 200) + "…"
	}
	return l
}

// EffectiveTimeout is the wall-clock limit of one agent run: its own timeout,
// or the shorter of the configured one and what the parent context leaves.
// Exporting it to the child keeps its own deadline aligned with this one, so
// an agent that paces itself has written its report before it is killed.
func EffectiveTimeout(ctx context.Context, cfg *config.Config, a config.AgentConfig) time.Duration {
	timeout := cfg.EffectiveTimeout(a)
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left < timeout {
			timeout = left
		}
	}
	return timeout
}

// BuildEnv computes the child environment: optionally the parent environment
// minus variables that would redirect Git or leak the run, then fixed values,
// then the agent's explicit variables. A positive timeout is exported as
// CONCLAVE_AGENT_DEADLINE_MS and CONCLAVE_DEADLINE_AT for agents that can pace
// themselves, such as the pi deadline extension.
func BuildEnv(a config.AgentConfig, worktree, tmpDir string, timeout time.Duration) []string {
	env := map[string]string{}
	if a.InheritsEnv() {
		for _, kv := range os.Environ() {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.HasPrefix(k, "GIT_") && config.IsProtectedEnv(k) || k == "GIT_NAMESPACE" {
				continue
			}
			env[k] = v
		}
	} else {
		for _, k := range []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "SHELL"} {
			if v, ok := os.LookupEnv(k); ok {
				env[k] = v
			}
		}
	}
	env["NO_COLOR"] = "1"
	env["TERM"] = "dumb"
	env["CI"] = "1"
	env["CONCLAVE_AGENT_ID"] = a.ID
	env["CONCLAVE_WORKTREE"] = worktree
	if timeout > 0 {
		// The budget is what the child can act on; the deadline is only there
		// for an agent that wants to reason about the wall clock.
		env["CONCLAVE_AGENT_DEADLINE_MS"] = strconv.FormatInt(timeout.Milliseconds(), 10)
		env["CONCLAVE_DEADLINE_AT"] = time.Now().Add(timeout).UTC().Format(time.RFC3339)
	}
	if tmpDir != "" {
		env["TMPDIR"] = tmpDir
	}
	for k, v := range a.Environment {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Check verifies that the agent executable can be located.
func Check(a config.AgentConfig) (string, error) {
	if len(a.Command) == 0 {
		return "", errors.New("empty command")
	}
	p, err := lookPath(a.Command[0])
	if err != nil {
		return "", err
	}
	return p, nil
}

func lookPath(name string) (string, error) {
	if filepath.IsAbs(name) || strings.Contains(name, string(filepath.Separator)) {
		if st, err := os.Stat(name); err != nil || st.IsDir() {
			return "", fmt.Errorf("executable %q not found", name)
		}
		return name, nil
	}
	return execLookPath(name)
}
