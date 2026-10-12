package app

import (
	"baton/internal/workspace"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))
	repo, other := t.TempDir(), t.TempDir()
	for _, dir := range []string{repo, other} {
		if out, err := workspace.Git(dir, "init", "-q"); err != nil {
			t.Fatal(out, err)
		}
	}
	repo, _ = workspace.Git(repo, "rev-parse", "--show-toplevel")
	other, _ = workspace.Git(other, "rev-parse", "--show-toplevel")
	touch := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	claudeFile := touch(filepath.Join(home, ".claude", "projects", "-wt", "c1.jsonl"))
	codexFile := touch(filepath.Join(home, "codex-home", "sessions", "2026", "10", "11", "rollout-2026-10-11T05-09-59-x1.jsonl"))

	s := &Service{Home: t.TempDir()}
	base := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	add := func(n int, task Task) {
		t.Helper()
		task.Status, task.CreatedAt = "succeeded", base.Add(time.Duration(n)*time.Minute)
		task.Turns = []Turn{{Number: 1, Prompt: "# 修复分页\n细节"}}
		dir := filepath.Join(s.Home, "tasks", task.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, "request.json"), request{Task: task, Number: 1}); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, "task.json"), task); err != nil {
			t.Fatal(err)
		}
	}
	add(1, Task{ID: "w", Agent: "claude", Mode: "write", Repo: repo, Worktree: "/wt/w", SessionID: "c1"})
	add(2, Task{ID: "r", Agent: "codex", Mode: "read", Repo: repo, BaseTask: "w", SessionID: "x1"})
	add(3, Task{ID: "q", Agent: "pi", Mode: "read", Repo: repo, SessionID: "../*"})
	add(4, Task{ID: "o", Agent: "codex", Mode: "write", Repo: other, Worktree: "/wt/o"})

	got, err := s.Sessions(filepath.Join(repo, "."))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Task != "q" || got[2].Task != "w" {
		t.Fatalf("got %+v, want q, r, w of repo", got)
	}
	q, r, w := got[0], got[1], got[2]
	if w.Dir != "/wt/w" || w.Transcript != claudeFile || w.Title != "修复分页" {
		t.Errorf("write task: %+v", w)
	}
	if r.Dir != "/wt/w" || r.Transcript != codexFile {
		t.Errorf("read task on base runs in the base worktree: %+v", r)
	}
	if q.Dir != repo || q.Transcript != "" {
		t.Errorf("read task runs in the repo; glob characters in an id find nothing: %+v", q)
	}
	if want := filepath.Join(s.Home, "tasks", "w", "turn-1.stdout"); len(w.Turns) != 1 || w.Turns[0] != want {
		t.Errorf("turns %q, want %q", w.Turns, want)
	}
	if all, err := s.Sessions(""); err != nil || len(all) != 4 {
		t.Errorf("all repositories: %d sessions, %v", len(all), err)
	}
}
