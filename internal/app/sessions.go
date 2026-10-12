package app

import (
	"baton/internal/workspace"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session is one task's conversation: where the agent ran and where its record
// lives. A write task runs in its worktree, so agents that key sessions by
// directory file it apart from the repository; this maps it back.
type Session struct {
	Repo      string `json:"repo"`
	Task      string `json:"task"`
	Title     string `json:"title"`
	Agent     string `json:"agent"`
	Mode      string `json:"mode"`
	Status    string `json:"status"`
	Dir       string `json:"dir"` // where the agent ran
	SessionID string `json:"session_id"`
	// Transcript is the agent's own session file; empty when not found.
	Transcript string `json:"transcript,omitempty"`
	// Turns are Baton's copy of each turn's event stream.
	Turns     []string  `json:"turns"`
	CreatedAt time.Time `json:"created_at"`
}

// transcripts locate an agent's session file: env overrides dir (under home),
// glob takes the session id.
// ponytail: follows only these overrides (not PI_CODING_AGENT_SESSION_DIR);
// add one when an agent's session is reported missing.
var transcripts = map[string]struct{ env, dir, glob string }{
	"claude": {"CLAUDE_CONFIG_DIR", ".claude", "projects/*/%s.jsonl"},
	"codex":  {"CODEX_HOME", ".codex", "sessions/*/*/*/rollout-*-%s.jsonl"},
	"pi":     {"PI_CODING_AGENT_DIR", ".pi/agent", "sessions/*/*_%s.jsonl"},
	"grok":   {"", ".grok", "sessions/*/%s/chat_history.jsonl"},
	"agy":    {"", ".gemini/antigravity-cli", "conversations/%s.db"},
}

func transcript(agent, id string) string {
	p, ok := transcripts[agent]
	if !ok || id == "" || strings.ContainsAny(id, `*?[]/\`) {
		return ""
	}
	dir := os.Getenv(p.env)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, p.dir)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf(p.glob, id)))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// Sessions lists the conversations of tasks in the repository containing repo,
// or of every task when repo is "", newest first.
func (s *Service) Sessions(repo string) ([]Session, error) {
	if repo != "" {
		root, err := workspace.Git(repo, "rev-parse", "--show-toplevel")
		if err != nil {
			return nil, fmt.Errorf("%s is not inside a git repository: %w", repo, err)
		}
		repo = root
	}
	list, err := s.List("")
	if err != nil {
		return nil, err
	}
	byID := map[string]*Task{}
	for _, t := range list.Tasks {
		byID[t.ID] = t
	}
	out := []Session{}
	for _, t := range list.Tasks {
		if repo != "" && t.Repo != repo {
			continue
		}
		dir := t.Repo
		if t.Mode == "write" {
			dir = t.Worktree
		} else if base := byID[t.BaseTask]; base != nil {
			dir = base.Worktree
		}
		turns := make([]string, len(t.Turns))
		for i, turn := range t.Turns {
			turns[i] = filepath.Join(s.Home, "tasks", t.ID, fmt.Sprintf("turn-%d.stdout", turn.Number))
		}
		title := t.Title
		if title == "" && len(t.Turns) > 0 {
			title = TitleFromPrompt(t.Turns[0].Prompt)
		}
		out = append(out, Session{Repo: t.Repo, Task: t.ID, Title: title, Agent: t.Agent, Mode: t.Mode, Status: t.Status,
			Dir: dir, SessionID: t.SessionID, Transcript: transcript(t.Agent, t.SessionID), Turns: turns, CreatedAt: t.CreatedAt})
	}
	return out, nil
}
