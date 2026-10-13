package app

import (
	"baton/internal/agent"
	"baton/internal/guard"
	"baton/internal/verify"
	"baton/internal/workspace"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func (s *Service) Worker(id string) (retErr error) {
	dir, err := s.dir(id)
	if err != nil {
		return err
	}
	f, err := lock(dir, false)
	if err != nil {
		return err
	}
	defer f.Close()
	var req request
	if err := readJSON(filepath.Join(dir, "request.json"), &req); err != nil {
		return err
	}
	t := req.Task
	if err := readJSON(filepath.Join(dir, "task.json"), &t); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cancelPath := filepath.Join(dir, fmt.Sprintf("cancel-%d", req.Number))
	if req.Number <= len(t.Turns) {
		// A stale task can be canceled without executing its agent again.
		if _, err := os.Stat(cancelPath); err == nil && t.Status == "running" && !alive(t.WorkerPID) {
			t.Status = "canceled"
			t.UpdatedAt = time.Now().UTC()
			last := &t.Turns[len(t.Turns)-1]
			last.EndedAt = &t.UpdatedAt
			last.Error = "canceled after worker disappeared"
			return writeJSON(filepath.Join(dir, "task.json"), t)
		}
		return nil // Duplicate worker invocation must never repeat a turn.
	}
	if req.Number != len(t.Turns)+1 {
		return fmt.Errorf("invalid turn sequence")
	}
	slot, err := s.slot(req.Parallel, t.ID, cancelPath)
	if err != nil {
		return err
	}
	defer slot.Release()
	start := time.Now().UTC()
	t.Turns = append(t.Turns, Turn{Number: req.Number, Prompt: req.Prompt, StartedAt: &start, ExitCode: -1})
	t.Status, t.WorkerPID, t.UpdatedAt = "running", os.Getpid(), start
	t.Verify = verify.Result{Status: verify.StatusNone, Checks: []verify.Outcome{}}
	if len(t.Checks) > 0 {
		t.Verify.Status = verify.StatusUnavailable
		t.Verify.Notes = "checks have not run"
	}
	if err := writeJSON(filepath.Join(dir, "task.json"), t); err != nil {
		return err
	}
	turn := &t.Turns[len(t.Turns)-1]
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, req.Timeout)
	defer cancel()
	if _, err := os.Stat(cancelPath); err == nil {
		cancel()
	}
	watchDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(cancelPath); err == nil {
				cancel()
				return
			}
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer close(watchDone)
	defer func() {
		end := time.Now().UTC()
		turn.EndedAt = &end
		t.UpdatedAt = end
		if retErr != nil {
			if turn.Error == "" {
				turn.Error = retErr.Error()
			} else {
				turn.Error = errors.Join(errors.New(turn.Error), retErr).Error()
			}
		}
		t.Status = "succeeded"
		if turn.Error != "" || (t.Verify.Status != verify.StatusPass && t.Verify.Status != verify.StatusNone) {
			t.Status = "failed"
		}
		if _, err := os.Stat(cancelPath); err == nil || signalCtx.Err() != nil {
			t.Status = "canceled"
		}
		if err := writeJSON(filepath.Join(dir, "task.json"), t); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	runDir, hint := t.Repo, ""
	if t.BaseTask != "" {
		bt, err := s.Status(t.BaseTask)
		if err != nil {
			return err
		}
		if t.Mode == "read" {
			runDir = bt.Worktree
		}
		hint = fmt.Sprintf("这里接着任务 %s（分支 %s）的结果，它的改动可用 git diff %s 查看。\n\n", bt.ID, bt.Branch, bt.BaseCommit)
	}
	if t.Mode == "write" {
		runDir = t.Worktree
		if req.Number == 1 {
			info, err := workspace.Inspect(t.Repo)
			if err != nil {
				return err
			}
			if err := workspace.RequireClean(info); err != nil && t.BaseTask == "" {
				return err
			}
			if _, err := workspace.CreateWorktree(info, t.Worktree, t.Branch, t.BaseCommit); err != nil {
				return err
			}
		} else {
			if err := checkBranch(&t); err != nil {
				return err
			}
			info, err := workspace.Inspect(runDir)
			if err != nil {
				return err
			}
			if err := workspace.RequireClean(info); err != nil {
				return fmt.Errorf("cannot resume dirty worktree: %w", err)
			}
		}
	}
	prefix := "只读，不要修改文件。\n\n"
	if t.Mode == "write" {
		prefix = "你在隔离的 git worktree 里工作，不要 push，不要切换分支，也不用提交，Baton 在本轮结束时提交；结束时用几句话总结改了什么、怎么验证的。\n\n"
	}
	if req.Number == 1 {
		prefix += hint
		if t.Instructions != "" {
			prefix += t.Instructions + "\n\n---\n\n"
		}
	}
	lastFile := filepath.Join(dir, fmt.Sprintf("turn-%d.last", req.Number))
	cache := filepath.Join(s.Home, "cache")
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	argv, err := agent.Argv(t.Agent, agent.Options{Path: t.AgentPath, Mode: t.Mode, Dir: runDir, Prompt: prefix + req.Prompt,
		Model: t.Model, Effort: t.Effort, SessionID: t.SessionID, LastFile: lastFile, Cache: cache})
	if err != nil {
		return err
	}
	outPath := filepath.Join(dir, fmt.Sprintf("turn-%d.stdout", req.Number))
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	errout, err := os.Create(filepath.Join(dir, fmt.Sprintf("turn-%d.stderr", req.Number)))
	if err != nil {
		return err
	}
	defer errout.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	// A check waits for other tasks' checks as long as the turn may run; ctx
	// bounds the wait, so a slow check elsewhere never fails this one.
	limits := guard.Limits{CPUs: guard.DefaultCPUs, Nice: guard.DefaultNice, MemoryMB: guard.DefaultMemoryMB, GateWait: req.Timeout}
	stdin := io.Reader(devnull)
	if agent.PromptOnStdin(t.Agent) {
		stdin = strings.NewReader(prefix + req.Prompt)
	}
	cr := workspace.RunArgv(runDir, argv, stdin, os.Environ(), workspace.RunOptions{
		Context: ctx, Limits: limits, Stdout: out, Stderr: errout, Note: func(msg string) { fmt.Fprintln(os.Stderr, msg) },
	})
	turn.ExitCode = cr.ExitCode
	stdout, err := os.ReadFile(outPath)
	if err != nil {
		return err
	}
	last, _ := os.ReadFile(lastFile)
	parsed := agent.Parse(t.Agent, stdout, string(last), cr.ExitCode)
	turn.FinalText, turn.Usage, turn.CostUSD, turn.Error = parsed.FinalText, parsed.Usage, parsed.CostUSD, parsed.Error
	for _, w := range parsed.Warnings {
		t.Warnings = append(t.Warnings, fmt.Sprintf("turn %d: %s", req.Number, w))
	}
	if parsed.SessionID != "" {
		t.SessionID = parsed.SessionID
	}
	if cr.StartErr != "" {
		turn.Error = cr.StartErr
	}
	if cr.Stopped {
		turn.Error = fmt.Sprintf("agent stopped: canceled=%v timed_out=%v memory_exceeded=%v error=%s", cr.Canceled, cr.TimedOut, cr.MemoryExceeded, turn.Error)
	}
	if t.Mode == "write" {
		if err := s.capture(&t, req.Number); err != nil {
			return err
		}
	}
	gate, err := guard.NewGate(s.Home)
	if err != nil {
		return err
	}
	checks := make([]verify.Input, 0, len(t.Checks))
	for i, cmd := range t.Checks {
		checks = append(checks, verify.Input{ID: fmt.Sprint(i + 1), Command: cmd, Required: true})
	}
	snapshot := t.BaseCommit
	if t.Mode == "write" {
		snapshot, err = workspace.Git(runDir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
	}
	t.Verify, err = verify.Run(t.ID, req.Number, snapshot, runDir, filepath.Join(dir, "checks", fmt.Sprintf("turn-%d", req.Number)), checks,
		req.Timeout, verify.Options{Context: ctx, Limits: limits, Gate: gate})
	turn.Verify = &t.Verify
	if err != nil {
		return err
	}
	if t.Mode == "write" {
		if err := checkBranch(&t); err != nil {
			return err
		}
		info, err := workspace.Inspect(runDir)
		if err != nil {
			return err
		}
		if info.Dirty {
			t.Warnings = append(t.Warnings, fmt.Sprintf("turn %d: 检查修改了工作区：%v", req.Number, info.DirtyFiles))
		}
		if info.Head != snapshot {
			t.Warnings = append(t.Warnings, fmt.Sprintf("turn %d: 检查修改了工作区：HEAD 从 %s 变为 %s；patch 对应检查前的提交。", req.Number, snapshot, info.Head))
		}
	}
	return ctx.Err()
}

func checkBranch(t *Task) error {
	branch, err := workspace.Git(t.Worktree, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if branch != t.Branch || branch != "baton/"+t.ID {
		return fmt.Errorf("worktree branch changed: expected %s, got %s; refusing to commit", t.Branch, branch)
	}
	return nil
}

func (s *Service) capture(t *Task, number int) error {
	if err := checkBranch(t); err != nil {
		return err
	}
	info, err := workspace.Inspect(t.Worktree)
	if err != nil {
		return err
	}
	if info.Dirty {
		if _, err := workspace.Git(t.Worktree, "add", "-A"); err != nil {
			return err
		}
		if err := checkBranch(t); err != nil {
			return err
		}
		if _, err := workspace.Git(t.Worktree, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "-m", fmt.Sprintf("baton: %s turn %d (%s)", t.ID, number, t.Agent)); err != nil {
			return err
		}
	}
	patch, stderr, code := workspace.GitRun(t.Worktree, "diff", "--binary", t.BaseCommit, "HEAD")
	if code != 0 {
		return fmt.Errorf("git diff: %s", stderr)
	}
	if err := os.WriteFile(t.PatchPath, []byte(patch), 0600); err != nil {
		return err
	}
	t.Diffstat, err = workspace.Git(t.Worktree, "diff", "--stat", t.BaseCommit, "HEAD")
	return err
}

// slot holds one of n turn slots for the whole turn, so the task stays queued
// until one frees; n <= 0 is no limit. A cancel while waiting returns no slot
// and the turn then records the cancellation.
func (s *Service) slot(n int, id, cancelPath string) (*guard.Lease, error) {
	for n > 0 {
		for i := range n {
			gate, err := guard.NewSlotGate(s.Home, i)
			if err != nil {
				return nil, err
			}
			lease, err := gate.Acquire(context.Background(), id, 0)
			if !errors.Is(err, guard.ErrGateBusy) {
				return lease, err
			}
		}
		if _, err := os.Stat(cancelPath); err == nil {
			return nil, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, nil
}
