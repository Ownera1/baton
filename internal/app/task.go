package app

import (
	"baton/internal/agent"
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
	// Tokens is this turn's own usage in common terms, worked out from Usage
	// whenever the task is read, so older tasks get it too.
	Tokens  *agent.Tokens  `json:"tokens,omitempty"`
	CostUSD *float64       `json:"cost_usd,omitempty"`
	Error   string         `json:"error,omitempty"`
	Verify  *verify.Result `json:"verify,omitempty"`
}

type Task struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Agent string `json:"agent"`
	Role  string `json:"role,omitempty"`
	Group string `json:"group,omitempty"`
	// Instructions is the role's body when dispatched, put before turn 1.
	Instructions string `json:"instructions,omitempty"`
	AgentPath    string `json:"agent_path,omitempty"` // configured executable; empty means PATH lookup
	Mode         string `json:"mode"`
	Repo         string `json:"repo"`
	Worktree     string `json:"worktree"`
	Branch       string `json:"branch"`
	BaseCommit   string `json:"base_commit"`
	// BaseTask is the write task this one continues: a write task branches from
	// its latest commit, a read task runs in its worktree.
	BaseTask  string        `json:"base_task,omitempty"`
	Model     string        `json:"model,omitempty"`
	Effort    string        `json:"effort,omitempty"`
	Checks    []string      `json:"checks"`
	Status    string        `json:"status"`
	WorkerPID int           `json:"worker_pid"`
	SessionID string        `json:"session_id"`
	Turns     []Turn        `json:"turns"`
	Verify    verify.Result `json:"verify"`
	Diffstat  string        `json:"diffstat"`
	PatchPath string        `json:"patch_path"`
	Warnings  []string      `json:"warnings,omitempty"`
	// Unreadable marks a task List could not read; its one turn holds the error.
	Unreadable bool      `json:"unreadable,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type RunRequest struct {
	Title  string   `json:"title,omitempty" jsonschema:"Conversation title supplied by the dispatching agent; inferred from task when omitted"`
	Agent  string   `json:"agent,omitempty" jsonschema:"an agent or role listed by baton_agents; defaults to BATON_AGENT, the Baton config, then codex"`
	Repo   string   `json:"repo,omitempty" jsonschema:"git repository; defaults to the current directory"`
	Task   string   `json:"task"`
	Mode   string   `json:"mode,omitempty" jsonschema:"read or write; defaults to the role's mode, then write"`
	Group  string   `json:"group,omitempty" jsonschema:"label for tasks working on one goal, to list them together; a base task's group is inherited"`
	Base   string   `json:"base,omitempty" jsonschema:"a finished write task (id, unique prefix or suffix) to continue from: write branches from its latest commit and inherits its checks, read runs in its own detached worktree at that commit; repo defaults to its repository"`
	Model  string   `json:"model,omitempty"`
	Effort string   `json:"effort,omitempty"`
	Checks []string `json:"checks,omitempty" jsonschema:"check commands for a write task; omitted means detect from the project (.baton/checks, go.mod, Cargo.toml, Makefile test); a read task refuses them"`
	// NoChecks skips detection and any checks for a write task.
	NoChecks bool   `json:"no_checks,omitempty"`
	Timeout  string `json:"timeout,omitempty" jsonschema:"turn timeout, default 2h"`
	// FromMCP refuses agents hidden from the main agent.
	FromMCP bool `json:"-"`
}

type ReplyRequest struct {
	ID      string `json:"id" jsonschema:"task id from baton_run, or its unique prefix or suffix"`
	Message string `json:"msg"`
	Timeout string `json:"timeout,omitempty"`
	FromMCP bool   `json:"-"`
}

type Submitted struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type ListResult struct {
	Tasks []*Task `json:"tasks"`
}

// Brief is a task without its turns and check records, for MCP tools the main
// agent polls; baton_result has the rest.
type Brief struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Agent  string `json:"agent"`
	Role   string `json:"role,omitempty"`
	Group  string `json:"group,omitempty"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
	Branch string `json:"branch,omitempty"`
	Turns  int    `json:"turns"`
	// CheckStatus is the latest turn's, empty until its checks ran;
	// CheckNotes counts them.
	CheckStatus string    `json:"check_status,omitempty"`
	CheckNotes  string    `json:"check_notes,omitempty"`
	Error       string    `json:"error,omitempty"` // the latest turn's
	UpdatedAt   time.Time `json:"updated_at"`
}

func (t *Task) Brief() *Brief {
	b := &Brief{ID: t.ID, Title: t.Name(), Agent: t.Agent, Role: t.Role, Group: t.Group, Mode: t.Mode, Status: t.Status,
		Turns: len(t.Turns), UpdatedAt: t.UpdatedAt}
	if t.Mode == "write" {
		b.Branch = t.Branch
	}
	if len(t.Turns) > 0 {
		last := t.Turns[len(t.Turns)-1]
		b.Error = last.Error
		// Records older than per-turn checks keep them only in t.Verify.
		if v := last.Verify; v != nil || t.Verify.Round == last.Number {
			if v == nil {
				v = &t.Verify
			}
			b.CheckStatus, b.CheckNotes = v.Status, v.Notes
		}
	}
	return b
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
	// Parallel, MemoryMB and CPUs are the config when the turn was dispatched.
	Parallel int `json:"parallel,omitempty"`
	MemoryMB int `json:"memory_mb,omitempty"`
	CPUs     int `json:"cpus,omitempty"`
}

// Who names the role and its agent, or just the agent.
// countTokens sets each turn's Tokens from its usage record.
func (t *Task) countTokens() {
	usages := make([]json.RawMessage, len(t.Turns))
	for i, turn := range t.Turns {
		usages[i] = turn.Usage
	}
	for i, tokens := range agent.TurnTokens(t.Agent, usages) {
		if len(usages[i]) > 0 {
			t.Turns[i].Tokens = &tokens
		}
	}
}

func (t *Task) Who() string {
	if t.Role != "" {
		return t.Role + "（" + t.Agent + "）"
	}
	return t.Agent
}

var glyphs = map[string]string{"running": "●", "queued": "◌", "succeeded": "✓", "failed": "✗", "canceled": "■", "stale": "!"}

// Glyph marks a status in lists.
func Glyph(status string) string { return glyphs[status] }

// ModeName is the mode as lists show it.
func ModeName(mode string) string {
	if mode == "read" {
		return "读"
	}
	return "写"
}

// ShortID is the end of an id, unique enough to show and to resolve.
func ShortID(id string) string { return id[max(0, len(id)-8):] }

// Name is the title, or one inferred from the first prompt.
func (t *Task) Name() string {
	if t.Title != "" {
		return t.Title
	}
	if len(t.Turns) > 0 {
		return TitleFromPrompt(t.Turns[0].Prompt)
	}
	return "未命名对话"
}
