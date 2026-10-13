// Copied from collaborative-work (same author).
// Package workspace manages the isolated git worktree a task runs in. The
// worktree is an isolation boundary for code changes, not a security sandbox.
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Info describes the source repository at task start.
type Info struct {
	Root       string   `json:"root"`
	Head       string   `json:"head"`
	Branch     string   `json:"branch"`
	Dirty      bool     `json:"dirty"`
	DirtyFiles []string `json:"dirty_files"`
	HasCommits bool     `json:"has_commits"`
}

// ErrDirtyRepo is returned when the source repository has uncommitted changes.
// The task stops and the user chooses; nothing is stashed or committed for them.
type ErrDirtyRepo struct {
	Root  string
	Files []string
}

func (e *ErrDirtyRepo) Error() string {
	return fmt.Sprintf("repository %s has uncommitted changes in %d path(s): %s\n"+
		"commit or stash them yourself, then start the task again",
		e.Root, len(e.Files), strings.Join(e.Files, ", "))
}

// Inspect reads repository state.
func Inspect(repo string) (*Info, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	root, err := gitOut(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository: %w", abs, err)
	}
	info := &Info{Root: root}
	if out, err := gitOut(root, "rev-parse", "HEAD"); err == nil {
		info.Head = out
		info.HasCommits = true
	} else {
		info.HasCommits = false
	}
	if out, err := gitOut(root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		info.Branch = out
	}
	status, stderr, code := GitRun(root, "status", "--porcelain=v1", "--untracked-files=all")
	if code != 0 {
		return nil, fmt.Errorf("git status: %s", stderr)
	}
	for _, line := range strings.Split(status, "\n") {
		if len(line) > 3 {
			info.DirtyFiles = append(info.DirtyFiles, strings.TrimSpace(line[3:]))
		}
	}
	info.Dirty = len(info.DirtyFiles) > 0
	return info, nil
}

// RequireClean fails on a dirty repository.
func RequireClean(info *Info) error {
	if info.Dirty {
		return &ErrDirtyRepo{Root: info.Root, Files: info.DirtyFiles}
	}
	return nil
}

// CreateWorktree adds a worktree at path on a new branch based on base.
func CreateWorktree(info *Info, path, branch, base string) error {
	if base == "" {
		base = info.Head
	}
	if base == "" {
		return fmt.Errorf("repository %s has no commits to branch from", info.Root)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		entries, _ := os.ReadDir(path)
		if len(entries) > 0 {
			return fmt.Errorf("worktree path %s already exists and is not empty", path)
		}
	}
	_, err := gitOut(info.Root, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-b", branch, path, base)
	return err
}

// Git runs a git command in dir and returns trimmed stdout.
func Git(dir string, args ...string) (string, error) { return gitOut(dir, args...) }

// GitRun runs a git command and returns stdout, stderr and the exit code.
func GitRun(dir string, args ...string) (string, string, int) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		} else {
			code = -1
			errb.WriteString(err.Error())
		}
	}
	return out.String(), errb.String(), code
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// stopProcessGroup asks the group to stop, then insists. It returns true when
// the unconditional kill turned out to be necessary. done is the channel
// cmd.Wait reports on.
func stopProcessGroup(cmd *exec.Cmd, done <-chan error) bool {
	if cmd.Process == nil {
		return false
	}
	if err := signalProcessGroup(cmd, termSignal); err != nil {
		return false
	}
	select {
	case <-done:
		_ = signalProcessGroup(cmd, killSignal)
		return false
	case <-time.After(2 * time.Second):
	}
	_ = signalProcessGroup(cmd, killSignal)
	<-done
	return true
}
