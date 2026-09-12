// Package process runs local commands with bounded output, timeouts and
// process group cleanup.
package process

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a process request.
type Runner interface {
	Run(ctx context.Context, req Request) (*Result, error)
}

// Request describes a process to run.
type Request struct {
	Command        []string
	Dir            string
	Env            []string
	Stdin          []byte
	Timeout        time.Duration
	MaxStdoutBytes int64
	MaxStderrBytes int64
}

// Result is the outcome of a completed process. A non-zero exit is not an error.
type Result struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Duration        time.Duration
	TimedOut        bool
}

// ErrNotFound is returned when the executable cannot be located.
var ErrNotFound = errors.New("executable not found")

// ExecRunner runs commands with os/exec.
type ExecRunner struct {
	// KillDelay is how long to wait after cancellation before SIGKILL.
	KillDelay time.Duration
}

// Run implements Runner.
func (r *ExecRunner) Run(ctx context.Context, req Request) (*Result, error) {
	if len(req.Command) == 0 {
		return nil, errors.New("empty command")
	}
	bin, err := exec.LookPath(req.Command[0])
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, req.Command[0])
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, bin, req.Command[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = req.Env
	if req.Stdin != nil {
		cmd.Stdin = strings.NewReader(string(req.Stdin))
	}
	stdout := newLimitedBuffer(req.MaxStdoutBytes)
	stderr := newLimitedBuffer(req.MaxStderrBytes)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	delay := r.KillDelay
	if delay <= 0 {
		delay = 5 * time.Second
	}
	cmd.WaitDelay = delay
	setProcessGroup(cmd, delay)

	start := time.Now()
	runErr := cmd.Run()
	res := &Result{
		Stdout: stdout.Bytes(), Stderr: stderr.Bytes(),
		StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
		Duration: time.Since(start),
	}
	if runCtx.Err() != nil && ctx.Err() == nil {
		res.TimedOut = true
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			if res.ExitCode < 0 {
				res.ExitCode = 128 + 9 // killed
			}
			return res, nil
		}
		if res.TimedOut {
			res.ExitCode = 124
			return res, nil
		}
		return res, fmt.Errorf("run %s: %w", req.Command[0], runErr)
	}
	return res, nil
}

type limitedBuffer struct {
	buf       []byte
	limit     int64
	truncated bool
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	if limit <= 0 {
		limit = 1 << 20
	}
	return &limitedBuffer{limit: limit}
}

// Write keeps the first limit bytes and drops the rest silently so the child
// never blocks on a full pipe.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - int64(len(b.buf))
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.buf = append(b.buf, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte { return b.buf }
