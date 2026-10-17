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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Service) Run(in RunRequest) (*Submitted, error) {
	var err error
	var base *Task
	if in.Base != "" {
		if base, err = s.baseTask(in.Base); err != nil {
			return nil, err
		}
		in.Repo = cmp.Or(in.Repo, base.Repo)
	}
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
	var role *Role
	if !slices.Contains(Agents, in.Agent) {
		if role, err = FindRole(in.Repo, in.Agent); err != nil {
			return nil, err
		}
		if in.FromMCP && role.Hidden {
			return nil, hidden(role.Name)
		}
		if role.Mode != "" && in.Mode != "" && in.Mode != role.Mode {
			return nil, fmt.Errorf("role %s only runs in %s mode", role.Name, role.Mode)
		}
		in.Agent, in.Mode = cmp.Or(role.Agent, cfg.DefaultAgent()), cmp.Or(in.Mode, role.Mode)
		in.Model, in.Effort = cmp.Or(in.Model, role.Model), cmp.Or(in.Effort, role.Effort)
	}
	if in.Mode == "" {
		in.Mode = "write"
	}
	if in.Mode != "read" && in.Mode != "write" {
		return nil, fmt.Errorf("mode must be read or write")
	}
	if role != nil {
		if err := role.unsandboxed(in.Agent, in.Mode); err != nil {
			return nil, err
		}
	}
	ac := cfg.Agents[in.Agent]
	if in.FromMCP && ac.Hidden && role == nil {
		return nil, hidden(in.Agent)
	}
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
	head := info.Head
	if base != nil {
		if info.Root != base.Repo {
			return nil, fmt.Errorf("base task %s belongs to %s, not %s", base.ID, base.Repo, info.Root)
		}
		// The new worktree starts from a commit, so the source checkout may be dirty.
		if head, err = workspace.Git(info.Root, "rev-parse", "--verify", "refs/heads/"+base.Branch); err != nil {
			return nil, fmt.Errorf("base task %s: %w", base.ID, err)
		}
		if in.Checks == nil && in.Mode == "write" {
			in.Checks = base.Checks
		}
	} else if in.Mode == "write" {
		if err := workspace.RequireClean(info); err != nil {
			return nil, err
		}
		if !info.HasCommits {
			return nil, fmt.Errorf("write mode requires a repository with a HEAD commit")
		}
	}
	if in.Mode == "write" && in.Checks == nil {
		in.Checks = DetectChecks(info.Root)
	}
	if in.NoChecks {
		in.Checks = nil
	}
	// A check is a shell command outside the agent's read-only sandbox, run
	// where the read task runs: the source checkout or the base's worktree.
	if in.Mode == "read" && len(in.Checks) > 0 {
		return nil, fmt.Errorf("read tasks run no checks: a check could change the repository; drop -check / checks or use write mode")
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
	t := Task{ID: id, Title: title, Agent: in.Agent, Mode: in.Mode, Repo: info.Root, BaseCommit: head,
		AgentPath: ac.Path, Model: in.Model, Effort: in.Effort, Checks: in.Checks, Status: "queued", Turns: []Turn{},
		Verify: verify.Result{Status: verify.StatusNone, Checks: []verify.Outcome{}}, CreatedAt: now, UpdatedAt: now}
	if t.Checks == nil {
		t.Checks = []string{}
	}
	if role != nil {
		t.Role, t.Instructions = role.Name, role.Prompt
	}
	t.Group = strings.TrimSpace(in.Group)
	if base != nil {
		t.BaseTask = base.ID
		t.Group = cmp.Or(t.Group, base.Group)
	}
	if in.Mode == "write" {
		t.Worktree = filepath.Join(dir, "wt")
		t.Branch = "baton/" + id
		t.PatchPath = filepath.Join(dir, "patch.diff")
		if in.Agent == "pi" {
			t.Warnings = append(t.Warnings, "pi write 模式没有沙箱；worktree 仅隔离 Git 改动。")
		}
	}
	return s.submit(dir, request{Task: t, Prompt: prompt, Number: 1, Timeout: timeout, Parallel: cfg.MaxParallel, MemoryMB: cfg.MemoryMB, CPUs: cfg.CPUs})
}

func (s *Service) submit(dir string, req request) (*Submitted, error) {
	var previous request
	previousErr := readJSON(filepath.Join(dir, "request.json"), &previous)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return nil, previousErr
	}
	if err := writeJSON(filepath.Join(dir, "request.json"), req); err != nil {
		if previousErr != nil {
			_ = os.RemoveAll(dir) // a new task that never got its request
		}
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

func (s *Service) Status(id string) (*Task, error) { return s.status(id, false) }

func (s *Service) status(id string, cache bool) (*Task, error) {
	dir, err := s.dir(id)
	if err != nil {
		return nil, err
	}
	req, t, err := s.load(dir, cache)
	if err != nil {
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

// load reads a task's request and record. With cache it skips files whose
// stat is unchanged since the last call: the panel lists every task each
// second. Every write is a rename, so a changed file is a different file.
func (s *Service) load(dir string, cache bool) (request, Task, error) {
	reqPath, taskPath := filepath.Join(dir, "request.json"), filepath.Join(dir, "task.json")
	reqInfo, err := os.Stat(reqPath)
	if err != nil {
		return request{}, Task{}, err
	}
	taskInfo, _ := os.Stat(taskPath)
	same := func(a, b os.FileInfo) bool {
		return a == nil && b == nil || a != nil && b != nil && os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
	}
	if cache {
		s.mu.Lock()
		c, ok := s.loaded[dir]
		s.mu.Unlock()
		if ok && same(c.reqInfo, reqInfo) && same(c.taskInfo, taskInfo) {
			t := c.task
			t.Turns = slices.Clone(t.Turns) // status appends a queued turn
			return c.req, t, nil
		}
	}
	var req request
	if err := readJSON(reqPath, &req); err != nil {
		return req, Task{}, err
	}
	t := req.Task
	if err := readJSON(taskPath, &t); err != nil && !errors.Is(err, os.ErrNotExist) {
		return req, t, err
	}
	t.countTokens()
	if cache {
		s.mu.Lock()
		if s.loaded == nil {
			s.loaded = map[string]loaded{}
		}
		s.loaded[dir] = loaded{reqInfo: reqInfo, taskInfo: taskInfo, req: req, task: t}
		t.Turns = slices.Clone(t.Turns)
		s.mu.Unlock()
	}
	return req, t, nil
}

// baseTask is a finished write task that another task may continue from.
func (s *Service) baseTask(ref string) (*Task, error) {
	id, err := s.Resolve(ref)
	if err != nil {
		return nil, err
	}
	t, err := s.Status(id)
	if err != nil {
		return nil, err
	}
	if t.Mode != "write" {
		return nil, fmt.Errorf("base task %s is read-only and has no branch", id)
	}
	if t.Status == "queued" || t.Status == "running" || t.Status == "stale" {
		return nil, fmt.Errorf("base task %s is %s; wait for it first", id, t.Status)
	}
	return t, nil
}

func (s *Service) Wait(ctx context.Context, id string, timeout time.Duration) (*Task, error) {
	tasks, err := s.WaitMany(ctx, []string{id}, false, timeout)
	if len(tasks) == 0 {
		return nil, err
	}
	return tasks[0], err
}

// WaitMany polls until every task, or with first any one of them, has left
// queued and running, or until timeout; it returns every task's state.
func (s *Service) WaitMany(ctx context.Context, ids []string, first bool, timeout time.Duration) ([]*Task, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("wait timeout must be positive")
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no tasks to wait for")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		tasks, done := make([]*Task, len(ids)), 0
		for i, id := range ids {
			t, err := s.Status(id)
			if err != nil {
				return nil, err
			}
			tasks[i] = t
			if t.Status != "queued" && t.Status != "running" {
				done++
			}
		}
		if done == len(ids) || (first && done > 0) {
			return tasks, nil
		}
		select {
		case <-ctx.Done():
			return tasks, ctx.Err()
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
	if in.FromMCP {
		hide := cfg.Agents[t.Agent].Hidden
		if t.Role != "" {
			r, err := FindRole(t.Repo, t.Role)
			hide = err == nil && r.Hidden // a removed role keeps its conversations
		}
		if hide {
			return nil, hidden(cmp.Or(t.Role, t.Agent))
		}
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
	return s.submit(dir, request{Task: *t, Prompt: prompt, Number: len(t.Turns) + 1, Timeout: timeout, Parallel: cfg.MaxParallel, MemoryMB: cfg.MemoryMB, CPUs: cfg.CPUs})
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

// List returns every task, or only those in group, newest first.
func (s *Service) List(group string) (*ListResult, error) {
	ids, err := s.ids()
	if err != nil {
		return nil, err
	}
	r := &ListResult{Tasks: []*Task{}}
	for _, id := range ids {
		t, err := s.status(id, true)
		if err != nil {
			// One unreadable record must not hide the others; clean -force removes it.
			created, _ := time.Parse("20060102-150405", id[:min(len(id), 15)])
			t = &Task{ID: id, Status: "failed", Unreadable: true, CreatedAt: created,
				Turns: []Turn{{Number: 1, ExitCode: -1, Error: "cannot read task: " + err.Error()}}}
		}
		if group == "" || t.Group == group {
			r.Tasks = append(r.Tasks, t)
		}
	}
	sort.Slice(r.Tasks, func(i, j int) bool { return r.Tasks[i].CreatedAt.After(r.Tasks[j].CreatedAt) })
	s.mu.Lock()
	for dir := range s.loaded { // forget removed tasks
		if !slices.Contains(ids, filepath.Base(dir)) {
			delete(s.loaded, dir)
		}
	}
	s.mu.Unlock()
	return r, nil
}

// ids are the task directories holding a request.json, oldest first (ids
// start with a UTC timestamp). A directory still being created, or left by a
// dispatch that failed, has none and is skipped instead of failing every list.
func (s *Service) ids() ([]string, error) {
	tasks := filepath.Join(s.Home, "tasks")
	entries, err := os.ReadDir(tasks)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(tasks, e.Name(), "request.json")); e.IsDir() && err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}
