package tui

import (
	"baton/internal/agent"
	"baton/internal/app"
	"baton/internal/verify"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func fixtureTask(t *testing.T) (*app.Service, *app.Task, string) {
	t.Helper()
	t.Setenv("BATON_HOME", t.TempDir())
	svc, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-5 * time.Second)
	task := &app.Task{ID: "20261011-110000-0123456789abcdef", Agent: "codex", Mode: "read", Status: "running",
		Repo: "/repo/baton", Branch: "baton/fixture", Model: "fixture-model", Effort: "high", SessionID: "fixture-session",
		WorkerPID: os.Getpid(), CreatedAt: start, Checks: []string{"go test ./..."},
		Turns:  []app.Turn{{Number: 1, Prompt: "Read a.txt", StartedAt: &start, ExitCode: -1}},
		Verify: verify.Result{Status: "unavailable"}}
	dir := filepath.Join(svc.Home, "tasks", task.ID)
	writeTask(t, dir, task)
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "stream", "codex.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turn-1.stdout"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return svc, task, dir
}

func writeTask(t *testing.T, dir string, task *app.Task) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	request := struct {
		Task   *app.Task `json:"task"`
		Prompt string    `json:"prompt"`
		Number int       `json:"number"`
	}{task, task.Turns[len(task.Turns)-1].Prompt, len(task.Turns)}
	for name, value := range map[string]any{"request.json": request, "task.json": task} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func update(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

func refresh(m model) model {
	m, cmd := update(m, m.refresh()())
	if cmd != nil {
		m, _ = update(m, cmd())
	}
	return m
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestViewAndReadOnlyRefresh(t *testing.T) {
	svc, _, dir := fixtureTask(t)
	before := snapshot(t, dir)
	m := refresh(newModel(svc))
	for _, size := range []tea.WindowSizeMsg{{Width: 140, Height: 32}, {Width: 80, Height: 38}} {
		m, _ = update(m, size)
		view := m.View()
		for _, text := range []string{"codex", "running", "cat a.txt", "fixture-model", "fixture-session", "Read a.txt"} {
			if !strings.Contains(view, text) {
				t.Fatalf("%dx%d missing %q:\n%s", size.Width, size.Height, text, view)
			}
		}
		assertSize(t, view, size.Width, size.Height)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if !m.detail {
		t.Fatal("tab did not focus details")
	}
	m, _ = update(m, key("j"))
	if m.viewport.YOffset == 0 {
		t.Fatal("details did not scroll")
	}
	if after := snapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("view/refresh wrote task files")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 18}, {Width: 10, Height: 5}, {Width: 1, Height: 1}} {
		m, _ = update(m, size)
		assertSize(t, m.View(), size.Width, size.Height)
	}
	for _, msg := range []tea.KeyMsg{key("q"), {Type: tea.KeyCtrlC}} {
		_, cmd := update(m, msg)
		if cmd == nil {
			t.Fatal("missing quit command")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("did not quit")
		}
	}
}

func TestSelectionRefreshAndCancel(t *testing.T) {
	svc, task, dir := fixtureTask(t)
	m := refresh(newModel(svc))
	second := *task
	second.ID = "20261011-110001-fedcba9876543210"
	second.CreatedAt = task.CreatedAt.Add(-time.Minute)
	secondDir := filepath.Join(svc.Home, "tasks", second.ID)
	writeTask(t, secondDir, &second)
	m = refresh(m)
	m, cmd := update(m, key("j"))
	if m.task().ID != second.ID || cmd == nil || len(m.events) != 0 {
		t.Fatal("selection did not change and clear old events")
	}
	m, _ = update(m, cmd()) // missing stdout must become a visible error
	if !strings.Contains(m.View(), "Error reading stdout") {
		t.Fatal(m.View())
	}
	m, cmd = update(m, key("k"))
	m, _ = update(m, cmd())
	if m.task().ID != task.ID {
		t.Fatal("k did not select previous task")
	}
	m, _ = update(m, eventsMsg{id: second.ID, turn: 1, events: []agent.Event{{Kind: "message", Text: "wrong selection"}}})
	if strings.Contains(m.View(), "wrong selection") {
		t.Fatal("stale stdout replaced selected events")
	}
	before := snapshot(t, dir)
	m, cmd = update(m, key("c"))
	if cmd != nil || !strings.Contains(m.View(), "y confirm") || !reflect.DeepEqual(before, snapshot(t, dir)) {
		t.Fatal("c must only ask for confirmation")
	}
	m, cmd = update(m, key("n"))
	if cmd != nil || m.cancelID != "" || !reflect.DeepEqual(before, snapshot(t, dir)) {
		t.Fatal("dismissal must not cancel")
	}
	m, _ = update(m, key("c"))
	// A new task/reordering while the confirmation is open must keep its ID.
	second.CreatedAt = task.CreatedAt.Add(time.Minute)
	writeTask(t, secondDir, &second)
	m = refresh(m)
	if m.task().ID != task.ID || m.selected != 1 {
		t.Fatal("refresh lost selection")
	}
	m, cmd = update(m, key("y"))
	if cmd == nil {
		t.Fatal("missing cancel command")
	}
	m, _ = update(m, cmd())
	if _, err := os.Stat(filepath.Join(dir, "cancel-1")); err != nil {
		t.Fatal("confirmed cancel did not call svc.Cancel:", err)
	}
	if _, err := os.Stat(filepath.Join(secondDir, "cancel-1")); !os.IsNotExist(err) {
		t.Fatal("canceled wrong task:", err)
	}
	if !strings.Contains(m.View(), "Cancellation requested") {
		t.Fatal(m.View())
	}
}

func TestRefreshFileErrorsAndRecovery(t *testing.T) {
	for _, file := range []string{"request.json", "task.json"} {
		t.Run(file, func(t *testing.T) {
			svc, task, dir := fixtureTask(t)
			m := refresh(newModel(svc))
			if err := os.WriteFile(filepath.Join(dir, file), []byte("{bad json"), 0600); err != nil {
				t.Fatal(err)
			}
			m = refresh(m)
			if !strings.Contains(m.View(), "Error:") || !strings.Contains(m.View(), "codex") {
				t.Fatal("bad JSON must show an error and retain the previous list:", m.View())
			}
			initial := refresh(newModel(svc))
			if !strings.Contains(initial.View(), "Error:") {
				t.Fatal("initial bad JSON must show an error:", initial.View())
			}
			writeTask(t, dir, task)
			m = refresh(m)
			if m.err != nil {
				t.Fatal("did not recover:", m.err)
			}
			if err := os.Remove(filepath.Join(dir, "request.json")); err != nil {
				t.Fatal(err)
			}
			m = refresh(m)
			if !strings.Contains(m.View(), "Error:") {
				t.Fatal("missing file must show an error:", m.View())
			}
		})
	}
	m := refresh(newModel(&app.Service{Home: t.TempDir()}))
	if m.err != nil || !strings.Contains(m.View(), "No tasks") {
		t.Fatal("empty home:", m.View())
	}
}

func TestDetailsAndElapsed(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	cost := 0.012345
	end := task.Turns[0].StartedAt.Add(7 * time.Second)
	task.Turns[0].EndedAt, task.Turns[0].CostUSD = &end, &cost
	task.Turns[0].FinalText, task.Turns[0].Error = "final reply", "turn error"
	task.Diffstat, task.Warnings = "a.txt | 1 +", []string{"fixture warning"}
	task.Verify = verify.Result{Status: "fail", Checks: []verify.Outcome{{Command: "go test ./...", Status: "fail", ExitCode: 2}}}
	m := newModel(svc)
	m.tasks = []*app.Task{task}
	for _, text := range []string{"final reply", "turn error", "a.txt | 1 +", "fail (exit 2)", "fixture warning", "$0.012345"} {
		if !strings.Contains(m.details(), text) {
			t.Fatalf("missing %q: %s", text, m.details())
		}
	}
	if got := elapsed(task, end.Add(time.Hour)); got != "7s" {
		t.Fatal("completed elapsed:", got)
	}
	task.Turns[0].EndedAt = nil
	if got := elapsed(task, task.Turns[0].StartedAt.Add(12*time.Second)); got != "12s" {
		t.Fatal("running elapsed:", got)
	}
	m, cmd := update(m, tickMsg(end))
	if !m.now.Equal(end) || cmd == nil {
		t.Fatal("tick did not update time and schedule refresh")
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	return files
}

func assertSize(t *testing.T, view string, width, height int) {
	t.Helper()
	if lipgloss.Width(view) > width || lipgloss.Height(view) > height {
		t.Fatalf("view exceeds %dx%d: %dx%d\n%s", width, height, lipgloss.Width(view), lipgloss.Height(view), view)
	}
}
