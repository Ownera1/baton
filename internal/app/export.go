package app

import (
	"baton/internal/agent"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ExportRequest picks the tasks to export: ids, or every task in a group
// and/or repository; nothing picks the newest task.
type ExportRequest struct {
	IDs   []string `json:"ids,omitempty" jsonschema:"task ids, unique prefixes or suffixes"`
	Group string   `json:"group,omitempty" jsonschema:"every task in this group"`
	Repo  string   `json:"repo,omitempty" jsonschema:"every task of the git repository containing this directory"`
}

type ExportResult struct {
	Files []string `json:"files"`
}

// Export writes each task as Markdown to .baton/history/ in its repository,
// where agents working on it can read it. A new history directory ignores
// itself, so the repository stays clean for the next write task; delete its
// .gitignore to commit the history.
func (s *Service) Export(req ExportRequest) (*ExportResult, error) {
	tasks, err := s.exportTasks(req)
	if err != nil {
		return nil, err
	}
	out := &ExportResult{Files: []string{}}
	for _, t := range tasks {
		dir := filepath.Join(t.Repo, ".baton", "history")
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return out, err
			}
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o644); err != nil {
				return out, err
			}
		}
		path := filepath.Join(dir, exportName(t))
		if err := os.WriteFile(path, []byte(s.Markdown(t)), 0o644); err != nil {
			return out, err
		}
		out.Files = append(out.Files, path)
	}
	return out, nil
}

func (s *Service) exportTasks(req ExportRequest) ([]*Task, error) {
	if len(req.IDs) > 0 && (req.Group != "" || req.Repo != "") {
		return nil, fmt.Errorf("pick tasks by ids, or by group and repo, not both")
	}
	if req.Group == "" && req.Repo == "" {
		refs := req.IDs
		if len(refs) == 0 {
			refs = []string{""}
		}
		var tasks []*Task
		for _, ref := range refs {
			id, err := s.Resolve(ref)
			if err != nil {
				return nil, err
			}
			t, err := s.Status(id)
			if err != nil {
				return nil, err
			}
			tasks = append(tasks, t)
		}
		return tasks, nil
	}
	root := ""
	if req.Repo != "" {
		var err error
		if root, err = repoRoot(req.Repo); err != nil {
			return nil, err
		}
	}
	list, err := s.List(req.Group)
	if err != nil {
		return nil, err
	}
	var tasks []*Task
	for _, t := range list.Tasks {
		if root == "" || t.Repo == root {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks to export")
	}
	return tasks, nil
}

var unsafeName = regexp.MustCompile(`[\\/:*?"<>|\s…]+`)

// exportName is date-title-id.md: readable, and stable so a re-export
// replaces the file.
func exportName(t *Task) string {
	name := strings.Trim(unsafeName.ReplaceAllString(t.Name(), "-"), "-.")
	id := t.ID
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	return fmt.Sprintf("%s-%s-%s.md", t.CreatedAt.Local().Format("2006-01-02"), name, id)
}

// Markdown renders a task turn by turn: the instruction, the final reply with
// how many tools it called, and the checks.
func (s *Service) Markdown(t *Task) string {
	var b strings.Builder
	meta := []string{t.Who(), map[string]string{"read": "读", "write": "写"}[t.Mode], t.Status, filepath.Base(t.Repo)}
	if t.Branch != "" {
		meta = append(meta, t.Branch)
	}
	fmt.Fprintf(&b, "# %s\n\n%s\n", t.Name(), strings.Join(meta, " · "))
	for _, turn := range t.Turns {
		fmt.Fprintf(&b, "\n## 第 %d 轮", turn.Number)
		if turn.StartedAt != nil {
			b.WriteString(" · " + turn.StartedAt.Local().Format("2006-01-02 15:04"))
			if turn.EndedAt != nil {
				b.WriteString(" · " + turn.EndedAt.Sub(*turn.StartedAt).Round(time.Second).String())
			}
		}
		if turn.Tokens != nil {
			b.WriteString(" · " + turn.Tokens.String())
		}
		fmt.Fprintf(&b, "\n\n### 指令\n\n%s\n\n### 回复", strings.TrimSpace(turn.Prompt))
		stdout, _ := os.ReadFile(filepath.Join(s.Home, "tasks", t.ID, fmt.Sprintf("turn-%d.stdout", turn.Number)))
		tools := 0
		for _, e := range agent.Events(t.Agent, stdout) {
			if e.Kind == "tool" {
				tools++
			}
		}
		if tools > 0 {
			fmt.Fprintf(&b, "（%d 个工具调用）", tools)
		}
		reply := strings.TrimSpace(turn.FinalText)
		if reply == "" && turn.EndedAt == nil {
			reply = "（进行中）"
		} else if reply == "" {
			reply = "（没有回复）"
		}
		b.WriteString("\n\n" + reply + "\n")
		if turn.Error != "" {
			b.WriteString("\n> 错误：" + strings.ReplaceAll(turn.Error, "\n", " ") + "\n")
		}
		if turn.Verify != nil && len(turn.Verify.Checks) > 0 {
			b.WriteString("\n### 检查\n\n")
			for _, c := range turn.Verify.Checks {
				mark := map[string]string{"pass": "✓", "fail": "✗", "unavailable": "✗"}[c.Status]
				if mark == "" {
					mark = "◌"
				}
				fmt.Fprintf(&b, "- %s `%s`\n", mark, c.Command)
			}
		}
	}
	return b.String()
}
