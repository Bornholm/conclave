package process

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/conclave/internal/testutil"
)

func run(t *testing.T, mode string, timeout time.Duration, maxOut int64) (*Result, error) {
	t.Helper()
	r := &ExecRunner{KillDelay: 500 * time.Millisecond}
	return r.Run(context.Background(), Request{
		Command: []string{testutil.TestAgent(t), mode},
		Dir:     t.TempDir(), Env: append(os.Environ(), "CONCLAVE_TESTAGENT_ID=x"),
		Stdin: []byte("prompt"), Timeout: timeout, MaxStdoutBytes: maxOut, MaxStderrBytes: 1024,
	})
}

func TestRunSuccess(t *testing.T) {
	res, err := run(t, "stderr", 10*time.Second, 1<<20)
	if err != nil || res.ExitCode != 0 || !strings.Contains(string(res.Stdout), `"schema_version"`) || !strings.Contains(string(res.Stderr), "agent log line") {
		t.Fatalf("unexpected: %v %+v", err, res)
	}
}

func TestRunExitCode(t *testing.T) {
	res, err := run(t, "exit-1", 10*time.Second, 1<<20)
	if err != nil || res.ExitCode != 1 || !strings.Contains(string(res.Stderr), "boom") {
		t.Fatalf("unexpected: %v %+v", err, res)
	}
}

func TestRunTimeout(t *testing.T) {
	start := time.Now()
	res, err := run(t, "timeout", 300*time.Millisecond, 1<<20)
	if err != nil || !res.TimedOut {
		t.Fatalf("unexpected: %v %+v", err, res)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout took too long")
	}
}

func TestRunTruncates(t *testing.T) {
	res, err := run(t, "too-large", 10*time.Second, 1000)
	if err != nil || !res.StdoutTruncated || len(res.Stdout) != 1000 {
		t.Fatalf("unexpected: %v truncated=%v len=%d", err, res.StdoutTruncated, len(res.Stdout))
	}
}

func TestRunNotFound(t *testing.T) {
	r := &ExecRunner{}
	_, err := r.Run(context.Background(), Request{Command: []string{"conclave-does-not-exist-xyz"}})
	if err == nil || !strings.Contains(err.Error(), "executable not found") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRunContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	r := &ExecRunner{KillDelay: 500 * time.Millisecond}
	_, err := r.Run(ctx, Request{Command: []string{testutil.TestAgent(t), "timeout"}, Timeout: time.Minute})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}
