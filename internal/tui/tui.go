package tui

import (
	"baton/internal/agent"
	"baton/internal/app"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func Run(svc *app.Service) error {
	_, err := tea.NewProgram(newModel(svc), tea.WithAltScreen()).Run()
	return err
}

type model struct {
	svc           *app.Service
	tasks         []*app.Task
	selected      int
	width, height int
	detail        bool
	viewport      viewport.Model
	events        []agent.Event
	eventErr      error
	err           error
	now           time.Time
	cancelID      string
	notice        string
}

type tickMsg time.Time
type listMsg struct {
	list *app.ListResult
	err  error
}
type eventsMsg struct {
	id     string
	turn   int
	events []agent.Event
	err    error
}
type cancelMsg struct {
	id  string
	err error
}

func newModel(svc *app.Service) model {
	m := model{svc: svc, width: 120, height: 30, now: time.Now(), viewport: viewport.New(1, 1)}
	m.syncViewport()
	return m
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }

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
	if t == nil || len(t.Turns) == 0 {
		return nil
	}
	id, number, name := t.ID, t.Turns[len(t.Turns)-1].Number, t.Agent
	return func() tea.Msg {
		b, err := os.ReadFile(filepath.Join(m.svc.Home, "tasks", id, fmt.Sprintf("turn-%d.stdout", number)))
		events := agent.Events(name, b)
		if len(events) > 40 {
			events = events[len(events)-40:]
		}
		return eventsMsg{id: id, turn: number, events: events, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.syncViewport()
	case tickMsg:
		m.now = time.Time(msg)
		return m, tea.Batch(m.refresh(), tick())
	case listMsg:
		m.err = msg.err
		if msg.err == nil && msg.list != nil {
			id := ""
			number := 0
			if t := m.task(); t != nil {
				id = t.ID
				if len(t.Turns) > 0 {
					number = t.Turns[len(t.Turns)-1].Number
				}
			}
			m.tasks = msg.list.Tasks
			m.selected = min(m.selected, max(0, len(m.tasks)-1))
			for i, t := range m.tasks {
				if t.ID == id {
					m.selected = i
					break
				}
			}
			if t := m.task(); t == nil || t.ID != id || (len(t.Turns) > 0 && t.Turns[len(t.Turns)-1].Number != number) {
				m.events, m.eventErr = nil, nil
				m.viewport.GotoTop()
			}
		}
		m.syncViewport()
		return m, m.readEvents()
	case eventsMsg:
		// A refresh for a previous selection/turn must not replace live events.
		if t := m.task(); t != nil && t.ID == msg.id && len(t.Turns) > 0 && t.Turns[len(t.Turns)-1].Number == msg.turn {
			m.events, m.eventErr = msg.events, msg.err
			m.syncViewport()
		}
	case cancelMsg:
		m.notice = "Cancellation requested: " + shortID(msg.id)
		if msg.err != nil {
			m.notice = "Cancel error: " + msg.err.Error()
		}
		return m, m.refresh()
	case tea.KeyMsg:
		key := msg.String()
		if key == "q" || key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.cancelID != "" {
			id := m.cancelID
			m.cancelID = ""
			if key == "y" {
				return m, func() tea.Msg {
					_, err := m.svc.Cancel(id)
					return cancelMsg{id: id, err: err}
				}
			}
			return m, nil
		}
		switch key {
		case "tab":
			m.detail = !m.detail
		case "c":
			if t := m.task(); t != nil && (t.Status == "queued" || t.Status == "running" || t.Status == "stale") {
				m.cancelID = t.ID
			}
		case "up", "k", "down", "j":
			if !m.detail && len(m.tasks) > 0 {
				i := m.selected
				if key == "up" || key == "k" {
					m.selected = max(0, i-1)
				} else {
					m.selected = min(len(m.tasks)-1, i+1)
				}
				if i != m.selected {
					m.events, m.eventErr = nil, nil
					m.viewport.GotoTop()
					m.syncViewport()
					return m, m.readEvents()
				}
			}
		}
		if m.detail && key != "tab" && key != "c" {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// Panel dimensions include borders; the header and key hints use two rows.
func (m model) layout() (lw, lh, dw, dh int) {
	h := max(4, m.height-2)
	if m.width >= 120 {
		lw = min(80, m.width*3/5)
		return lw, h, m.width - lw, h
	}
	lh = max(3, h/3)
	return m.width, lh, m.width, max(3, h-lh)
}

func (m *model) syncViewport() {
	_, _, w, h := m.layout()
	bottom := m.detail && m.viewport.AtBottom()
	m.viewport.Width, m.viewport.Height = max(1, w-2), max(1, h-3)
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(m.details()))
	if bottom {
		m.viewport.GotoBottom()
	}
}

func (m model) details() string {
	t := m.task()
	if t == nil {
		return "No task selected."
	}
	lines := []string{
		"ID: " + t.ID, "Repo: " + t.Repo, "Branch: " + value(t.Branch),
		"Model / effort: " + value(t.Model) + " / " + value(t.Effort),
		"Session: " + value(t.SessionID),
	}
	var turn *app.Turn
	if len(t.Turns) > 0 {
		turn = &t.Turns[len(t.Turns)-1]
		lines = append(lines, fmt.Sprintf("Turn %d prompt: %s", turn.Number, fit(turn.Prompt, 360)))
	}
	lines = append(lines, "", "Live events (latest below)")
	if m.eventErr != nil {
		lines = append(lines, "Error reading stdout: "+m.eventErr.Error())
	}
	for _, e := range m.events {
		lines = append(lines, e.Kind+": "+e.Text)
	}
	if len(m.events) == 0 && m.eventErr == nil {
		lines = append(lines, "Waiting for events…")
	}
	lines = append(lines, "", "Final reply")
	if turn != nil {
		lines = append(lines, value(turn.FinalText))
		if turn.Error != "" {
			lines = append(lines, "Error: "+turn.Error)
		}
	}
	lines = append(lines, "", "Diffstat", value(t.Diffstat), "", "Checks: "+value(t.Verify.Status))
	if t.Verify.Notes != "" {
		lines = append(lines, t.Verify.Notes)
	}
	for _, c := range t.Verify.Checks {
		lines = append(lines, fmt.Sprintf("%s (exit %d) $ %s", c.Status, c.ExitCode, c.Command))
		if c.Reason != "" {
			lines = append(lines, "  "+c.Reason)
		}
	}
	for i := len(t.Verify.Checks); i < len(t.Checks); i++ {
		lines = append(lines, "pending (exit -) $ "+t.Checks[i])
	}
	lines = append(lines, "", "Warnings")
	if len(t.Warnings) == 0 {
		lines = append(lines, "none")
	}
	lines = append(lines, t.Warnings...)
	cost := "unknown"
	if turn != nil && turn.CostUSD != nil {
		cost = fmt.Sprintf("$%.6f", *turn.CostUSD)
	}
	lines = append(lines, "", "Cost (latest turn): "+cost)
	return clean(strings.Join(lines, "\n"))
}

func (m model) taskList(w, h int) string {
	if len(m.tasks) == 0 {
		return "No tasks."
	}
	wide := m.width >= 120
	rows := 1
	var lines []string
	if wide {
		lines = append(lines, tableRow(w, " ", []string{"ID", "Agent", "Mode", "Status", "Turns", "Elapsed", "Check", "Repo"}, false))
	} else {
		rows = 2
	}
	count := max(1, (h-len(lines))/rows)
	start := max(0, m.selected-count+1)
	for i := start; i < min(len(m.tasks), start+count); i++ {
		t := m.tasks[i]
		marker := " "
		if i == m.selected {
			marker = ">"
		}
		if wide {
			lines = append(lines, tableRow(w, marker, []string{shortID(t.ID), t.Agent, t.Mode, t.Status, fmt.Sprint(len(t.Turns)), elapsed(t, m.now), value(t.Verify.Status), filepath.Base(t.Repo)}, true))
		} else {
			prefix := fmt.Sprintf("%s %s %s %s ", marker, shortID(t.ID), t.Agent, t.Mode)
			lines = append(lines, fit(prefix, w)+statusStyle(t.Status).Render(fit(t.Status, max(0, w-lipgloss.Width(prefix)))))
			lines = append(lines, fit(fmt.Sprintf("  #%d %s check=%s %s", len(t.Turns), elapsed(t, m.now), value(t.Verify.Status), filepath.Base(t.Repo)), w))
		}
	}
	return strings.Join(lines, "\n")
}

func tableRow(w int, marker string, cells []string, colored bool) string {
	widths := []int{8, 6, 5, 9, 5, 7, 11, max(1, w-60)}
	parts := []string{marker}
	for i, c := range cells {
		cell := lipgloss.NewStyle().Width(widths[i]).Render(fit(c, widths[i]))
		if colored && i == 3 {
			cell = statusStyle(c).Render(cell)
		}
		parts = append(parts, cell)
	}
	return strings.Join(parts, " ")
}

func pane(title, body string, w, h int, focused bool) string {
	color := lipgloss.Color("8")
	if focused {
		color = "4"
		title += " [focus]"
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(color).
		Width(max(1, w-2)).Height(max(1, h-2)).MaxWidth(w).MaxHeight(h).
		Render(fit(title, max(1, w-2)) + "\n" + body)
}

func (m model) View() string {
	lw, lh, dw, dh := m.layout()
	list := pane("Tasks", m.taskList(max(1, lw-2), max(1, lh-3)), lw, lh, !m.detail)
	details := pane("Details", m.viewport.View(), dw, dh, m.detail)
	body := lipgloss.JoinVertical(lipgloss.Left, list, details)
	if m.width >= 120 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, details)
	}
	header := "Baton · refresh 1s"
	if m.notice != "" {
		header = m.notice
	}
	if m.err != nil {
		header = "Error: " + m.err.Error()
	}
	footer := "↑/↓ k/j select · tab details/scroll · c cancel · q/ctrl+c quit"
	if m.cancelID != "" {
		footer = "Cancel " + shortID(m.cancelID) + "? y confirm · any other key dismiss"
	}
	return lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).
		Render(fit(header, m.width) + "\n" + body + "\n" + fit(footer, m.width))
}

func statusStyle(status string) lipgloss.Style {
	color := lipgloss.Color("8")
	switch status {
	case "running":
		color = "6"
	case "queued":
		color = "3"
	case "succeeded":
		color = "2"
	case "failed":
		color = "1"
	case "stale":
		color = "5"
	}
	return lipgloss.NewStyle().Foreground(color)
}

func elapsed(t *app.Task, now time.Time) string {
	if len(t.Turns) == 0 {
		return "-"
	}
	turn := t.Turns[len(t.Turns)-1]
	if turn.StartedAt == nil {
		return "-"
	}
	if turn.EndedAt != nil {
		now = *turn.EndedAt
	}
	return max(time.Duration(0), now.Sub(*turn.StartedAt)).Round(time.Second).String()
}

func shortID(id string) string {
	// Task IDs end in random hex; a timestamp prefix is not distinctive.
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func value(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func fit(s string, width int) string {
	s = strings.ReplaceAll(clean(s), "\n", " ")
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		n := lipgloss.Width(string(r))
		if used+n > width-1 {
			break
		}
		b.WriteRune(r)
		used += n
	}
	return b.String() + "…"
}
