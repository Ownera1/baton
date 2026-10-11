package app

import (
	"baton/internal/verify"
	"encoding/json"
	"time"
)

type Turn struct {
	Number    int             `json:"number"`
	Prompt    string          `json:"prompt"`
	StartedAt *time.Time      `json:"started_at,omitempty"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
	ExitCode  int             `json:"exit_code"`
	FinalText string          `json:"final_text"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	CostUSD   *float64        `json:"cost_usd,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type Task struct {
	ID         string        `json:"id"`
	Agent      string        `json:"agent"`
	Mode       string        `json:"mode"`
	Repo       string        `json:"repo"`
	Worktree   string        `json:"worktree"`
	Branch     string        `json:"branch"`
	BaseCommit string        `json:"base_commit"`
	Model      string        `json:"model,omitempty"`
	Effort     string        `json:"effort,omitempty"`
	Checks     []string      `json:"checks"`
	Status     string        `json:"status"`
	WorkerPID  int           `json:"worker_pid"`
	SessionID  string        `json:"session_id"`
	Turns      []Turn        `json:"turns"`
	Verify     verify.Result `json:"verify"`
	Diffstat   string        `json:"diffstat"`
	PatchPath  string        `json:"patch_path"`
	Warnings   []string      `json:"warnings,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

type RunRequest struct {
	Agent   string   `json:"agent"`
	Repo    string   `json:"repo"`
	Task    string   `json:"task"`
	Mode    string   `json:"mode,omitempty"`
	Model   string   `json:"model,omitempty"`
	Effort  string   `json:"effort,omitempty"`
	Checks  []string `json:"checks,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
}

type ReplyRequest struct {
	ID      string `json:"id"`
	Message string `json:"msg"`
	Timeout string `json:"timeout,omitempty"`
}

type Submitted struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type ListResult struct {
	Tasks []*Task `json:"tasks"`
}
type Result struct {
	*Task
	FinalText string          `json:"final_text"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	CostUSD   *float64        `json:"cost_usd,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// request.json is the CLI-to-worker mailbox. Only a worker writes task.json.
type request struct {
	Task    Task          `json:"task"`
	Prompt  string        `json:"prompt"`
	Number  int           `json:"number"`
	Timeout time.Duration `json:"timeout"`
}
