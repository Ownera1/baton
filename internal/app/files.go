package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Service struct{ Home string }

func New() (*Service, error) {
	home := os.Getenv("BATON_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".local", "share", "baton")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	return &Service{Home: abs}, nil
}

func (s *Service) dir(id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("invalid task id %q", id)
	}
	return filepath.Join(s.Home, "tasks", id), nil
}

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".json-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func lock(dir string, nonblocking bool) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	flags := syscall.LOCK_EX
	if nonblocking {
		flags |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, fmt.Errorf("task is busy: %w", err)
	}
	return f, nil // close releases the cross-process lock
}

// DepthEnv is set to 1 for every worker, so agents and checks inherit it.
const DepthEnv = "BATON_DEPTH"

// nested refuses dispatch from inside a task: a worker's environment, a
// worker among this process's ancestors (codex starts MCP servers without the
// parent environment), or a repository inside a task worktree.
// ponytail: depth fixed at one level; make it configurable if sub-agents should fan out.
func (s *Service) nested(repo string) error {
	if v := os.Getenv(DepthEnv); v != "" && v != "0" {
		return fmt.Errorf("refusing to dispatch from inside a Baton task (%s=%s): sub-agents cannot dispatch further tasks", DepthEnv, v)
	}
	if pid := workerAncestor(); pid > 0 {
		return fmt.Errorf("refusing to dispatch from inside a Baton task (worker pid %d started this process): sub-agents cannot dispatch further tasks", pid)
	}
	if repo == "" {
		return nil
	}
	tasks, err := filepath.EvalSymlinks(filepath.Join(s.Home, "tasks"))
	if err != nil {
		return nil // no tasks yet, so no task worktrees
	}
	if real, err := filepath.EvalSymlinks(repo); err == nil {
		repo = real
	}
	if rel, err := filepath.Rel(tasks, repo); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to dispatch from a Baton task worktree %s: reply to that task instead", repo)
	}
	return nil
}

// workerAncestor is the pid of a `baton _worker` that started this process, or 0.
// ponytail: one ps per ancestor; read /proc or sysctl directly if dispatch latency matters.
func workerAncestor() int {
	for pid, depth := os.Getppid(), 0; pid > 1 && depth < 64; depth++ {
		out, err := exec.Command("ps", "-ww", "-o", "ppid=", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0 // ps unavailable or the process is gone; the other checks still apply
		}
		ppid, command, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
		if strings.Contains(command+" ", " _worker ") {
			return pid
		}
		pid, _ = strconv.Atoi(ppid)
	}
	return 0
}

func alive(pid int) bool {
	// ponytail: kill(pid, 0) cannot detect PID reuse; a reused PID may look alive.
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func duration(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid positive timeout %q", value)
	}
	return d, nil
}

func Text(value string) (string, error) {
	if strings.HasPrefix(value, "@") {
		b, err := os.ReadFile(value[1:])
		if err != nil {
			return "", err
		}
		value = string(b)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("prompt is empty")
	}
	return value, nil
}
