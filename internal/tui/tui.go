package tui

import (
	"baton/internal/agent"
	"baton/internal/app"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func Run(svc *app.Service, theme string) error {
	if theme != "auto" && theme != "light" && theme != "dark" {
		return fmt.Errorf("theme must be auto, light or dark")
	}
	_, err := tea.NewProgram(newModel(svc, theme), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

type model struct {
	svc           *app.Service
	tasks         []*app.Task
	selected      int
	width, height int
	detail        bool
	zoom, expand  bool
	dragging      bool
	spinning      bool // a spinner tick is scheduled; only while a task runs
	ratio, tab    int
	theme         string
	palette       palette
	viewport      viewport.Model
	logs          map[logKey]logSnapshot
	patches       map[string]fileSnapshot
	markdown      map[markdownKey]string
	render        func(string, int, string) (string, error)
	err           error
	now           time.Time
	frame         int
	cancelID      string
	cancelTitle   string
	notice        string
	noticeUntil   time.Time
}

type logKey struct {
	id   string
	turn int
}
type fileSnapshot struct {
	size  int64
	stamp time.Time
	text  string
	err   error
}
type logSnapshot struct {
	size   int64
	events []agent.Event
	err    error
}
type markdownKey struct {
	kind, id, theme     string
	turn, width, length int
}
type tickMsg time.Time
type spinMsg struct{}
type listMsg struct {
	list *app.ListResult
	err  error
}
type eventsMsg struct {
	id    string
	logs  map[logKey]logSnapshot
	patch fileSnapshot
}
type cancelMsg struct {
	id  string
	err error
}

func newModel(svc *app.Service, themes ...string) model {
	theme := "auto"
	if len(themes) > 0 {
		theme = themes[0]
	}
	if theme == "auto" {
		theme = "light"
		if lipgloss.HasDarkBackground() {
			theme = "dark"
		}
	}
	m := model{svc: svc, width: 120, height: 30, now: time.Now(), ratio: 38,
		theme: theme, palette: themePalette(theme), viewport: viewport.New(1, 1),
		logs: map[logKey]logSnapshot{}, patches: map[string]fileSnapshot{}, markdown: map[markdownKey]string{}, render: renderMarkdown}
	var settings struct {
		Ratio int `json:"ratio"`
	}
	if b, err := os.ReadFile(filepath.Join(svc.Home, "tui.json")); err == nil && json.Unmarshal(b, &settings) == nil && settings.Ratio >= 20 && settings.Ratio <= 70 {
		m.ratio = settings.Ratio
	}
	m.syncViewport()
	return m
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func spin() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}
func (m model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }

func running(tasks []*app.Task) bool {
	for _, t := range tasks {
		if t.Status == "running" {
			return true
		}
	}
	return false
}
func (m model) refresh() tea.Cmd {
	return func() tea.Msg {
		list, err := m.svc.List()
		return listMsg{list: list, err: err}
	}
}
func (m model) task() *app.Task {
	if m.selected < 0 || m.selected >= len(m.tasks) {
		return nil
	}
	return m.tasks[m.selected]
}

func (m model) readEvents() tea.Cmd {
	t := m.task()
	if t == nil {
		return nil
	}
	// Copy cache entries before the command runs; commands never mutate model maps.
	old := make(map[logKey]logSnapshot, len(t.Turns))
	for _, turn := range t.Turns {
		k := logKey{t.ID, turn.Number}
		if entry, ok := m.logs[k]; ok {
			old[k] = entry
		}
	}
	id, name, mode, patchPath, turns := t.ID, t.Agent, t.Mode, t.PatchPath, t.Turns
	previousPatch, hasPatch := m.patches[id]
	home := m.svc.Home
	return func() tea.Msg {
		msg := eventsMsg{id: id, logs: map[logKey]logSnapshot{}}
		for _, turn := range turns {
			k := logKey{id, turn.Number}
			path := filepath.Join(home, "tasks", id, fmt.Sprintf("turn-%d.stdout", turn.Number))
			st, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) && turn.StartedAt == nil {
					err = nil // Queued turns have no stdout yet.
				}
				msg.logs[k] = logSnapshot{size: -1, err: err}
				continue
			}
			if cached, ok := old[k]; ok && cached.err == nil && cached.size == st.Size() {
				msg.logs[k] = cached
				continue
			}
			b, err := os.ReadFile(path)
			// ponytail: growing stdout is still reparsed in O(n); use incremental
			// parsing only if very large logs make this a measured bottleneck.
			msg.logs[k] = logSnapshot{size: int64(len(b)), events: agent.Events(name, b), err: err}
		}
		if mode == "write" {
			if patchPath == "" {
				patchPath = filepath.Join(home, "tasks", id, "patch.diff")
			}
			st, err := os.Stat(patchPath)
			switch {
			case os.IsNotExist(err):
				msg.patch = fileSnapshot{size: -1}
			case err != nil:
				msg.patch = fileSnapshot{size: -1, err: err}
			case hasPatch && previousPatch.err == nil && previousPatch.size == st.Size() && previousPatch.stamp.Equal(st.ModTime()):
				msg.patch = previousPatch
			default:
				b, err := os.ReadFile(patchPath)
				msg.patch = fileSnapshot{size: int64(len(b)), stamp: st.ModTime(), text: string(b), err: err}
			}
		}
		return msg
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case spinMsg:
		// Every message redraws the whole screen, so tick only while something spins.
		if !running(m.tasks) {
			m.spinning = false
			return m, nil
		}
		m.frame = (m.frame + 1) % len(spinner)
		if t := m.task(); t != nil && t.Status == "running" {
			m.syncViewport() // Redraw cached content; no filesystem or service reads.
		}
		return m, spin()
	case tickMsg:
		m.now = time.Time(msg)
		if !m.now.Before(m.noticeUntil) {
			m.notice = ""
		}
		m.syncViewport()
		if running(m.tasks) && !m.spinning {
			m.spinning = true
			return m, tea.Batch(m.refresh(), tick(), spin())
		}
		return m, tea.Batch(m.refresh(), tick())
	case listMsg:
		m.err = msg.err
		if msg.err == nil && msg.list != nil {
			id, number := "", 0
			if t := m.task(); t != nil {
				id, number = t.ID, len(t.Turns)
			}
			m.tasks = msg.list.Tasks
			sort.SliceStable(m.tasks, func(i, j int) bool { return taskGroup(m.tasks[i].Status) < taskGroup(m.tasks[j].Status) })
			m.selected = min(m.selected, max(0, len(m.tasks)-1))
			for i, t := range m.tasks {
				if t.ID == id {
					m.selected = i
					break
				}
			}
			if t := m.task(); t == nil || t.ID != id || len(t.Turns) != number {
				m.viewport.GotoTop()
				m.expand = false
			}
		}
		m.syncViewport()
		return m, m.readEvents()
	case eventsMsg:
		for k, entry := range msg.logs {
			m.logs[k] = entry
		}
		m.patches[msg.id] = msg.patch
	case cancelMsg:
		m.notice = "已请求取消 · " + shortID(msg.id)
		if msg.err != nil {
			m.notice = "取消失败: " + msg.err.Error()
		}
		m.noticeUntil = m.now.Add(3 * time.Second)
		m.syncViewport()
		return m, m.refresh()
	case tea.MouseMsg:
		lw, lh, _, _ := m.layout()
		separator := !m.zoom && ((m.width >= 100 && msg.X == lw && msg.Y >= 1 && msg.Y < m.height-1) || (m.width < 100 && msg.Y == lh+1))
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft && separator {
			m.dragging = true
		}
		if m.dragging {
			if msg.Action == tea.MouseActionMotion {
				if m.width >= 100 {
					m.setRatio((msg.X*100 + (m.width-1)/2) / max(1, m.width-1))
				} else {
					m.setRatio(((msg.Y-1)*100 + (m.height-3)/2) / max(1, m.height-3))
				}
			}
			if msg.Action == tea.MouseActionRelease {
				m.dragging = false
				m.saveRatio()
			}
		} else if m.detail || m.zoom {
			m.viewport, _ = m.viewport.Update(msg)
		}
	case tea.KeyMsg:
		key := msg.String()
		if m.cancelID != "" {
			id := m.cancelID
			m.cancelID, m.cancelTitle = "", ""
			if key == "y" {
				return m, func() tea.Msg {
					_, err := m.svc.Cancel(id)
					return cancelMsg{id: id, err: err}
				}
			}
			return m, nil
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "[", "]", "=":
			ratio := 38
			if key == "[" {
				ratio = m.ratio - 5
			} else if key == "]" {
				ratio = m.ratio + 5
			}
			m.setRatio(ratio)
			m.saveRatio()
		case "z":
			m.zoom = !m.zoom
		case "tab":
			m.detail = !m.detail
		case "1", "2", "3", "4":
			m.tab = int(key[0] - '1')
			m.viewport.GotoTop()
		case "e":
			m.expand = !m.expand
		case "c":
			if t := m.task(); t != nil && (t.Status == "queued" || t.Status == "running" || t.Status == "stale") {
				m.cancelID, m.cancelTitle = t.ID, taskTitle(t)
			}
		case "up", "k", "down", "j":
			if !m.detail && !m.zoom && len(m.tasks) > 0 {
				i := m.selected
				if key == "up" || key == "k" {
					m.selected = max(0, i-1)
				} else {
					m.selected = min(len(m.tasks)-1, i+1)
				}
				if i != m.selected {
					m.viewport.GotoTop()
					m.expand = false
					m.syncViewport()
					return m, m.readEvents()
				}
			} else {
				m.viewport, _ = m.viewport.Update(msg)
			}
		case "pgup", "pgdown", "home", "end":
			if m.detail || m.zoom {
				m.viewport, _ = m.viewport.Update(msg)
			}
		}
	}
	m.syncViewport()
	return m, nil
}

func (m *model) setRatio(ratio int) { m.ratio = max(20, min(70, ratio)) }
func (m model) saveRatio() {
	if os.MkdirAll(m.svc.Home, 0700) != nil {
		return
	}
	b, _ := json.Marshal(struct {
		Ratio int `json:"ratio"`
	}{m.ratio})
	_ = os.WriteFile(filepath.Join(m.svc.Home, "tui.json"), append(b, '\n'), 0600)
}

// Dimensions include pane titles; one cell separates panes, plus top/bottom bars.
func (m model) layout() (lw, lh, dw, dh int) {
	h := max(1, m.height-2)
	if m.zoom {
		return 0, 0, m.width, h
	}
	if m.width >= 100 {
		lw = max(1, (m.width-1)*m.ratio/100)
		return lw, h, max(1, m.width-lw-1), h
	}
	lh = max(1, (h-1)*m.ratio/100)
	return m.width, lh, m.width, max(1, h-lh-1)
}
func (m *model) syncViewport() {
	_, _, w, h := m.layout()
	bottom := (m.detail || m.zoom) && m.viewport.YOffset > 0 && m.viewport.AtBottom()
	m.viewport.Width, m.viewport.Height = max(1, w-2), max(1, h-1)
	m.viewport.SetContent(m.details())
	if bottom {
		m.viewport.GotoBottom()
	}
}
