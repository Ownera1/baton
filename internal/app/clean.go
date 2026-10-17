package app

import (
	"baton/internal/workspace"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CleanRequest picks finished tasks to remove: ids, or every finished task of
// the repository containing Repo, or of every repository when both are empty.
type CleanRequest struct {
	IDs   []string `json:"ids,omitempty" jsonschema:"task ids, unique prefixes or suffixes"`
	Repo  string   `json:"repo,omitempty" jsonschema:"every finished task of the git repository containing this directory; defaults to the server's directory when ids are empty"`
	Force bool     `json:"force,omitempty" jsonschema:"also remove write tasks whose branch is not merged into the repository's current branch"`
}

// CleanItem is one task clean looked at; Keep says why it stays.
type CleanItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Repo   string `json:"repo"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
	Branch string `json:"branch,omitempty"`
	Merged bool   `json:"merged,omitempty"`
	Bytes  int64  `json:"bytes"`
	Keep   string `json:"keep,omitempty"`
	Error  string `json:"error,omitempty"` // why removing it failed
	// The task as listed: Clean removes it only if it is still the same.
	Head  string `json:"head,omitempty"` // branch commit of a write task
	Turns int    `json:"turns"`
}

type CleanPlan struct {
	Tasks   []CleanItem `json:"tasks"`
	Removed []string    `json:"removed"`
	Force   bool        `json:"force,omitempty"`
}

func active(status string) bool {
	return status == "queued" || status == "running" || status == "stale"
}

// PlanClean lists what Clean would remove and what it keeps. A task goes whole
// (worktree, branch and record) or not at all; a write task goes only when its
// branch is merged, or with Force.
func (s *Service) PlanClean(req CleanRequest) (*CleanPlan, error) {
	list, err := s.List("")
	if err != nil {
		return nil, err
	}
	picked := map[string]bool{}
	for _, ref := range req.IDs {
		id, err := s.Resolve(ref)
		if err != nil {
			return nil, err
		}
		picked[id] = true
	}
	root := ""
	if len(picked) == 0 && req.Repo != "" {
		if root, err = repoRoot(req.Repo); err != nil {
			return nil, err
		}
	}
	plan := &CleanPlan{Tasks: []CleanItem{}, Removed: []string{}, Force: req.Force}
	index := map[string]int{}
	for _, t := range list.Tasks {
		if len(picked) > 0 && !picked[t.ID] || len(picked) == 0 && (root != "" && t.Repo != root || active(t.Status)) {
			continue
		}
		it := CleanItem{ID: t.ID, Title: t.Name(), Repo: t.Repo, Mode: t.Mode, Status: t.Status, Branch: t.Branch,
			Bytes: dirSize(filepath.Join(s.Home, "tasks", t.ID)), Turns: len(t.Turns)}
		switch _, err := os.Stat(t.Repo); {
		case active(t.Status):
			it.Keep = "还没结束，先等它结束或 baton cancel"
		case t.Unreadable:
			if !req.Force {
				it.Keep = "任务记录读不出来，force 才删除（只删记录，分支要自己删）"
			}
		case t.Mode != "write":
		case err != nil:
			if !req.Force {
				it.Keep = "仓库已不存在，force 才删除"
			}
		default:
			it.Head = branchHead(t)
			if it.Merged = merged(t); !it.Merged && !req.Force {
				it.Keep = "分支未合并到仓库当前分支，force 才删除"
			}
		}
		index[t.ID] = len(plan.Tasks)
		plan.Tasks = append(plan.Tasks, it)
	}
	// A task that stays still needs its base: its worker looks the base up
	// every turn, and a read task recorded before reads got their own
	// worktree runs in the base's.
	for changed := true; changed; {
		changed = false
		for _, t := range list.Tasks {
			b, ok := index[t.BaseTask]
			if !ok || plan.Tasks[b].Keep != "" {
				continue
			}
			if i, ok := index[t.ID]; !ok || plan.Tasks[i].Keep != "" {
				plan.Tasks[b].Keep = "任务 " + ShortID(t.ID) + " 以它为 base"
				changed = true
			}
		}
	}
	return plan, nil
}

// merged reports whether the task's branch adds nothing to the repository's
// current branch: it is an ancestor, git cherry finds each of its commits
// there (rebased or cherry-picked), or a squashed copy of it is there (squash
// merge). The squash test follows git-delete-merged-branches; no code is
// taken from it. A missing branch has nothing to lose.
func merged(t *Task) bool {
	ref := "refs/heads/" + t.Branch
	if _, err := workspace.Git(t.Repo, "rev-parse", "--verify", "-q", ref); err != nil {
		return true
	}
	if _, _, code := workspace.GitRun(t.Repo, "merge-base", "--is-ancestor", ref, "HEAD"); code == 0 {
		return true
	}
	if inHead(t.Repo, ref) {
		return true
	}
	base, err := workspace.Git(t.Repo, "merge-base", "HEAD", ref)
	if err != nil {
		return false
	}
	// commit-tree only writes an unreferenced object, which git gc drops.
	squash, err := workspace.Git(t.Repo, "-c", "user.name=baton", "-c", "user.email=baton@localhost",
		"commit-tree", ref+"^{tree}", "-p", base, "-m", "baton clean: squashed "+t.Branch)
	return err == nil && inHead(t.Repo, squash)
}

// branchHead is the commit of the task's branch, empty when it has none.
func branchHead(t *Task) string {
	head, err := workspace.Git(t.Repo, "rev-parse", "--verify", "-q", "refs/heads/"+t.Branch)
	if err != nil {
		return ""
	}
	return head
}

// inHead reports whether git cherry finds every commit of rev in HEAD.
func inHead(repo, rev string) bool {
	out, err := workspace.Git(repo, "cherry", "HEAD", rev)
	return err == nil && !strings.Contains("\n"+out, "\n+")
}

// errBase marks a task that a task still on disk continues from.
var errBase = errors.New("另一个任务以它为 base")

// Clean removes the plan's tasks that have no reason to stay. A task that
// fails, such as a worktree git will not remove because of changes, records
// the error in the plan and the rest go on. A base waits for the tasks that
// continue from it and stays when one of them stays.
func (s *Service) Clean(plan *CleanPlan) {
	var pending []int
	for i, it := range plan.Tasks {
		if it.Keep == "" {
			pending = append(pending, i)
		}
	}
	for len(pending) > 0 {
		var later []int
		for _, i := range pending {
			it := &plan.Tasks[i]
			err := s.remove(it, plan.Force)
			if it.Error = ""; err != nil {
				it.Error = err.Error()
				if errors.Is(err, errBase) {
					later = append(later, i)
				}
				continue
			}
			plan.Removed = append(plan.Removed, it.ID)
		}
		if len(later) == len(pending) {
			return
		}
		pending = later
	}
}

// remove deletes one planned task, checking under its lock that it is still
// the task the person approved: finished, with the same turns and branch
// commit, merged unless forced, and no task left that continues from it.
func (s *Service) remove(it *CleanItem, force bool) error {
	// Reply holds this lock while it queues a turn, and Run while it records a
	// task continuing from this one, so neither lands between these checks
	// and the removal.
	f, err := lock(filepath.Join(s.Home, "tasks", it.ID), true)
	if err != nil {
		return err
	}
	defer f.Close()
	t, err := s.Status(it.ID)
	if err != nil {
		// Only an unreadable task gets here, forced; its worktree lives in its directory.
		return os.RemoveAll(filepath.Join(s.Home, "tasks", it.ID))
	}
	if active(t.Status) {
		return fmt.Errorf("task is %s again", t.Status)
	}
	if len(t.Turns) != it.Turns {
		return fmt.Errorf("列出后又跑了新的一轮，没有删除；重新运行 clean 确认")
	}
	list, err := s.List("")
	if err != nil {
		return err
	}
	for _, o := range list.Tasks {
		if o.BaseTask == t.ID {
			return fmt.Errorf("%w：任务 %s", errBase, ShortID(o.ID))
		}
	}
	if _, err := os.Stat(t.Repo); t.Worktree != "" && err == nil {
		if t.Mode == "write" {
			if t.Branch != "baton/"+t.ID {
				return fmt.Errorf("refusing to delete branch %q of task %s", t.Branch, t.ID)
			}
			if branchHead(t) != it.Head {
				return fmt.Errorf("分支 %s 在列出后有新提交，没有删除；重新运行 clean 确认", t.Branch)
			}
			if !force && !merged(t) {
				return fmt.Errorf("分支 %s 未合并到仓库当前分支，没有删除", t.Branch)
			}
		}
		if _, err := os.Stat(t.Worktree); err == nil {
			// Without --force git refuses a worktree with changes or untracked files.
			if _, err := workspace.Git(t.Repo, "worktree", "remove", t.Worktree); err != nil {
				return err
			}
		} else if _, err := workspace.Git(t.Repo, "worktree", "prune"); err != nil {
			return err
		}
		if t.Mode == "write" { // a read task's worktree is detached, with no branch
			if _, err := workspace.Git(t.Repo, "rev-parse", "--verify", "-q", "refs/heads/"+t.Branch); err == nil {
				if _, err := workspace.Git(t.Repo, "branch", "-D", t.Branch); err != nil {
					return err
				}
			}
		}
	}
	return os.RemoveAll(filepath.Join(s.Home, "tasks", t.ID))
}

func dirSize(dir string) (n int64) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Removable counts the tasks Clean would remove.
func (p *CleanPlan) Removable() int { return len(p.RemovableIDs()) }

func (p *CleanPlan) RemovableIDs() []string {
	var ids []string
	for _, it := range p.Tasks {
		if it.Keep == "" {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// Text lists the plan for a person to approve.
func (p *CleanPlan) Text() string {
	var remove, keep []string
	var size int64
	for _, it := range p.Tasks {
		line := fmt.Sprintf("  %s %s · %s · %s · %s", ShortID(it.ID), it.Title, ModeName(it.Mode), it.Status, megabytes(it.Bytes))
		if it.Mode == "write" && it.Keep == "" {
			line += map[bool]string{true: " · 已合并", false: " · 未合并"}[it.Merged]
		}
		if it.Keep != "" {
			keep = append(keep, line+" — "+it.Keep)
			continue
		}
		remove = append(remove, line)
		size += it.Bytes
	}
	if len(remove) == 0 && len(keep) == 0 {
		return "没有可清理的任务\n"
	}
	var b strings.Builder
	if len(remove) > 0 {
		fmt.Fprintf(&b, "删除 %d 个任务（%s），连同各自的 worktree、baton/<id> 分支和记录：\n%s\n", len(remove), megabytes(size), strings.Join(remove, "\n"))
	}
	if len(keep) > 0 {
		fmt.Fprintf(&b, "保留 %d 个：\n%s\n", len(keep), strings.Join(keep, "\n"))
	}
	return b.String()
}

func megabytes(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }
