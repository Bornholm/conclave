package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// errStraggler is the cause of a reviewer's context cancelled because the
// quorum was reached and its straggler grace ran out.
var errStraggler = errors.New("stopped once the other reviewers had reported")

// isStraggler reports whether ctx was cancelled to stop a straggler.
func isStraggler(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errStraggler)
}

// quorum stops the reviewers still running some time after enough of them
// have reported: the lead waits for the slowest reviewer, and one much slower
// than the others holds the whole review for little more than what it had
// already written, which the salvage keeps.
type quorum struct {
	need  int
	grace time.Duration
	log   *slog.Logger

	mu        sync.Mutex
	reported  int
	running   map[string]context.CancelCauseFunc
	timer     *time.Timer
	triggered bool
}

// newQuorum returns nil when there is nothing to stop: a grace of 0, or a
// quorum that takes every reviewer.
func newQuorum(need, total int, grace time.Duration, log *slog.Logger) *quorum {
	if grace <= 0 || need <= 0 || need >= total {
		return nil
	}
	return &quorum{need: need, grace: grace, log: log, running: map[string]context.CancelCauseFunc{}}
}

// start gives a reviewer the context it runs on, cancelled with errStraggler
// when it is stopped as one. A nil quorum hands ctx back.
func (q *quorum) start(ctx context.Context, id string) (context.Context, context.CancelCauseFunc) {
	actx, cancel := context.WithCancelCause(ctx)
	if q == nil {
		return actx, cancel
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.running[id] = cancel
	return actx, cancel
}

// done records that a reviewer ended, with a report or not. Only a report
// counts toward the quorum: a reviewer that failed says nothing about how long
// the others need.
func (q *quorum) done(id string, reported bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.running, id)
	if !reported {
		return
	}
	q.reported++
	if q.reported < q.need || q.triggered || len(q.running) == 0 {
		return
	}
	q.triggered = true
	q.log.Info("reviewer quorum reached, stopping the others after the straggler grace", "reports", q.reported, "grace", q.grace, "running", len(q.running))
	q.timer = time.AfterFunc(q.grace, q.stopStragglers)
}

func (q *quorum) stopStragglers() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, cancel := range q.running {
		q.log.Warn("stopping a straggler reviewer", "agent", id, "grace", q.grace)
		cancel(errStraggler)
	}
}

// stop releases the timer once every reviewer has ended.
func (q *quorum) stop() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.timer != nil {
		q.timer.Stop()
	}
}
