package app

import (
	"baton/internal/verify"
	"baton/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExport(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := workspace.Git(repo, args...); err != nil {
			t.Fatal(out, err)
		}
	}
	repo, _ = workspace.Git(repo, "rev-parse", "--show-toplevel")
	s := &Service{Home: t.TempDir()}
	start := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Second)
	task := Task{ID: "20261011-090000-aaaa1111bbbb2222", Agent: "claude", Mode: "write", Repo: repo, Branch: "baton/x",
		Status: "failed", CreatedAt: start, Turns: []Turn{
			{Number: 1, Prompt: "给 /users 加分页", StartedAt: &start, EndedAt: &end, FinalText: "已加分页",
				Verify: &verify.Result{Checks: []verify.Outcome{{Command: "go vet ./...", Status: "pass"}, {Command: "go test ./...", Status: "fail"}}}},
			{Number: 2, Prompt: "补测试", StartedAt: &end, EndedAt: &end, Error: "agent exited with code 1"},
		}}
	dir := filepath.Join(s.Home, "tasks", task.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "request.json"), request{Task: task, Number: 2}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "task.json"), task); err != nil {
		t.Fatal(err)
	}
	tool := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t%d","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "turn-1.stdout"), []byte(strings.ReplaceAll(tool, "%d", "1")+strings.ReplaceAll(tool, "%d", "2")), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 2 { // a re-export replaces the file
		out, err := s.Export(ExportRequest{})
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(repo, ".baton", "history", start.Local().Format("2006-01-02")+"-给-users-加分页-bbbb2222.md")
		if len(out.Files) != 1 || out.Files[0] != want {
			t.Fatalf("files %q, want %q", out.Files, want)
		}
	}
	b, err := os.ReadFile(filepath.Join(repo, ".baton", "history", start.Local().Format("2006-01-02")+"-给-users-加分页-bbbb2222.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)
	for _, want := range []string{"# 给 /users 加分页\n", "claude · 写 · failed · ", " · baton/x\n", "## 第 1 轮 · ", " · 1m30s\n",
		"### 指令\n\n给 /users 加分页\n", "### 回复（2 个工具调用）\n\n已加分页\n", "- ✓ `go vet ./...`\n", "- ✗ `go test ./...`\n",
		"## 第 2 轮", "（没有回复）", "> 错误：agent exited with code 1"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if status, err := workspace.Git(repo, "status", "--porcelain", "--untracked-files=all"); err != nil || status != "" {
		t.Errorf("repository must stay clean for the next write task: %q %v", status, err)
	}
	if _, err := s.Export(ExportRequest{IDs: []string{"bbbb2222"}, Group: "g"}); err == nil {
		t.Error("ids with a group must be refused")
	}
	if out, err := s.Export(ExportRequest{Repo: repo}); err != nil || len(out.Files) != 1 {
		t.Errorf("every task of the repo: %v %v", out, err)
	}
}
