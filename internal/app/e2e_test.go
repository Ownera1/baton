package app_test

import (
	"baton/internal/app"
	"baton/internal/guard"
	"baton/internal/workspace"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEndToEnd(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "baton")
	build := exec.Command("go", "build", "-o", bin, "./cmd/baton")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(dir, "home")
	fakeBin := filepath.Join(dir, "fake-bin")
	repo := filepath.Join(dir, "repo")
	for _, p := range []string{home, fakeBin, repo} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BATON_HOME", home)
	t.Setenv("BATON_AGENT", "")
	t.Setenv("BATON_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"agents":{"codex":{"model":"cfg-model"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_AUTHOR_NAME", "Baton Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "baton@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Baton Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "baton@example.invalid")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("FIXTURE_PATH", filepath.Join(root, "testdata", "fixtures", "stream", "codex.jsonl"))
	t.Setenv("BATON_BIN", bin)
	t.Setenv("SOURCE_REPO", repo)
	argvLog := filepath.Join(dir, "argv.log")
	t.Setenv("ARGV_LOG", argvLog)
	// Only this fake codex is executed; no provider or real agent is used.
	script := `#!/bin/sh
set -eu
if read -r input; then echo "stdin was not /dev/null" >&2; exit 9; fi
printf '%s\n' "$PWD" "$@" >> "$ARGV_LOG"
case "$*" in
  *HOLD*) echo "$$" > agent.pid; sleep 60 ;;
  *DELAY*) sleep 1 ;;
  *NESTED*) "$BATON_BIN" do child -repo "$SOURCE_REPO" > nested.out 2>&1 || true ;;
esac
case "$*" in *read-only*) ;; *) printf 'turn\n' >> result.txt ;; esac
cat "$FIXTURE_PATH"
printf 'raw stderr\n' >&2
`
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"commit", "--allow-empty", "-m", "base"}} {
		if _, err := workspace.Git(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := workspace.Git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	cli := func(dst any, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if dst != nil {
			args = append(args, "-json") // output is human-readable by default; flags may follow positionals
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("baton %v: %v\n%s\n%s", args, err, out, stderr.String())
		}
		if dst != nil {
			if err := json.Unmarshal(out, dst); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}
		}
	}
	var submitted app.Submitted
	cli(&submitted, "run", "-agent", "codex", "-repo", repo, "-task", "make a file", "-title", "创建结果文件", "-check", "test -f result.txt", "-timeout", "20s")
	if submitted.ID == "" || submitted.Status != "queued" {
		t.Fatal(submitted)
	}
	var task app.Task
	cli(&task, "wait", submitted.ID, "-timeout", "20s", "-json")
	if len(task.Turns) != 1 || task.Title != "创建结果文件" || task.Turns[0].Verify == nil || task.Turns[0].Verify.Status != "pass" || task.Status != "succeeded" || task.Verify.Status != "pass" || task.Verify.Checks[0].ExitCode != 0 {
		t.Fatalf("task: %+v", task)
	}
	var result app.Result
	cli(&result, "result", submitted.ID, "-json")
	if result.FinalText != "baton-fixture" || result.SessionID != "01a12aa4-3781-7181-a0c4-33d76e940f0a" || result.Branch != "baton/"+submitted.ID || result.BaseCommit != base || result.Diffstat == "" {
		t.Fatalf("result: %+v", result)
	}
	patch, err := os.ReadFile(result.PatchPath)
	if err != nil || !bytes.Contains(patch, []byte("result.txt")) {
		t.Fatalf("patch: %s %v", patch, err)
	}
	if msg, err := workspace.Git(result.Worktree, "log", "-1", "--format=%s"); err != nil || msg != "baton: "+submitted.ID+" turn 1 (codex)" {
		t.Fatalf("commit: %s %v", msg, err)
	}
	if out, err := os.ReadFile(filepath.Join(filepath.Dir(result.PatchPath), "turn-1.stdout")); err != nil {
		t.Fatal(err)
	} else if fixture, _ := os.ReadFile(os.Getenv("FIXTURE_PATH")); !bytes.Equal(out, fixture) {
		t.Fatal("stdout was altered")
	}
	if out, err := os.ReadFile(filepath.Join(filepath.Dir(result.PatchPath), "turn-1.stderr")); err != nil || string(out) != "raw stderr\n" {
		t.Fatalf("stderr: %q %v", out, err)
	}
	firstWT, firstBranch, session := result.Worktree, result.Branch, result.SessionID
	promptPath := filepath.Join(dir, "reply.txt")
	if err := os.WriteFile(promptPath, []byte("another turn"), 0600); err != nil {
		t.Fatal(err)
	}
	cli(&submitted, "reply", submitted.ID, "-msg", "@"+promptPath, "-timeout", "20s")
	cli(&task, "wait", submitted.ID, "-timeout", "20s", "-json")
	if len(task.Turns) != 2 || task.Title != "创建结果文件" || task.Turns[0].Verify == nil || task.Turns[0].Verify.Round != 1 || task.Turns[1].Verify == nil || task.Turns[1].Verify.Round != 2 || task.Status != "succeeded" || task.Worktree != firstWT || task.Branch != firstBranch || task.SessionID != session || task.Verify.Round != 2 {
		t.Fatalf("reply task: %+v", task)
	}
	if commits, err := workspace.Git(firstWT, "rev-list", "--count", base+"..HEAD"); err != nil || commits != "2" {
		t.Fatalf("commits: %s %v", commits, err)
	}
	if head, err := workspace.Git(repo, "rev-parse", "HEAD"); err != nil || head != base {
		t.Fatalf("source HEAD changed: %s %v", head, err)
	}
	if source, err := workspace.Inspect(repo); err != nil || source.Dirty || source.Branch != "main" {
		t.Fatalf("source changed: %+v %v", source, err)
	}
	log, err := os.ReadFile(argvLog)
	if err != nil || !bytes.Contains(log, []byte("resume\n")) || !bytes.Contains(log, []byte("sandbox_mode=\"workspace-write\"")) || !bytes.Contains(log, []byte("-m\ncfg-model\n")) {
		t.Fatalf("resume arguments: %q %v", log, err)
	}
	// Sub-agents cannot dispatch: the worker environment and task worktrees refuse it.
	if out, err := exec.Command(bin, "do", "from worktree", "-repo", firstWT).CombinedOutput(); err == nil || !strings.Contains(string(out), "task worktree") {
		t.Fatalf("dispatch from worktree: %s %v", out, err)
	}
	cli(&submitted, "do", "NESTED", "-repo", repo, "-no-check", "-timeout", "20s")
	cli(&task, "wait", submitted.ID, "-timeout", "20s")
	if out, err := os.ReadFile(filepath.Join(task.Worktree, "nested.out")); err != nil || !strings.Contains(string(out), "BATON_DEPTH=1") {
		t.Fatalf("nested dispatch: %s %v", out, err)
	}
	cli(&submitted, "run", "-agent", "codex", "-repo", repo, "-task", "fail check", "-check", "false", "-timeout", "20s")
	cli(&task, "wait", submitted.ID, "-timeout", "20s", "-json")
	if task.Status != "failed" || task.Verify.Status != "fail" || task.Verify.Checks[0].ExitCode != 1 {
		t.Fatalf("failed check: %+v", task)
	}
	// do: default agent, positional task text with flags after it, short id.
	cli(&submitted, "do", "no", "checks", "-repo", repo, "-timeout", "20s")
	cli(&task, "wait", submitted.ID[len(submitted.ID)-8:], "-timeout", "20s")
	if task.Status != "succeeded" || task.Verify.Status != "none" || task.Agent != "codex" || task.Model != "cfg-model" || task.Turns[0].Prompt != "no checks" {
		t.Fatalf("no checks: %+v", task)
	}
	cli(&submitted, "run", "-agent", "codex", "-repo", repo, "-task", "dirty check", "-check", "touch check-created.txt", "-timeout", "20s")
	cli(&task, "wait", submitted.ID, "-timeout", "20s", "-json")
	if task.Verify.Status != "pass" || len(task.Warnings) != 1 || !strings.Contains(task.Warnings[0], "检查修改了工作区") {
		t.Fatalf("dirty check: %+v", task)
	}
	if out, err := exec.Command(bin, "reply", submitted.ID, "-msg", "dirty reply").CombinedOutput(); err == nil || !strings.Contains(string(out), "dirty worktree") {
		t.Fatalf("dirty reply: %s %v", out, err)
	}
	cli(&submitted, "run", "-agent", "codex", "-repo", repo, "-task", "missing check", "-check", "/nonexistent-baton-test-command", "-timeout", "20s")
	cli(&task, "wait", submitted.ID, "-timeout", "20s", "-json")
	if task.Status != "failed" || task.Verify.Status != "unavailable" || task.Verify.Checks[0].ExitCode != 127 {
		t.Fatalf("missing check: %+v", task)
	}
	gate, err := guard.NewGate(home)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := gate.Acquire(context.Background(), "test holds the check gate", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	cli(&submitted, "run", "-agent", "codex", "-repo", repo, "-task", "blocked check", "-check", "test -f result.txt", "-timeout", "3s")
	cli(&task, "wait", submitted.ID, "-timeout", "10s", "-json")
	if task.Status != "failed" || task.Verify.Status != "unavailable" || len(task.Verify.Checks) != 1 || !strings.Contains(task.Verify.Checks[0].Reason, "wait for the machine") {
		t.Fatalf("cross-process gate: %+v", task)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	cli(&submitted, "run", "-agent", "codex", "-repo", repo, "-mode", "read", "-task", "read repo", "-timeout", "20s")
	cli(&task, "wait", submitted.ID, "-timeout", "20s", "-json")
	if task.Title != "read repo" || task.Status != "succeeded" || task.Worktree != "" || task.Branch != "" || task.Verify.Status != "none" {
		t.Fatalf("read task: %+v", task)
	}
	if source, err := workspace.Inspect(repo); err != nil || source.Dirty {
		t.Fatalf("read modified source: %+v %v", source, err)
	}
	// An active turn rejects reply; cancellation stops its process group.
	cli(&submitted, "run", "-agent", "codex", "-repo", repo, "-task", "HOLD", "-timeout", "20s")
	deadline := time.Now().Add(15 * time.Second)
	for {
		cli(&task, "status", submitted.ID, "-json")
		if _, err := os.Stat(filepath.Join(task.Worktree, "agent.pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not start: %+v", task)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if out, err := exec.Command(bin, "reply", submitted.ID, "-msg", "invalid").CombinedOutput(); err == nil {
		t.Fatalf("active reply accepted: %s", out)
	}
	cli(&task, "cancel", submitted.ID)
	cli(&task, "wait", submitted.ID, "-timeout", "10s", "-json")
	if task.Status != "canceled" {
		t.Fatalf("cancel: %+v", task)
	}
	if patch, err := os.ReadFile(task.PatchPath); err != nil || !bytes.Contains(patch, []byte("agent.pid")) {
		t.Fatalf("canceled turn patch: %s %v", patch, err)
	}
	// Confirm MCP registration and parity using the same built CLI and store.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "baton-test", Version: "1"}, nil)
	conn, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin, "mcp")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tools, err := conn.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("MCP tools: %+v %v", tools, err)
	}
	out, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: "baton_result", Arguments: map[string]any{"id": submitted.ID}})
	if err != nil || out.IsError {
		t.Fatalf("MCP result: %+v %v", out, err)
	}
	b, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var mcpResult app.Result
	if err := json.Unmarshal(b, &mcpResult); err != nil || mcpResult.ID != task.ID || mcpResult.Status != "canceled" {
		t.Fatalf("MCP JSON: %s %v", b, err)
	}
	// Hidden agents are left out of baton_agents and refused over MCP only.
	exposed := func() map[string]app.AgentInfo {
		t.Helper()
		out, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: "baton_agents", Arguments: map[string]any{}})
		if err != nil || out.IsError {
			t.Fatalf("MCP agents: %+v %v", out, err)
		}
		b, _ := json.Marshal(out.StructuredContent)
		var list app.AgentList
		if err := json.Unmarshal(b, &list); err != nil {
			t.Fatal(err)
		}
		byName := map[string]app.AgentInfo{}
		for _, a := range list.Agents {
			byName[a.Name] = a
		}
		return byName
	}
	if a := exposed()["codex"]; !a.Default || a.Model != "cfg-model" || a.Error != "" || len(exposed()) != 5 {
		t.Fatalf("exposed agents: %+v", exposed())
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"agents":{"codex":{"model":"cfg-model","hidden":true}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := exposed()["codex"]; ok {
		t.Fatal("hidden codex listed")
	}
	refused := func(tool string, args map[string]any) {
		t.Helper()
		out, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "hidden") {
			t.Fatalf("%s with hidden agent: %+v %v", tool, out, err)
		}
	}
	refused("baton_run", map[string]any{"repo": repo, "task": "default agent is hidden"})
	refused("baton_reply", map[string]any{"id": submitted.ID, "msg": "continue"})
	cli(&submitted, "ask", "codex", "hidden agents still work on the CLI", "-repo", repo, "-timeout", "20s")
	cli(&task, "wait", submitted.ID, "-timeout", "20s")
	if task.Status != "succeeded" {
		t.Fatalf("CLI with hidden agent: %+v", task)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"agents":{"codex":{"model":"cfg-model"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = conn.CallTool(ctx, &mcp.CallToolParams{Name: "baton_run", Arguments: map[string]any{"agent": "codex", "repo": repo, "task": "DELAY", "title": "MCP 对话标题", "timeout": "20s", "checks": []string{"test -f result.txt"}}})
	if err != nil || out.IsError {
		t.Fatalf("MCP run: %+v %v", out, err)
	}
	b, err = json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &submitted); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	cli(&task, "wait", submitted.ID, "-timeout", "20s", "-json")
	if task.Title != "MCP 对话标题" || task.Status != "succeeded" || task.Verify.Status != "pass" {
		t.Fatalf("worker stopped after MCP disconnected: %+v", task)
	}
}
