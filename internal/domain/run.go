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
	Kind      string     `json:"kind"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Status    RunStatus  `json:"status"`
	Error     string     `json:"error,omitempty"`

	Forge    string  `json:"forge"`
	Owner    string  `json:"owner"`
	Repo     string  `json:"repo"`
	PRNumber int64   `json:"pr_number,omitempty"`
	Issues   []int64 `json:"issues,omitempty"`

	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha"`
	MergeBaseSHA string `json:"merge_base_sha"`

	// SinceRunID and SinceHeadSHA name the previous review an incremental
	// review is measured against.
	SinceRunID   string `json:"since_run_id,omitempty"`
	SinceHeadSHA string `json:"since_head_sha,omitempty"`

	// ConfigSources are the configuration files the run was configured
	// from, the farthest inherited one first: an inherited file can declare
	// the agents that ran.
	ConfigSources []string `json:"config_sources,omitempty"`

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
	// ReportRetries counts the runs asking again for a missing report.
	ReportRetries int `json:"report_retries,omitempty"`
	// Salvaged marks a run stopped by its time budget whose report was usable
	// anyway, so the end result is not silently a report about less work.
	Salvaged bool     `json:"salvaged,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}
