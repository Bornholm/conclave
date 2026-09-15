package agent

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/process"
	"github.com/bornholm/conclave/internal/testutil"
)

func testCfg(t *testing.T, mode, input string, cmdExtra ...string) (*config.Config, config.AgentConfig) {
	t.Helper()
	cfg := &config.Config{Review: config.ReviewConfig{AgentTimeout: 5 * time.Second, LeadTimeout: 5 * time.Second,
		Limits: config.LimitsConfig{MaxOutputBytes: 1 << 20}}}
	a := config.AgentConfig{ID: "r1", Role: config.RoleReviewer, Input: input,
		Command:     append([]string{testutil.TestAgent(t)}, cmdExtra...),
		Environment: map[string]string{"CONCLAVE_TESTAGENT_MODE": mode, "CONCLAVE_TESTAGENT_ID": "r1"}}
	return cfg, a
}

func newRunner(t *testing.T) *Runner {
	return &Runner{Process: &process.ExecRunner{KillDelay: 300 * time.Millisecond}, TempDir: t.TempDir()}
}

func TestRunModes(t *testing.T) {
	cases := map[string]string{
		"valid": "", "envelope": "", "stderr": "",
		"invalid-json": "", // runner does not parse
		"exit-1":       "exited with code 1: boom", "empty": "empty output",
		"timeout": "timed out",
	}
	for mode, wantErr := range cases {
		t.Run(mode, func(t *testing.T) {
			cfg, a := testCfg(t, mode, config.InputStdin)
			if mode == "timeout" {
				a.Timeout = 300 * time.Millisecond
			}
			ex := newRunner(t).Run(context.Background(), cfg, a, t.TempDir(), []byte("prompt"))
			if wantErr == "" && ex.Err != nil {
				t.Fatalf("unexpected error: %v", ex.Err)
			}
			if wantErr != "" && (ex.Err == nil || !strings.Contains(ex.Err.Error(), wantErr)) {
				t.Fatalf("want %q got %v", wantErr, ex.Err)
			}
			if wantErr == "" && mode != "invalid-json" {
				if _, _, err := ParseReport(ex.Result.Stdout, "r1", nil, nil, Limits{}); err != nil {
					t.Errorf("parse %s: %v", mode, err)
				}
			}
		})
	}
}

func TestRunTooLarge(t *testing.T) {
	cfg, a := testCfg(t, "too-large", config.InputStdin)
	cfg.Review.Limits.MaxOutputBytes = 1000
	ex := newRunner(t).Run(context.Background(), cfg, a, t.TempDir(), nil)
	if ex.Err == nil || !strings.Contains(ex.Err.Error(), "exceeded") {
		t.Fatalf("got %v", ex.Err)
	}
}

func TestRunMissingExecutable(t *testing.T) {
	cfg, a := testCfg(t, "valid", config.InputStdin)
	a.Command = []string{"conclave-missing-binary"}
	ex := newRunner(t).Run(context.Background(), cfg, a, t.TempDir(), nil)
	if ex.Err == nil || !strings.Contains(ex.Err.Error(), "not found") {
		t.Fatalf("got %v", ex.Err)
	}
}

func TestPromptDelivery(t *testing.T) {
	for _, input := range []string{config.InputStdin, config.InputArgument, config.InputFile} {
		t.Run(input, func(t *testing.T) {
			var extra []string
			if input == config.InputFile {
				extra = []string{"--prompt", config.PromptFilePlaceholder}
			}
			cfg, a := testCfg(t, "echo-prompt", input, extra...)
			if input == config.InputFile {
				a.Environment["CONCLAVE_TESTAGENT_PROMPT_FILE"] = "" // set below via placeholder
				a.Command = []string{testutil.TestAgent(t)}
				a.Environment["CONCLAVE_TESTAGENT_MODE"] = "echo-prompt"
				// testagent reads CONCLAVE_TESTAGENT_PROMPT_FILE; map the placeholder through env is not possible,
				// so pass the file path as argument and let the agent join args.
				a.Command = []string{testutil.TestAgent(t), config.PromptFilePlaceholder}
			}
			ex := newRunner(t).Run(context.Background(), cfg, a, t.TempDir(), []byte("THE PROMPT"))
			if ex.Err != nil {
				t.Fatal(ex.Err)
			}
			got := string(ex.Result.Stderr)
			if input == config.InputFile {
				if !strings.Contains(got, "prompt-r1-") {
					t.Errorf("file path not passed: %q", got)
				}
				return
			}
			if !strings.Contains(got, "THE PROMPT") {
				t.Errorf("prompt not delivered: %q", got)
			}
		})
	}
}

func TestRunPromptOverArgumentLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the per-argument limit is a Linux one")
	}
	cfg, a := testCfg(t, "echo-prompt", config.InputArgument)
	ex := newRunner(t).Run(context.Background(), cfg, a, t.TempDir(), make([]byte, config.MaxArgBytes))
	if ex.Err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(ex.Err.Error(), "set input to stdin or file") {
		t.Errorf("unhelpful error: %v", ex.Err)
	}
	if ex.Result != nil {
		t.Error("the agent should not have been started")
	}
}

func TestWorktreePlaceholder(t *testing.T) {
	cfg, a := testCfg(t, "echo-prompt", config.InputArgument, "--dir", config.WorktreePlaceholder)
	wt := t.TempDir()
	ex := newRunner(t).Run(context.Background(), cfg, a, wt, []byte("P"))
	if ex.Err != nil {
		t.Fatal(ex.Err)
	}
	if got := string(ex.Result.Stderr); !strings.Contains(got, "--dir "+wt) {
		t.Errorf("placeholder not substituted: %q", got)
	}
}

func TestBuildEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/evil")
	t.Setenv("GIT_WORK_TREE", "/evil")
	t.Setenv("KEEP_ME", "1")
	f := false
	env := strings.Join(BuildEnv(config.AgentConfig{ID: "a", Environment: map[string]string{"X": "y"}}, "/wt", "/tmp/x"), "\n")
	for _, bad := range []string{"GIT_DIR=", "GIT_WORK_TREE="} {
		if strings.Contains(env, bad) {
			t.Errorf("env leaks %s", bad)
		}
	}
	for _, want := range []string{"KEEP_ME=1", "X=y", "NO_COLOR=1", "CONCLAVE_WORKTREE=/wt", "TMPDIR=/tmp/x", "CONCLAVE_AGENT_ID=a"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	env = strings.Join(BuildEnv(config.AgentConfig{ID: "a", InheritEnv: &f}, "/wt", ""), "\n")
	if strings.Contains(env, "KEEP_ME") || !strings.Contains(env, "PATH="+os.Getenv("PATH")) {
		t.Errorf("isolated env wrong: %s", env)
	}
}

func TestCheck(t *testing.T) {
	if _, err := Check(config.AgentConfig{Command: []string{testutil.TestAgent(t)}}); err != nil {
		t.Error(err)
	}
	if _, err := Check(config.AgentConfig{Command: []string{"conclave-missing-binary"}}); err == nil {
		t.Error("expected error")
	}
}
