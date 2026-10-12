package app

import (
	"baton/internal/agent"
	"baton/internal/verify"
	"baton/internal/workspace"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Service) Run(in RunRequest) (*Submitted, error) {
	if in.Mode == "" {
		in.Mode = "write"
	}
	if in.Mode != "read" && in.Mode != "write" {
		return nil, fmt.Errorf("mode must be read or write")
	}
	var err error
	if in.Repo == "" {
		if in.Repo, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	cfg, _, _, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	if in.Agent == "" {
		in.Agent = cfg.DefaultAgent()
	}
	ac := cfg.Agents[in.Agent]
	in.Model, in.Effort = cmp.Or(in.Model, ac.Model), cmp.Or(in.Effort, ac.Effort)
	prompt, err := Text(in.Task)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = TitleFromPrompt(prompt)
	}
	timeout, err := duration(in.Timeout, cfg.TurnTimeout())
	if err != nil {
		return nil, err
	}
	if _, err := agent.Argv(in.Agent, agent.Options{Mode: in.Mode, Path: ac.Path}); err != nil {
		return nil, err
	}
	info, err := workspace.Inspect(in.Repo)
	if err != nil {
		return nil, err
	}
	if err := s.nested(info.Root); err != nil {
		return nil, err
	}
	if in.Mode == "write" {
		if err := workspace.RequireClean(info); err != nil {
			return nil, err
		}
		if !info.HasCommits {
			return nil, fmt.Errorf("write mode requires a repository with a HEAD commit")
		}
		if in.Checks == nil {
			in.Checks = DetectChecks(info.Root)
		}
	}
	if in.NoChecks {
		in.Checks = nil
	}
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(entropy[:])
	dir, _ := s.dir(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	t := Task{ID: id, Title: title, Agent: in.Agent, Mode: in.Mode, Repo: info.Root, BaseCommit: info.Head,
		AgentPath: ac.Path, Model: in.Model, Effort: in.Effort, Checks: in.Checks, Status: "queued", Turns: []Turn{},
		Verify: verify.Result{Status: verify.StatusNone, Checks: []verify.Outcome{}}, CreatedAt: now, UpdatedAt: now}
	if t.Checks == nil {
		t.Checks = []string{}
	}
	if in.Mode == "write" {
		t.Worktree = filepath.Join(dir, "wt")
		t.Branch = "baton/" + id
		t.PatchPath = filepath.Join(dir, "patch.diff")
		if in.Agent == "pi" {
			t.Warnings = append(t.Warnings, "pi write 模式没有沙箱；worktree 仅隔离 Git 改动。")
		}
	}
	return s.submit(dir, request{Task: t, Prompt: prompt, Number: 1, Timeout: timeout})
}

func (s *Service) submit(dir string, req request) (*Submitted, error) {
	var previous request
	previousErr := readJSON(filepath.Join(dir, "request.json"), &previous)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return nil, previousErr
	}
	if err := writeJSON(filepath.Join(dir, "request.json"), req); err != nil {
		return nil, err
	}
	if err := s.spawnDetached(req.Task.ID, dir); err != nil {
		if previousErr == nil {
			_ = writeJSON(filepath.Join(dir, "request.json"), previous)
		} else {
			_ = os.RemoveAll(dir)
		}
		return nil, err
	}
	return &Submitted{ID: req.Task.ID, Status: "queued"}, nil
}

func (s *Service) Status(id string) (*Task, error) {
	dir, err := s.dir(id)
	if err != nil {
		return nil, err
	}
	var req request
	if err := readJSON(filepath.Join(dir, "request.json"), &req); err != nil {
		return nil, err
	}
	t := req.Task
	if err := readJSON(filepath.Join(dir, "task.json"), &t); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if req.Number > len(t.Turns) {
		t.Status = "queued"
		t.Turns = append(t.Turns, Turn{Number: req.Number, Prompt: req.Prompt, ExitCode: -1})
		if b, err := os.ReadFile(filepath.Join(dir, "worker.pid")); err == nil {
			t.WorkerPID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			if t.WorkerPID > 0 && !alive(t.WorkerPID) {
				t.Status = "stale"
			}
		}
	}
	if t.Status == "running" && !alive(t.WorkerPID) {
		t.Status = "stale"
	}
	return &t, nil
}

func (s *Service) Wait(ctx context.Context, id string, timeout time.Duration) (*Task, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("wait timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		t, err := s.Status(id)
		if err != nil {
			return nil, err
		}
		if t.Status != "queued" && t.Status != "running" {
			return t, nil
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) Result(id string) (*Result, error) {
	t, err := s.Status(id)
	if err != nil {
		return nil, err
	}
	r := &Result{Task: t}
	if len(t.Turns) > 0 {
		last := t.Turns[len(t.Turns)-1]
		r.FinalText, r.Usage, r.CostUSD, r.Error = last.FinalText, last.Usage, last.CostUSD, last.Error
	}
	return r, nil
}

func (s *Service) Reply(in ReplyRequest) (*Submitted, error) {
	if err := s.nested(""); err != nil {
		return nil, err
	}
	prompt, err := Text(in.Message)
	if err != nil {
		return nil, err
	}
	cfg, _, _, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	timeout, err := duration(in.Timeout, cfg.TurnTimeout())
	if err != nil {
		return nil, err
	}
	dir, err := s.dir(in.ID)
	if err != nil {
		return nil, err
	}
	f, err := lock(dir, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	t, err := s.Status(in.ID)
	if err != nil {
		return nil, err
	}
	if t.Status == "queued" || t.Status == "running" || t.Status == "stale" {
		return nil, fmt.Errorf("cannot reply to a %s task", t.Status)
	}
	if t.SessionID == "" {
		return nil, fmt.Errorf("task has no resumable session id")
	}
	if t.Mode == "write" {
		if err := checkBranch(t); err != nil {
			return nil, err
		}
		info, err := workspace.Inspect(t.Worktree)
		if err != nil {
			return nil, err
		}
		if err := workspace.RequireClean(info); err != nil {
			return nil, fmt.Errorf("cannot resume dirty worktree: %w", err)
		}
	}
	return s.submit(dir, request{Task: *t, Prompt: prompt, Number: len(t.Turns) + 1, Timeout: timeout})
}

func (s *Service) Cancel(id string) (*Task, error) {
	t, err := s.Status(id)
	if err != nil {
		return nil, err
	}
	if t.Status != "queued" && t.Status != "running" && t.Status != "stale" {
		return t, nil
	}
	dir, _ := s.dir(id)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("cancel-%d", len(t.Turns))), nil, 0600); err != nil {
		return nil, err
	}
	if t.Status == "stale" {
		// A new worker records the cancellation; CLI still never writes task.json.
		if err := s.spawnDetached(id, dir); err != nil {
			return nil, err
		}
	}
	return s.Status(id)
}

func (s *Service) List() (*ListResult, error) {
	r := &ListResult{Tasks: []*Task{}}
	entries, err := os.ReadDir(filepath.Join(s.Home, "tasks"))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := s.Status(e.Name())
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", e.Name(), err)
		}
		r.Tasks = append(r.Tasks, t)
	}
	sort.Slice(r.Tasks, func(i, j int) bool { return r.Tasks[i].CreatedAt.After(r.Tasks[j].CreatedAt) })
	return r, nil
}
