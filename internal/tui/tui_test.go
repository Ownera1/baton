package tui

import (
	"baton/internal/agent"
	"baton/internal/app"
	"baton/internal/verify"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
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
		for _, text := range []string{"codex", "运行中", "cat a.txt", "fixture-model", "Read a.txt"} {
			if !strings.Contains(view, text) {
				t.Fatalf("%dx%d missing %q:\n%s", size.Width, size.Height, text, view)
			}
		}
		assertSize(t, view, size.Width, size.Height)
	}
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
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
	if m.task().ID != second.ID || cmd == nil || len(m.logs[logKey{second.ID, 1}].events) != 0 {
		t.Fatal("selection did not change and clear old events")
	}
	m, _ = update(m, cmd()) // missing stdout must become a visible error
	if !strings.Contains(m.View(), "读取 stdout 失败") {
		t.Fatal(m.View())
	}
	m, cmd = update(m, key("k"))
	m, _ = update(m, cmd())
	if m.task().ID != task.ID {
		t.Fatal("k did not select previous task")
	}
	m, _ = update(m, eventsMsg{id: second.ID, logs: map[logKey]logSnapshot{{second.ID, 1}: {events: []agent.Event{{Kind: "message", Text: "wrong selection", Category: "message"}}}}})
	if strings.Contains(m.View(), "wrong selection") {
		t.Fatal("stale stdout replaced selected events")
	}
	before := snapshot(t, dir)
	m, cmd = update(m, key("c"))
	if cmd != nil || !strings.Contains(m.View(), "y 确认") || !reflect.DeepEqual(before, snapshot(t, dir)) {
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
	if !strings.Contains(m.View(), "已请求取消") {
		t.Fatal(m.View())
	}
}

func TestDeleteTask(t *testing.T) {
	svc, task, dir := fixtureTask(t)
	m := refresh(newModel(svc))
	m, cmd := update(m, key("d"))
	m, _ = update(m, cmd())
	if m.cleanPlan != nil || !strings.Contains(m.View(), "保留") {
		t.Fatal("a running task must be kept:", m.View())
	}
	task.Status = "succeeded"
	writeTask(t, dir, task)
	m = refresh(m)
	m, cmd = update(m, key("d"))
	m, _ = update(m, cmd())
	if !strings.Contains(m.View(), "y 确认") {
		t.Fatal(m.View())
	}
	m, cmd = update(m, key("n"))
	if _, err := os.Stat(dir); cmd != nil || m.cleanPlan != nil || err != nil {
		t.Fatal("dismissal must not delete:", err)
	}
	m, cmd = update(m, key("d"))
	m, _ = update(m, cmd())
	m, cmd = update(m, key("y"))
	m, _ = update(m, cmd())
	if _, err := os.Stat(dir); !os.IsNotExist(err) || !strings.Contains(m.View(), "已删除") {
		t.Fatal("confirmed delete did not remove the task:", err)
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
			// The unreadable task is listed as failed with its error; the list still loads.
			if m.err != nil || !strings.Contains(m.View(), "cannot read task") {
				t.Fatal("bad JSON must show the task's error:", m.View())
			}
			initial := refresh(newModel(svc))
			if !strings.Contains(initial.View(), "cannot read task") {
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
			// Without request.json the directory is not a task yet; it must not break the list.
			m = refresh(m)
			if m.err != nil || !strings.Contains(m.View(), "暂无对话") {
				t.Fatal("a directory without request.json must be skipped:", m.View())
			}
		})
	}
	m := refresh(newModel(&app.Service{Home: t.TempDir()}))
	if m.err != nil || !strings.Contains(m.View(), "暂无对话") {
		t.Fatal("empty home:", m.View())
	}
}

func TestDetailsAndElapsed(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	cost := 0.012345
	end := task.Turns[0].StartedAt.Add(7 * time.Second)
	task.Turns[0].EndedAt, task.Turns[0].CostUSD = &end, &cost
	task.Status = "failed"
	task.Turns[0].FinalText, task.Turns[0].Error = "final reply", "turn error"
	task.Diffstat, task.Warnings = "a.txt | 1 +", []string{"fixture warning"}
	task.Verify = verify.Result{Status: "fail", Checks: []verify.Outcome{{Command: "go test ./...", Status: "fail", ExitCode: 2}}}
	m := newModel(svc)
	m.tasks = []*app.Task{task}
	m.syncViewport()
	for _, text := range []string{"final reply", "turn error", "✗ go test", "fixture warning", "$0.012345"} {
		if !strings.Contains(ansi.Strip(m.details()), text) {
			t.Fatalf("missing %q: %s", text, m.details())
		}
	}
	m.tab = 3
	if !strings.Contains(m.details(), "exit 2") {
		t.Fatal(m.details())
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

func TestViewThemes(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	for _, theme := range []string{"dark", "light"} {
		t.Run(theme, func(t *testing.T) {
			svc, task, _ := fixtureTask(t)
			task.Title, task.Status, task.Mode = "修复用户分页", "failed", "write"
			end := task.Turns[0].StartedAt.Add(time.Minute)
			task.Turns[0].EndedAt = &end
			task.Turns[0].FinalText = "## 已处理\n\n修复了 **分页**。"
			task.Checks = []string{"go test ./...", "go vet ./..."}
			task.Verify = verify.Result{Status: "fail", Checks: []verify.Outcome{{Command: task.Checks[0], Status: "fail", ExitCode: 1, OutputTail: "cursor_test.go:42: error: invalid cursor"}}}
			running, done := *task, *task
			running.ID, running.Title, running.Status = "running", "实现导出", "running"
			done.ID, done.Title, done.Status = "done", "梳理内存采样", "succeeded"
			m := newModel(svc, theme)
			m.width, m.height = 160, 32
			m.tasks = []*app.Task{task, &running, &done}
			m.patches[task.ID] = fileSnapshot{text: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"}
			m.syncViewport()
			view := m.View()
			plain := ansi.Strip(view)
			for _, want := range []string{"Baton", m.now.Format("15:04:05"), "● 1 运行", "✗ 1 失败", "✓ 1 完成", "需要关注  1", "进行中  1", "已完成  1", "修复用户分页", "失败", "✗ go test", "◌ go vet", "改动 +1 −1 · 1 个文件", "1 对话", "2 事件", "3 改动", "4 检查", "↑↓ 选择", "tab/点击 切焦点", "1-4 标签", "[ ] 比例", "z 专注", "e 展开", "c 取消", "x 导出", "s 设置", "q 退出", "列表 38% · 详情 62%", "cursor_test.go:42"} {
				if !strings.Contains(plain, want) {
					t.Errorf("missing %q:\n%s", want, plain)
				}
			}
			// Assert the actual theme colors as well as the shared text structure.
			bg, accent := "48;2;17;21;26", "38;2;143;184;255"
			if theme == "light" {
				bg, accent = "48;2;251;252;253", "38;2;43;103;209"
			}
			for _, color := range []string{bg, accent} {
				if !strings.Contains(view, color) {
					t.Errorf("missing theme color %s", color)
				}
			}
			assertSize(t, view, m.width, m.height)
			t.Log("\n" + plain)
		})
	}
}

func TestRatioFocusAndTabs(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	m := refresh(newModel(svc, "dark"))
	for _, tc := range []struct {
		key  string
		want int
	}{{"[", 33}, {"]", 38}, {"]", 43}, {"=", 38}} {
		m, _ = update(m, key(tc.key))
		if m.ratio != tc.want {
			t.Fatalf("%s: ratio=%d", tc.key, m.ratio)
		}
	}
	for i := 0; i < 30; i++ {
		m, _ = update(m, key("["))
	}
	if m.ratio != 20 {
		t.Fatal(m.ratio)
	}
	for i := 0; i < 30; i++ {
		m, _ = update(m, key("]"))
	}
	if m.ratio != 70 {
		t.Fatal(m.ratio)
	}
	m, _ = update(m, key("="))
	b, err := os.ReadFile(filepath.Join(svc.Home, "tui.json"))
	if err != nil || !strings.Contains(string(b), `"ratio":38`) {
		t.Fatalf("settings: %s %v", b, err)
	}
	m, _ = update(m, key("]"))
	if got := newModel(svc, "light").ratio; got != 43 {
		t.Fatal("ratio not restored:", got)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if !m.detail {
		t.Fatal("missing detail focus")
	}
	for i, k := range []string{"1", "2", "3", "4"} {
		m, _ = update(m, key(k))
		if m.tab != i || !strings.Contains(ansi.Strip(m.details()), tabs[i]) {
			t.Fatal(k)
		}
	}
	m, _ = update(m, key("1"))
	m, _ = update(m, key("e"))
	if !m.expand {
		t.Fatal("e did not expand")
	}
	m, _ = update(m, key("e"))
	if m.expand {
		t.Fatal("e did not fold")
	}
	m, _ = update(m, key("z"))
	lw, _, dw, _ := m.layout()
	if !m.zoom || lw != 0 || dw != m.width || strings.Contains(m.View(), "进行中") {
		t.Fatal("zoom did not hide list")
	}
	m, _ = update(m, key("z"))
	if m.zoom {
		t.Fatal("zoom did not restore")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.detail || m.task().ID != task.ID {
		t.Fatal("focus changed selection")
	}
	m, _ = update(m, key("c"))
	m, cmd := update(m, key("q"))
	if m.cancelID != "" || cmd != nil {
		t.Fatal("other key should dismiss confirmation")
	}
	for _, invalid := range []string{`{"ratio":99}`, `{"ratio":0}`, `{broken`} {
		if err := os.WriteFile(filepath.Join(svc.Home, "tui.json"), []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if got := newModel(svc).ratio; got != 38 {
			t.Fatal("invalid settings accepted:", got)
		}
	}
	// Unwritable settings are optional and must not interfere with key handling.
	m.svc = &app.Service{Home: filepath.Join(svc.Home, "tui.json", "child")}
	m, _ = update(m, key("]"))
	if m.ratio != 48 {
		t.Fatal(m.ratio)
	}
}

func TestMouseDividerAndNarrowLayout(t *testing.T) {
	m := newModel(&app.Service{Home: t.TempDir()}, "light")
	for _, size := range []tea.WindowSizeMsg{{Width: 140, Height: 40}, {Width: 80, Height: 40}} {
		m, _ = update(m, size)
		m.setRatio(38)
		lw, lh, _, _ := m.layout()
		x, y := lw, 5
		if m.width < 100 {
			x, y = 5, lh+1
		}
		m, _ = update(m, tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if !m.dragging {
			t.Fatal("divider press did not start drag")
		}
		if m.width >= 100 {
			x = (m.width - 1) / 2
		} else {
			y = 1 + (m.height-3)/2
		}
		m, _ = update(m, tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
		if m.ratio < 48 || m.ratio > 51 {
			t.Fatal("drag ratio:", m.ratio)
		}
		m, _ = update(m, tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease})
		if m.dragging || newModel(m.svc).ratio != m.ratio {
			t.Fatal("drag not saved")
		}
		lw, lh, dw, dh := m.layout()
		if m.width < 100 && (lw != m.width || dw != m.width || lh+dh+1 != m.height-2) {
			t.Fatal("narrow dimensions")
		}
		assertSize(t, m.View(), size.Width, size.Height)
	}
}

func TestFailureAlert(t *testing.T) {
	// From a real go test failure during this change; the location is more
	// useful than the surrounding FAIL lines and package summary.
	output := "--- FAIL: TestDetailsAndElapsed (0.01s)\n    tui_test.go:230: missing \"final reply\"\nFAIL\nFAIL\tbaton/internal/tui\t3.021s\nFAIL\n"
	if got := failureLine(output); got != `tui_test.go:230: missing "final reply"` {
		t.Fatal(got)
	}
	for _, tc := range []struct{ output, want string }{{"noise\npanic: bad input\nFAIL", "panic: bad input"}, {"noise\nerror: invalid cursor\nFAIL", "error: invalid cursor"}, {"noise\nFAIL pkg", "FAIL pkg"}, {"first\nlast\n", "last"}} {
		if got := failureLine(tc.output); got != tc.want {
			t.Fatal(got)
		}
	}
	task := &app.Task{Turns: []app.Turn{{Error: "agent crashed"}}, Verify: verify.Result{Checks: []verify.Outcome{{Command: "go test ./...", Status: "fail", OutputTail: output}, {Command: "second", Status: "fail", OutputTail: "ignore"}}}}
	if got := failureAlert(task); got != `go test ./... · tui_test.go:230: missing "final reply"` {
		t.Fatal(got)
	}
	task.Verify.Checks = nil
	if got := failureAlert(task); got != "agent crashed" {
		t.Fatal(got)
	}
}

func TestMarkdownCache(t *testing.T) {
	m := newModel(&app.Service{Home: t.TempDir()}, "dark")
	calls := 0
	m.render = func(text string, w int, theme string) (string, error) { calls++; return renderMarkdown(text, w, theme) }
	turn := app.Turn{Number: 1, FinalText: "## 回复\n\n- **已修复** `cursor`\n"}
	first := m.reply("task", turn, 60)
	if second := m.reply("task", turn, 60); first != second || calls != 1 {
		t.Fatal("cache miss", calls)
	}
	m.reply("other", turn, 60)
	m.reply("task", turn, 61)
	m.theme = "light"
	m.reply("task", turn, 61)
	turn.Number++
	m.reply("task", turn, 61)
	turn.FinalText += "new text"
	m.reply("task", turn, 61)
	if calls != 6 {
		t.Fatal("cache keys did not invalidate:", calls)
	}
	m.render = func(string, int, string) (string, error) { return "", os.ErrInvalid }
	turn.FinalText = "fallback **raw text**"
	if got := m.reply("fallback", turn, 60); got != turn.FinalText {
		t.Fatal(got)
	}
}

func TestStdoutCacheAndSpin(t *testing.T) {
	svc, task, dir := fixtureTask(t)
	m := refresh(newModel(svc, "dark"))
	k := logKey{task.ID, 1}
	first := m.logs[k]
	if len(first.events) == 0 {
		t.Fatal("no fixture events")
	}
	m = refresh(m)
	if &m.logs[k].events[0] != &first.events[0] {
		t.Fatal("unchanged stdout was parsed again")
	}
	path := filepath.Join(dir, "turn-1.stdout")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"new event\"}}\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(m)
	if len(m.logs[k].events) != len(first.events)+1 {
		t.Fatal("growth did not parse new event")
	}
	before := m.logs[k]
	// Remove the store: spin must redraw successfully without a service/file read.
	m.svc = &app.Service{Home: filepath.Join(svc.Home, "missing")}
	frame := m.frame
	m, cmd := update(m, spinMsg{})
	if cmd == nil || m.frame == frame || &m.logs[k].events[0] != &before.events[0] || m.err != nil {
		t.Fatal("spin performed refresh")
	}
}

func TestConversationEventsDiffAndChecks(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	m := newModel(svc, "light")
	task.Title, task.Status, task.Mode = "两轮任务", "succeeded", "write"
	end := task.Turns[0].StartedAt.Add(10 * time.Second)
	task.Turns[0].EndedAt, task.Turns[0].FinalText = &end, "第一轮回复"
	task.Turns[0].Verify = &verify.Result{Status: "pass", Checks: []verify.Outcome{{Command: "go test ./...", Status: "pass"}}}
	task.Turns = append(task.Turns, app.Turn{Number: 2, Prompt: "第二轮问题", StartedAt: &end, EndedAt: &end, FinalText: "第二轮回复"})
	m.tasks = []*app.Task{task}
	m.logs[logKey{task.ID, 1}] = logSnapshot{events: []agent.Event{{Kind: "tool", Category: "read", Text: "read old.go"}}}
	m.logs[logKey{task.ID, 2}] = logSnapshot{events: []agent.Event{{Kind: "tool", Category: "command", Text: "go test", Right: "exit 1 · 0.3s"}}}
	m.syncViewport()
	plain := ansi.Strip(m.details())
	for _, want := range []string{"你 · 第 1 轮", "你 · 第 2 轮", "codex · 第 1 轮 · 10s · 检查 1/1 通过", "第一轮回复", "第二轮回复", "▸ 1 个工具调用 · 点击展开"} {
		if !strings.Contains(plain, want) {
			t.Fatal("missing:", want, plain)
		}
	}
	if strings.Contains(plain, "old.go") {
		t.Fatal("tools should be folded")
	}
	clickFold := func(k foldKey) {
		for row, at := range m.folds {
			if at == k {
				m.click('d', row-m.viewport.YOffset, 0)
				m.syncViewport()
				return
			}
		}
		t.Fatal("no fold line for", k)
	}
	clickFold(foldKey{id: task.ID, turn: 1})
	if plain = ansi.Strip(m.details()); !strings.Contains(plain, "old.go") || strings.Contains(plain, "exit 1 · 0.3s") {
		t.Fatal("click should open turn 1 only:", plain)
	}
	clickFold(foldKey{id: task.ID, turn: 1})
	if strings.Contains(ansi.Strip(m.details()), "old.go") {
		t.Fatal("second click should fold turn 1")
	}
	m.expand = true
	if !strings.Contains(m.details(), "old.go") {
		t.Fatal("missing expanded history")
	}
	m.tab = 1
	plain = ansi.Strip(m.details())
	if strings.Contains(plain, "old.go") || !strings.Contains(plain, "exit 1 · 0.3s") {
		t.Fatal(plain)
	}
	m.tab = 2
	m.patches[task.ID] = fileSnapshot{text: "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-old\n+new\n" + strings.Repeat(" context\n", 2001)}
	if plain = ansi.Strip(m.details()); !strings.Contains(plain, "已截断") || !strings.Contains(plain, "+new") {
		t.Fatal("diff missing")
	}
	task.Mode = "read"
	if !strings.Contains(m.details(), "只读任务，没有改动") {
		t.Fatal(m.details())
	}
	m.tab = 3
	task.Checks = []string{"go test ./...", "go vet ./..."}
	task.Verify.Checks = []verify.Outcome{{Command: task.Checks[0], Status: "fail", DurationMS: 300, ExitCode: 1, OutputTail: "omit me\n" + strings.Repeat("failure detail\n", 12)}}
	plain = ansi.Strip(m.details())
	if strings.Contains(plain, "omit me") || strings.Count(plain, "failure detail") != 12 || !strings.Contains(plain, "◌ 等待中") || !strings.Contains(plain, "0.3s · exit 1") {
		t.Fatal(plain)
	}
}

func TestPendingTurnDoesNotReuseChecks(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	task.Status = "queued"
	task.Turns = append(task.Turns, app.Turn{Number: 2, Prompt: "new round"})
	task.Verify = verify.Result{Round: 1, Status: "pass", Checks: []verify.Outcome{{Status: "pass", Command: task.Checks[0]}}}
	m := newModel(svc, "dark")
	m.tasks = []*app.Task{task}
	m.tab = 3
	m.syncViewport()
	view := ansi.Strip(m.details())
	if strings.Contains(view, "✓ go test") || !strings.Contains(view, "◌ go test") || !strings.Contains(view, "◌ 等待中") {
		t.Fatal(view)
	}
}

func TestMarkdownFramesUseCache(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	first := task.Turns[0]
	end := first.StartedAt.Add(10 * time.Second)
	first.EndedAt, first.FinalText = &end, "## 已完成\n\n回复内容。"
	task.Turns = []app.Turn{first, {Number: 2, Prompt: "继续", StartedAt: &end}}
	m := newModel(svc, "dark")
	m.tasks = []*app.Task{task}
	calls := 0
	m.render = func(text string, w int, theme string) (string, error) { calls++; return renderMarkdown(text, w, theme) }
	m.syncViewport()
	rendered := calls // two prompts and one reply
	for i := 0; i < 10; i++ {
		m, _ = update(m, spinMsg{})
		_ = m.View()
	}
	if rendered != 3 || calls != rendered {
		t.Fatalf("markdown renders: %d initially, %d after 10 frames", rendered, calls)
	}
}

func TestPaintKeepsBackgroundAfterNestedReset(t *testing.T) {
	defer lipgloss.SetColorProfile(lipgloss.ColorProfile())
	lipgloss.SetColorProfile(termenv.TrueColor)
	bg := lipgloss.NewStyle().Background(lipgloss.Color("#e3ecfa"))
	on, _, _ := strings.Cut(bg.Render("x"), "x")
	got := paint("a"+lipgloss.NewStyle().Bold(true).Render("b")+"c", bg, 6)
	if !strings.Contains(got, "\x1b[0m"+on+"c") {
		t.Fatalf("background not re-applied after nested reset: %q", got)
	}
	if lipgloss.Width(got) != 6 {
		t.Fatalf("width %d, want 6", lipgloss.Width(got))
	}
}

func TestSpinnerStopsWhenNothingRuns(t *testing.T) {
	m := model{tasks: []*app.Task{{Status: "succeeded"}}, spinning: true}
	next, cmd := m.Update(spinMsg{})
	if cmd != nil || next.(model).spinning {
		t.Fatal("spinner must stop scheduling ticks when no task is running")
	}
}

func TestMarkdownElements(t *testing.T) {
	defer lipgloss.SetColorProfile(lipgloss.ColorProfile())
	lipgloss.SetColorProfile(termenv.TrueColor)
	md := "## 二级标题\n\n用 `go test` 跑。\n\n- 一\n- 二\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n"
	out, err := renderMarkdown(md, 60, "dark")
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"▌ 二级标题", "• 一", "│", " go", "    fmt.Println"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "## ") || strings.Contains(plain, "```") || strings.Contains(plain, "BATONCODE") {
		t.Errorf("raw markdown left in:\n%s", plain)
	}
	codeBG := "48;2;23;28;35" // dark --code #171c23
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(ansi.Strip(line), "fmt.Println") && !strings.Contains(line, codeBG) {
			t.Errorf("code line without block background: %q", line)
		}
	}
}

func TestLongPromptFolds(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	task.Turns[0].Prompt = strings.Repeat("一行\n\n", 30)
	m := newModel(svc, "dark")
	m.tasks = []*app.Task{task}
	if got, long := m.prompt(task.ID, task.Turns[0], 60); !long || !strings.Contains(got, "点击展开") {
		t.Fatalf("long prompt not folded:\n%s", got)
	}
	m.flip[foldKey{task.ID, 1, true}] = true
	if got, _ := m.prompt(task.ID, task.Turns[0], 60); !strings.Contains(got, "点击收起") {
		t.Fatal("clicked prompt still folded")
	}
	m.expand = true
	if got, _ := m.prompt(task.ID, task.Turns[0], 60); strings.Contains(got, "点击收起") {
		t.Fatal("click should undo e for that prompt")
	}
}

func TestSettingsMode(t *testing.T) {
	svc, _, _ := fixtureTask(t)
	dir := t.TempDir()
	t.Setenv("BATON_CONFIG_DIR", dir)
	t.Setenv("BATON_AGENT", "")
	path := filepath.Join(dir, "config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	press := func(m model, keys ...tea.KeyMsg) model {
		for _, k := range keys {
			m, _ = update(m, k)
		}
		return m
	}
	enter, right, ctrlS := tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyCtrlS}
	write(`{"agents":{"codex":{"model":"gpt-5.5","effort":"high"}}}`)
	// Down past agent, timeout, max_parallel, theme, memory_mb, cpus and codex model.
	m := press(refresh(newModel(svc, "dark")), key("s"), key("j"), key("j"), key("j"), key("j"), key("j"), key("j"), key("j"), right)
	if !m.settingsMode || m.set.draft.Agents["codex"].Effort != "xhigh" {
		t.Fatalf("codex effort did not cycle: %+v", m.set.draft)
	}
	view := ansi.Strip(m.View() + m.details())
	for _, want := range []string{" 设置", " 说明", "CODEX", "● 1 项设置未保存", "codex · effort", `model_reasoning_effort="xhigh"`, `+       "effort": "xhigh"`, "ctrl+s 保存"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	assertSize(t, m.View(), m.width, m.height)

	// Typed input, d for the default and u to undo, on codex model.
	m = press(m, key("k"), enter, tea.KeyMsg{Type: tea.KeyCtrlU}, key("o3"), enter)
	if m.set.editing || m.set.draft.Agents["codex"].Model != "o3" {
		t.Fatalf("typed model: %+v", m.set.draft)
	}
	if m = press(m, key("d")); m.set.draft.Agents["codex"].Model != "" {
		t.Fatal("d did not clear")
	}
	if m = press(m, key("u")); m.set.draft.Agents["codex"].Model != "gpt-5.5" {
		t.Fatal("u did not restore")
	}

	// An invalid path is flagged and blocks saving.
	m = press(m, key("j"), key("j"), enter)
	m = press(m, key("bin/codex"), enter, ctrlS)
	if !strings.Contains(ansi.Strip(m.details()), "must be absolute") || !strings.Contains(read(), `"high"`) || m.set.cursor != 8 {
		t.Fatalf("invalid path saved:\n%s\n%s", read(), ansi.Strip(m.details()))
	}
	m = press(m, key("d"), ctrlS)
	if got := read(); m.set.changed() != 0 || got != "{\n  \"agents\": {\n    \"codex\": {\n      \"model\": \"gpt-5.5\",\n      \"effort\": \"xhigh\"\n    }\n  }\n}\n" {
		t.Fatalf("save: %d %q", m.set.changed(), got)
	}

	// hidden toggles with ←/→ and is written as a bool.
	m = press(m, key("j"), right)
	view = ansi.Strip(m.View() + m.details())
	if !m.set.draft.Agents["codex"].Hidden || !strings.Contains(view, "隐藏（主 Agent 看不到）") || !strings.Contains(view, `+       "hidden": true`) {
		t.Fatalf("hidden toggle: %+v\n%s", m.set.draft, view)
	}
	if m = press(m, key("d"), key("k")); m.set.changed() != 0 || m.set.cursor != 8 {
		t.Fatalf("hidden reset: %+v", m.set.draft)
	}

	// An outside edit is never overwritten; r reloads it.
	write(`{"agent":"pi"}`)
	m = press(m, key("k"), right, ctrlS)
	if read() != `{"agent":"pi"}` || !strings.Contains(m.notice, "外部修改") {
		t.Fatalf("outside edit overwritten: %s %q", read(), m.notice)
	}
	if m = press(m, key("r")); m.set.draft.Agent != "pi" || m.set.changed() != 0 {
		t.Fatalf("reload: %+v", m.set.draft)
	}

	// Leaving keeps the draft; q asks before dropping it.
	m = press(m, key("k"), key("k"), key("k"), key("k"), key("k"), right, tea.KeyMsg{Type: tea.KeyEsc})
	if m.settingsMode || m.set.changed() != 1 || !strings.Contains(m.notice, "未保存") {
		t.Fatalf("esc: %v %d %q", m.settingsMode, m.set.changed(), m.notice)
	}
	m, cmd := update(m, key("q"))
	if cmd != nil || !m.settingsMode || !m.set.quit || !strings.Contains(ansi.Strip(m.View()), "再按 q 放弃并退出") {
		t.Fatal("q with unsaved settings quit at once")
	}
	if _, cmd = update(m, key("q")); cmd == nil {
		t.Fatal("second q did not quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("second q did not quit")
	}

	write(`{broken`)
	m = press(newModel(svc, "light"), key("s"))
	if m.set.err == nil || !strings.Contains(ansi.Strip(m.View()), "修好后按 r 重新读取") {
		t.Fatal("broken config not reported")
	}
	if m = press(m, enter, key("d")); m.set.editing || m.set.changed() != 0 {
		t.Fatal("broken config editable")
	}
}

func TestMouseFocusAndClicks(t *testing.T) {
	svc, task, _ := fixtureTask(t)
	t.Setenv("BATON_CONFIG_DIR", t.TempDir())
	second := *task
	second.ID = "20261011-110001-fedcba9876543210"
	second.CreatedAt = task.CreatedAt.Add(-time.Minute)
	writeTask(t, filepath.Join(svc.Home, "tasks", second.ID), &second)
	m := refresh(newModel(svc, "light"))
	press := func(x, y int, button tea.MouseButton) {
		m, _ = update(m, tea.MouseMsg{X: x, Y: y, Button: button, Action: tea.MouseActionPress})
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 140, Height: 40}, {Width: 80, Height: 40}} {
		m, _ = update(m, size)
		lw, lh, _, _ := m.layout()
		detailX, detailY := lw+5, 10 // a cell in the detail body
		bodyX, bodyY := lw+2, 2      // where the detail body's text starts
		if m.width < 100 {
			detailX, detailY, bodyX, bodyY = 5, lh+6, 1, lh+3
		}
		// List body: row 0 is the group header, rows 1-2 and 3-4 the two tasks.
		press(1, 2+3, tea.MouseButtonLeft)
		if m.selected != 1 || m.detail {
			t.Fatalf("%v: click did not select the second task: %d", size, m.selected)
		}
		press(1, 2+1, tea.MouseButtonLeft)
		if m.selected != 0 {
			t.Fatalf("%v: click did not select the first task", size)
		}
		press(1, 2+0, tea.MouseButtonLeft) // group header: no change
		press(detailX, detailY, tea.MouseButtonLeft)
		if !m.detail || m.selected != 0 {
			t.Fatalf("%v: detail click did not focus details", size)
		}
		press(bodyX+17, bodyY+m.tabRow(), tea.MouseButtonLeft) // "1 对话  2 事件  3 改动": column 17 is in 3 改动
		if m.tab != 2 {
			t.Fatalf("%v: tab click: %d", size, m.tab)
		}
		press(1, 4, tea.MouseButtonWheelDown)
		if m.selected != 1 || !m.detail {
			t.Fatalf("%v: wheel over list did not move the selection", size)
		}
		press(1, 4, tea.MouseButtonWheelUp)
		m.tab = 0
	}
	m, _ = update(m, tea.WindowSizeMsg{Width: 140, Height: 12})
	lw, _, _, _ := m.layout()
	m.detail = false
	press(lw+5, 6, tea.MouseButtonWheelDown)
	if m.viewport.YOffset == 0 || m.detail {
		t.Fatalf("wheel over details did not scroll them without focusing: %d", m.viewport.YOffset)
	}

	// Settings rows: 全局, agent, timeout, max_parallel, 面板, theme.
	m, _ = update(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m, _ = update(m, key("s"))
	press(1, 2+5, tea.MouseButtonLeft)
	if m.set.cursor != 3 {
		t.Fatalf("field click: %d", m.set.cursor)
	}
	press(1, 2+4, tea.MouseButtonLeft)
	if m.set.cursor != 3 {
		t.Fatal("group header click moved the cursor")
	}
}

func TestThemeSetting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BATON_CONFIG_DIR", dir)
	m := newModel(&app.Service{Home: t.TempDir()}, "dark")
	for _, k := range []tea.KeyMsg{key("s"), key("j"), key("j"), key("j")} {
		m, _ = update(m, k)
	}
	if m.theme != "dark" || !strings.Contains(ansi.Strip(m.View()), "theme") {
		t.Fatal("entering settings changed the -theme palette")
	}
	right := tea.KeyMsg{Type: tea.KeyRight}
	if m, _ = update(m, right); m.set.draft.Theme != "light" || m.theme != "light" || m.palette != themePalette("light") {
		t.Fatalf("no live preview: %q %q", m.set.draft.Theme, m.theme)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err != nil || !strings.Contains(string(b), `"theme": "light"`) {
		t.Fatalf("theme not saved: %s %v", b, err)
	}
	if m, _ = update(m, key("d")); m.theme != map[bool]string{true: "dark", false: "light"}[m.darkTerminal] {
		t.Fatal("default did not follow the terminal")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, key("blue"))
	if m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}); m.set.draft.Theme != "blue" || !strings.Contains(ansi.Strip(m.details()), "not auto, light or dark") {
		t.Fatal("invalid theme not flagged")
	}
}

func TestMaxParallelSetting(t *testing.T) {
	t.Setenv("BATON_CONFIG_DIR", t.TempDir())
	m := newModel(&app.Service{Home: t.TempDir()}, "dark")
	right, enter := tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyEnter}
	for _, k := range []tea.KeyMsg{key("s"), key("j"), key("j"), right, right} {
		m, _ = update(m, k)
	}
	if m.set.draft.MaxParallel != 2 || !strings.Contains(ansi.Strip(m.details()), `"max_parallel": 2`) {
		t.Fatalf("max_parallel: %+v", m.set.draft)
	}
	for _, k := range []tea.KeyMsg{enter, {Type: tea.KeyCtrlU}, key("two"), enter} {
		m, _ = update(m, k)
	}
	if !strings.Contains(ansi.Strip(m.details()), "max_parallel must be") {
		t.Fatal("typo not flagged")
	}
}

func TestRoleSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BATON_CONFIG_DIR", dir)
	t.Setenv("BATON_AGENT", "")
	t.Chdir(t.TempDir()) // no repository roles
	agents := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(agents, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(agents, name))
		return string(b)
	}
	write("drawer.md", "---\n# keep\ndescription: Draws\nagent: codex\nmode: read\n---\n\nRules.\n")
	write("broken.md", "no front matter")
	right, enter, ctrlS := tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyCtrlS}
	m, _ := update(newModel(&app.Service{Home: t.TempDir()}, "dark"), key("s"))
	at := func(role, k string) {
		t.Helper()
		m.set.role = role
		m.set.refields()
		if m.set.cursor = m.set.fieldAt(role, k); m.set.cursor < 0 {
			t.Fatalf("no field %s.%s", role, k)
		}
	}
	press := func(keys ...tea.KeyMsg) tea.Cmd {
		var cmd tea.Cmd
		for _, k := range keys {
			m, cmd = update(m, k)
		}
		return cmd
	}

	// A broken role only offers its file, with the parse error.
	at("broken", "file")
	if m.set.fieldAt("broken", "agent") >= 0 || !strings.Contains(ansi.Strip(m.View()+m.details()), "start with front matter") {
		t.Fatalf("broken role:\n%s", ansi.Strip(m.View()+m.details()))
	}

	// agent cycles, effort offers the new agent's levels, mode clears.
	at("drawer", "agent")
	press(right)
	at("drawer", "effort")
	press(right)
	at("drawer", "mode")
	press(right, right)
	r := m.set.draft.Roles["drawer"]
	view := ansi.Strip(m.View() + m.details())
	if r.Agent != "claude" || r.Effort != "low" || r.Mode != "" || m.set.changed() != 3 {
		t.Fatalf("draft: %+v %d", r, m.set.changed())
	}
	for _, want := range []string{"drawer  2/2 · 全局", "默认（读写都可）", "+ agent: claude", "+ effort: low", "--effort low", "Rules."} {
		if want == "Rules." {
			at("drawer", "file")
			view = ansi.Strip(m.View() + m.details())
		}
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	if cmd := press(enter); cmd != nil || !strings.Contains(m.notice, "先 ctrl+s") {
		t.Fatal("editor opened over unsaved changes")
	}
	// Switching roles keeps the draft; the overview marks it.
	at("drawer", "name")
	press(right)
	if m.set.role != "broken" || m.set.changed() != 3 || !strings.Contains(ansi.Strip(m.details()), "● drawer") {
		t.Fatalf("switch: %q %d\n%s", m.set.role, m.set.changed(), ansi.Strip(m.details()))
	}
	press(ctrlS)
	if got := read("drawer.md"); m.set.changed() != 0 || got != "---\n# keep\ndescription: Draws\nagent: claude\neffort: low\n---\n\nRules.\n" {
		t.Fatalf("save: %d %q %q", m.set.changed(), got, m.notice)
	}

	// The editor runs on a saved role; its edits are read back.
	at("drawer", "file")
	if cmd := press(enter); cmd == nil {
		t.Fatal("editor not opened")
	}
	write("drawer.md", "---\ndescription: Draws more\n---\nNew rules.\n")
	m, _ = update(m, editedMsg{name: "drawer"})
	if r := m.set.draft.Roles["drawer"]; r.Description != "Draws more" || r.Prompt != "New rules." || m.set.saved.Roles["drawer"].Description != "Draws more" || m.set.fields[m.set.cursor].role != "drawer" {
		t.Fatalf("reload: %+v", r)
	}

	// The name row searches roles; a name no role has creates one, which
	// saves once described, and d on its file drops it.
	at("drawer", "name")
	press(enter, key("dra"))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "> drawer  Draws more") || !strings.Contains(view, "dra  新建全局角色") {
		t.Fatalf("search:\n%s", view)
	}
	press(tea.KeyMsg{Type: tea.KeyEsc})
	at("broken", "name")
	press(enter, key("dra"), enter)
	if m.set.editing || m.set.role != "drawer" {
		t.Fatalf("search did not open drawer: %q", m.set.role)
	}
	press(enter, key("Bad"), enter)
	if !m.set.editing || !strings.Contains(m.notice, "lowercase") {
		t.Fatal("bad name accepted")
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlU}, key("tester"), enter, ctrlS)
	if m.set.editing || m.set.role != "tester" || m.set.changed() != 1 || !strings.Contains(m.notice, "有误") || read("tester.md") != "" {
		t.Fatalf("new role saved without description: %q", m.notice)
	}
	press(enter, key("Tests"), enter, ctrlS)
	if got := read("tester.md"); got != "---\ndescription: Tests\n---\n" || m.set.changed() != 0 {
		t.Fatalf("new role: %q %q", got, m.notice)
	}
	at("tester", "name")
	press(enter, key("scratch"), enter)
	at("scratch", "file")
	if press(key("d")); m.set.changed() != 0 || m.set.draft.Roles["scratch"].Name != "" || m.set.role != "broken" {
		t.Fatal("d did not drop the new role")
	}
	if got := rank([]string{"preview", "code-reviewer", "reviewer", "drawer"}, "Rev"); !slices.Equal(got, []string{"reviewer", "code-reviewer", "preview"}) {
		t.Fatalf("rank: %v", got)
	}
	assertSize(t, m.View(), m.width, m.height)
}
