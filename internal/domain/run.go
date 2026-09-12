package domain

import "time"

// RunStatus is the lifecycle state of a run.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// RunManifest is persisted in the run directory.
type RunManifest struct {
	ID        string     `json:"id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Status    RunStatus  `json:"status"`
	Error     string     `json:"error,omitempty"`

	Forge    string `json:"forge"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	PRNumber int64  `json:"pr_number"`

	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha"`
	MergeBaseSHA string `json:"merge_base_sha"`

	Agents []AgentExecution `json:"agents"`
}

// AgentExecution records how one agent ran.
type AgentExecution struct {
	ID        string        `json:"id"`
	Role      string        `json:"role"`
	Worktree  string        `json:"worktree"`
	Model     string        `json:"model,omitempty"`
	ToolCalls int           `json:"tool_calls,omitempty"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration"`
	Succeeded bool          `json:"succeeded"`
	Error     string        `json:"error,omitempty"`
	Warnings  []string      `json:"warnings,omitempty"`
}
