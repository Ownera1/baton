package app

import (
	"baton/internal/workspace"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cleanFixture is a repository with one commit and a Service whose add
// records a finished task; a write task gets a worktree with one commit per
// file.
func cleanFixture(t *testing.T) (*Service, string, func(string, ...string) string, func(int, string, string, string, ...string) string) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"} {
		t.Setenv(k, v)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := workspace.Git(dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	repo := t.TempDir()
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "base")
	repo = git(repo, "rev-parse", "--show-toplevel")
	info, err := workspace.Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Home: t.TempDir()}
	add := func(n int, mode, status, base string, files ...string) string {
		t.Helper()
		id := fmt.Sprintf("20261013-0000%02d-00000000aaaa00%02d", n, n)
		dir := filepath.Join(s.Home, "tasks", id)
		task := Task{ID: id, Title: "task " + fmt.Sprint(n), Agent: "codex", Mode: mode, Repo: repo, Status: status, BaseTask: base,
			Turns: []Turn{{Number: 1, Prompt: "p"}}}
		if status == "running" {
			task.WorkerPID = os.Getpid()
		}
		if mode == "write" {
			task.Worktree, task.Branch = filepath.Join(dir, "wt"), "baton/"+id
			if err := workspace.CreateWorktree(info, task.Worktree, task.Branch, info.Head); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(task.Worktree, f), []byte(f), 0o600); err != nil {
					t.Fatal(err)
				}
				git(task.Worktree, "add", f)
				git(task.Worktree, "commit", "-q", "-m", f)
			}
		} else if base != "" {
			task.Worktree = filepath.Join(dir, "wt")
			git(repo, "worktree", "add", "-q", "--detach", task.Worktree, info.Head)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, v := range map[string]any{"request.json": request{Task: task, Number: 1}, "task.json": task} {
			if err := writeJSON(filepath.Join(dir, name), v); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	return s, repo, git, add
}

func TestClean(t *testing.T) {
	s, repo, git, add := cleanFixture(t)
	ancestor := add(1, "write", "succeeded", "", "a.txt")
	squashed := add(2, "write", "succeeded", "", "c1.txt", "c2.txt")
	picked := add(3, "write", "failed", "", "p.txt")
	unmerged := add(4, "write", "failed", "", "b.txt")
	read := add(5, "read", "succeeded", "")
	base := add(6, "write", "succeeded", "")
	running := add(7, "read", "running", base)
	dirty := add(8, "write", "canceled", "")
	review := add(9, "read", "succeeded", ancestor)
	if err := os.WriteFile(filepath.Join(s.Home, "tasks", dirty, "wt", "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "merge", "-q", "--ff-only", "baton/"+ancestor)
	git(repo, "cherry-pick", "baton/"+picked)
	git(repo, "merge", "-q", "--squash", "baton/"+squashed)
	git(repo, "commit", "-q", "-m", "squash")

	plan, err := s.PlanClean(CleanRequest{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	keep := map[string]string{}
	for _, it := range plan.Tasks {
		keep[it.ID] = it.Keep
	}
	if _, ok := keep[running]; ok || len(keep) != 8 || keep[unmerged] == "" || !strings.Contains(keep[base], ShortID(running)) {
		t.Fatalf("plan:\n%s%+v", plan.Text(), keep)
	}
	s.Clean(plan)
	if want := []string{ancestor, squashed, picked, read, review}; !slices.Equal(sorted(plan.Removed), want) {
		t.Fatalf("removed %v, want %v\n%+v", plan.Removed, want, plan.Tasks)
	}
	for _, id := range plan.Removed {
		if _, err := os.Stat(filepath.Join(s.Home, "tasks", id)); !os.IsNotExist(err) {
			t.Errorf("%s still on disk: %v", id, err)
		}
	}
	branches := git(repo, "branch", "--format=%(refname:short)")
	if strings.Contains(git(repo, "worktree", "list"), review) {
		t.Error("a removed read task's worktree is still registered")
	}
	if strings.Contains(branches, ancestor) || !strings.Contains(branches, unmerged) || !strings.Contains(branches, dirty) {
		t.Errorf("branches:\n%s", branches)
	}
	if i := slices.IndexFunc(plan.Tasks, func(it CleanItem) bool { return it.ID == dirty }); plan.Tasks[i].Error == "" {
		t.Error("a worktree with an untracked file must stay")
	}

	if plan, err = s.PlanClean(CleanRequest{IDs: []string{unmerged, running}, Force: true}); err != nil {
		t.Fatal(err)
	}
	if s.Clean(plan); !slices.Equal(plan.Removed, []string{unmerged}) || !strings.Contains(git(repo, "worktree", "list"), base) {
		t.Fatalf("force: %v\n%s", plan.Removed, plan.Text())
	}
}

func sorted(s []string) []string { s = slices.Clone(s); slices.Sort(s); return s }

func TestUnreadableTask(t *testing.T) {
	s := &Service{Home: t.TempDir()}
	id := "20261013-000000-00000000bbbb0001"
	dir := filepath.Join(s.Home, "tasks", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := s.List("")
	if err != nil || len(list.Tasks) != 1 || !list.Tasks[0].Unreadable || list.Tasks[0].Status != "failed" || list.Tasks[0].CreatedAt.IsZero() {
		t.Fatalf("one unreadable task must not break list: %+v %v", list, err)
	}
	plan, err := s.PlanClean(CleanRequest{IDs: []string{id}})
	if err != nil || plan.Removable() != 0 {
		t.Fatalf("unreadable task removed without force: %+v %v", plan, err)
	}
	plan, err = s.PlanClean(CleanRequest{IDs: []string{id}, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	// A reply holding the task's lock keeps it.
	held, err := lock(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Clean(plan)
	if len(plan.Removed) != 0 || !strings.Contains(plan.Tasks[0].Error, "busy") {
		t.Fatalf("removed a locked task: %+v", plan)
	}
	held.Close()
	plan.Tasks[0].Error = ""
	s.Clean(plan)
	if _, err := os.Stat(dir); !os.IsNotExist(err) || len(plan.Removed) != 1 {
		t.Fatalf("forced clean must remove it: %+v %v", plan, err)
	}
}

// TestCleanApproved removes a task only as it was listed: one that gained a
// commit, a turn or a task continuing from it after the plan stays, and a
// base stays while a task continuing from it stays.
func TestCleanApproved(t *testing.T) {
	s, _, git, add := cleanFixture(t)
	wt := func(id string) string { return filepath.Join(s.Home, "tasks", id, "wt") }
	commit := func(id, file string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt(id), file), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		git(wt(id), "add", file)
		git(wt(id), "commit", "-q", "-m", file)
	}
	committed := add(1, "write", "succeeded", "")
	turned := add(2, "read", "succeeded", "")
	continued := add(3, "write", "succeeded", "")
	a := add(4, "write", "succeeded", "")
	b := add(5, "write", "succeeded", a)
	c := add(6, "write", "succeeded", b)
	d := add(7, "write", "succeeded", "")
	e := add(8, "write", "succeeded", d)
	plan, err := s.PlanClean(CleanRequest{IDs: []string{committed, turned, continued, a, b, c, d, e}})
	if err != nil || plan.Removable() != 8 {
		t.Fatalf("plan: %v\n%s", err, plan.Text())
	}
	// While the person reads the plan: a new commit, a new turn, a new task
	// continuing from one, and a worktree git will not remove.
	commit(committed, "new.txt")
	path := filepath.Join(s.Home, "tasks", turned, "task.json")
	var task Task
	if err := readJSON(path, &task); err != nil {
		t.Fatal(err)
	}
	task.Turns = append(task.Turns, Turn{Number: 2, Prompt: "again"})
	if err := writeJSON(path, task); err != nil {
		t.Fatal(err)
	}
	add(9, "read", "succeeded", continued)
	if err := os.WriteFile(filepath.Join(wt(c), "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	s.Clean(plan)
	if want := sorted([]string{d, e}); !slices.Equal(sorted(plan.Removed), want) {
		t.Fatalf("removed %v, want %v\n%+v", plan.Removed, want, plan.Tasks)
	}
	errs := map[string]string{}
	for _, it := range plan.Tasks {
		errs[it.ID] = it.Error
	}
	for id, want := range map[string]string{committed: "新提交", turned: "新的一轮", continued: "base", b: ShortID(c), a: ShortID(b)} {
		if !strings.Contains(errs[id], want) {
			t.Errorf("%s: error %q, want %q", ShortID(id), errs[id], want)
		}
	}
	for _, id := range []string{committed, a, b, c} {
		if _, err := os.Stat(wt(id)); err != nil {
			t.Errorf("%s lost its worktree: %v", ShortID(id), err)
		}
	}

	// force removes an unmerged branch as listed, not a commit made since.
	forced := add(10, "write", "failed", "", "f.txt")
	if plan, err = s.PlanClean(CleanRequest{IDs: []string{forced}, Force: true}); err != nil || plan.Removable() != 1 {
		t.Fatalf("force plan: %v\n%s", err, plan.Text())
	}
	commit(forced, "g.txt")
	if s.Clean(plan); len(plan.Removed) != 0 || !strings.Contains(plan.Tasks[0].Error, "新提交") {
		t.Fatalf("forced clean removed new work: %+v", plan)
	}
}
