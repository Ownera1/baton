package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baton/internal/guard"
)

func TestRunArgv(t *testing.T) {
	var out bytes.Buffer
	r := RunArgv(t.TempDir(), []string{"/bin/sh", "-c", "cat; exit 4"}, strings.NewReader("from stdin"), os.Environ(), RunOptions{Stdout: &out})
	if r.ExitCode != 4 || out.String() != "from stdin" || r.Stopped {
		t.Fatalf("%+v %q", r, out.String())
	}
}

func TestRunStopsTheWholeGroup(t *testing.T) {
	// The timeout reaches the background child too, or this would take 30 s.
	start := time.Now()
	r := RunCommand(t.TempDir(), "sleep 30 & sleep 30", RunOptions{Timeout: 300 * time.Millisecond})
	if !r.TimedOut || !r.Stopped || time.Since(start) > 10*time.Second {
		t.Fatalf("%+v after %s", r, time.Since(start))
	}
}

func TestRunMemoryCeiling(t *testing.T) {
	r := RunCommand(t.TempDir(), `perl -e '$x = "a" x 200_000_000; sleep 30'`,
		RunOptions{Timeout: 20 * time.Second, Limits: guard.Limits{MemoryMB: 50, SampleEvery: 50 * time.Millisecond}})
	if !r.MemoryExceeded || !r.Stopped || r.PeakRSSKB < 50*1024 {
		t.Fatalf("%+v", r)
	}
}

func TestInspectAndWorktree(t *testing.T) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"} {
		t.Setenv(k, v)
	}
	repo := t.TempDir()
	if _, err := Inspect(repo); err == nil {
		t.Fatal("not a repository")
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := Git(repo, args...); err != nil {
			t.Fatal(out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(repo)
	if err != nil || !info.HasCommits || info.Branch != "main" || !info.Dirty || RequireClean(info) == nil {
		t.Fatalf("%+v %v", info, err)
	}
	// A dirty source does not matter: the worktree starts from a commit.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := CreateWorktree(info, wt, "baton/x", ""); err != nil {
		t.Fatal(err)
	}
	if head, err := Git(wt, "rev-parse", "HEAD"); err != nil || head != info.Head {
		t.Fatalf("worktree head %s %v", head, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("uncommitted file leaked into the worktree")
	}
}
