package tui

import (
	"baton/internal/app"
	"baton/internal/verify"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var groupNames = []string{"需要关注", "进行中", "已完成"}
var tabs = []string{"对话", "事件", "改动", "检查"}

func taskGroup(status string) int {
	switch status {
	case "failed", "stale":
		return 0
	case "running", "queued":
		return 1
	default:
		return 2
	}
}
func taskTitle(t *app.Task) string { return strings.ReplaceAll(clean(t.Name()), "\n", " ") }
func (m model) glyph(status string) string {
	if status == "running" {
		return spinner[m.frame]
	}
	return app.Glyph(status)
}
func statusName(status string) string {
	switch status {
	case "running":
		return "运行中"
	case "queued":
		return "排队中"
	case "failed":
		return "失败"
	case "stale":
		return "失联"
	case "succeeded":
		return "已完成"
	case "canceled":
		return "已取消"
	}
	return status
}
func checkMeta(t *app.Task) string {
	return checkSummary(latestVerify(t), len(t.Checks))
}
func latestVerify(t *app.Task) verify.Result {
	if len(t.Turns) > 0 {
		last := t.Turns[len(t.Turns)-1]
		if last.Verify != nil {
			return *last.Verify
		}
		if t.Verify.Round != 0 && t.Verify.Round != last.Number {
			return verify.Result{}
		}
	}
	return t.Verify
}
func checkSummary(result verify.Result, total int) string {
	if total == 0 {
		return "无检查"
	}
	passed := 0
	for _, c := range result.Checks {
		if c.Status == verify.StatusPass {
			passed++
		}
	}
	return fmt.Sprintf("检查 %d/%d", passed, total)
}

// taskList shows the list pane's lines around the selection.
func (m model) taskList(w, h int) string {
	if len(m.tasks) == 0 && !m.settingsMode {
		return m.palette.text(m.palette.dim).Render("暂无对话")
	}
	lines, _, selected := m.listLines(w)
	start := listStart(selected, h)
	return strings.Join(lines[start:min(len(lines), start+max(1, h))], "\n")
}

// listStart keeps the selected row, two lines tall for tasks, in view.
func listStart(selected, h int) int { return max(0, selected+2-h) }

// listLines returns the list pane's lines, the task or field index of each
// line (-1 for headers) and the selected line.
func (m model) listLines(w int) ([]string, []int, int) {
	if m.settingsMode {
		return m.settingsLines(w)
	}
	p := m.palette
	var lines []string
	var owners []int
	selectedLine := 0
	for group, name := range groupNames {
		count := 0
		for _, t := range m.tasks {
			if taskGroup(t.Status) == group {
				count++
			}
		}
		if count == 0 {
			continue
		}
		lines = append(lines, p.text(p.faint).Render(fit(fmt.Sprintf(" %s  %d", name, count), w)))
		owners = append(owners, -1)
		for i, t := range m.tasks {
			if taskGroup(t.Status) != group {
				continue
			}
			bar, bg := p.term, p.term
			if i == m.selected {
				selectedLine = len(lines)
				bar, bg = p.accent, p.sel
				if m.detail || m.zoom {
					bar = p.faint
				}
			}
			stripe := p.text(bar).Background(bg).Render("▎")
			tm := elapsed(t, m.now)
			left := p.text(p.status(t.Status)).Render(m.glyph(t.Status)) + " " + p.text(p.ink).Bold(true).Render(fit(taskTitle(t), max(1, w-6-lipgloss.Width(tm))))
			first := stripe + " " + rightRow(left, p.text(p.dim).Render(tm), max(1, w-3)) + " "
			meta := fmt.Sprintf("%s · %s · 第 %d 轮 · %s", t.Who(), app.ModeName(t.Mode), len(t.Turns), checkMeta(t))
			if t.Group != "" {
				meta = "#" + t.Group + " · " + meta
			}
			second := stripe + "   " + p.text(p.dim).Render(fit(meta, max(1, w-5))) + " "
			style := p.text(p.ink).Background(bg)
			lines = append(lines, paint(first, style, w), paint(second, style, w))
			owners = append(owners, i, i)
		}
	}
	return lines, owners, selectedLine
}

func (m model) summary(t *app.Task, w int) string {
	p := m.palette
	state := p.status(t.Status)
	pill := p.text(state).Background(p.code).Bold(true).Padding(0, 1).Render(app.Glyph(t.Status) + " " + statusName(t.Status))
	lines := []string{rightRow(p.text(p.ink).Bold(true).Render(fit(taskTitle(t), max(1, w-lipgloss.Width(pill)-1))), pill, w)}
	timing := "共 " + totalElapsed(t, m.now)
	if t.Status == "running" {
		timing = fmt.Sprintf("第 %d 轮 %s", len(t.Turns), elapsed(t, m.now))
	}
	cost, sum, known := "未知", 0.0, false
	for _, turn := range t.Turns {
		if turn.CostUSD != nil {
			sum += *turn.CostUSD
			known = true
		}
	}
	if known {
		cost = fmt.Sprintf("$%.6f", sum)
	}
	meta := fmt.Sprintf("%s · %s / %s · %s · %s · 成本 %s", t.Who(), value(t.Model), value(t.Effort), app.ModeName(t.Mode), timing, cost)
	if t.BaseTask != "" {
		meta += " · 接着 " + t.BaseTask[max(0, len(t.BaseTask)-8):]
	}
	lines = append(lines, p.text(p.dim).Render(ansi.Wrap(clean(meta), w, "")))
	chips := []string{p.text(p.dim).Render("检查")}
	result := latestVerify(t)
	for i, command := range t.Checks {
		status := "pending"
		if i < len(result.Checks) {
			status = result.Checks[i].Status
		}
		icon, color := checkStyle(p, status)
		words := strings.Fields(clean(command))
		chips = append(chips, p.text(color).Render(icon+" "+strings.Join(words[:min(2, len(words))], " ")))
	}
	if len(t.Checks) == 0 {
		chips = append(chips, p.text(p.dim).Render("无"))
	}
	a, d, files := diffCounts(m.patches[t.ID].text)
	changes := p.text(p.dim).Render("改动 无")
	if files > 0 {
		changes = p.text(p.dim).Render("改动 ") + p.text(p.ok).Render(fmt.Sprintf("+%d", a)) + " " + p.text(p.bad).Render(fmt.Sprintf("−%d", d)) + p.text(p.dim).Render(fmt.Sprintf(" · %d 个文件", files))
	}
	chips = append(chips, changes)
	lines = append(lines, ansi.Wrap(strings.Join(chips, "  "), w, ""))
	if t.Status == "failed" || t.Status == "stale" {
		if alert := failureAlert(t); alert != "" {
			lines = append(lines, p.text(p.bad).Background(p.badBG).Width(w).Render(ansi.Wrap("✗ "+clean(alert), w, "")))
		}
	}
	return p.box(strings.Join(lines, "\n"), state, w)
}

// box is the summary block: a tinted body with a colored left bar.
func (p palette) box(body string, color lipgloss.Color, w int) string {
	body = paint(body, p.text(p.ink).Background(p.sum), w)
	return lipgloss.NewStyle().Border(lipgloss.Border{Left: "▎"}, false, false, false, true).BorderForeground(color).BorderBackground(p.sum).Render(body)
}

// tabRow is the details line holding the tab labels, for mouse hits.
func (m model) tabRow() int {
	t := m.task()
	if t == nil || m.settingsMode {
		return -1
	}
	return lipgloss.Height(m.summary(t, max(1, max(1, m.viewport.Width)-1))) + 1
}

func (m model) details() string {
	clear(m.folds)
	if m.settingsMode {
		return m.settingsDetail(max(1, m.viewport.Width))
	}
	t := m.task()
	if t == nil {
		return m.palette.text(m.palette.dim).Render("请选择对话")
	}
	w := max(1, m.viewport.Width)
	p := m.palette
	lines := []string{m.summary(t, max(1, w-1)), ""}
	var tabNames []string
	for i, name := range tabs {
		style := p.text(p.dim)
		if i == m.tab {
			style = p.text(p.accent).Bold(true).Underline(true)
		}
		tabNames = append(tabNames, style.Render(fmt.Sprintf("%d %s", i+1, name)))
	}
	lines = append(lines, strings.Join(tabNames, "  "), "")
	switch m.tab {
	case 0:
		text, folds := m.conversation(t, w)
		for row, k := range folds {
			m.folds[rows(lines)+row] = k
		}
		lines = append(lines, text)
	case 1:
		if len(t.Turns) > 0 {
			entry := m.logs[logKey{t.ID, t.Turns[len(t.Turns)-1].Number}]
			lines = append(lines, m.eventRows(entry, w))
		}
	case 2:
		lines = append(lines, m.diffView(t, w))
	case 3:
		lines = append(lines, m.checksView(t, w))
	}
	for _, warning := range t.Warnings {
		lines = append(lines, p.text(p.wait).Render(ansi.Wrap("提示: "+clean(warning), w, "")))
	}
	branch := clean(t.Branch)
	if branch == "" {
		branch = "−"
	}
	lines = append(lines, "", p.text(p.faint).Render(ansi.Wrap(fmt.Sprintf("ID %s · %s · %s", t.ID, filepath.Base(t.Repo), branch), w, "")))
	return strings.Join(lines, "\n")
}

// conversation also returns the row of each fold line, for clicks.
func (m model) conversation(t *app.Task, w int) (string, map[int]foldKey) {
	p := m.palette
	var lines []string
	folds := map[int]foldKey{}
	for i, turn := range t.Turns {
		clock := "排队中"
		if turn.StartedAt != nil {
			clock = turn.StartedAt.Local().Format("15:04")
		}
		prompt, long := m.prompt(t.ID, turn, w)
		if long {
			folds[rows(lines)+1+strings.Count(prompt, "\n")] = foldKey{t.ID, turn.Number, true}
		}
		lines = append(lines, p.text(p.ink).Bold(true).Render(fmt.Sprintf("你 · 第 %d 轮 · %s", turn.Number, clock)), prompt, "")
		entry := m.logs[logKey{t.ID, turn.Number}]
		if entry.err != nil && !(i == len(t.Turns)-1 && t.Status == "running") {
			lines = append(lines, p.text(p.bad).Render(ansi.Wrap("读取 stdout 失败: "+clean(entry.err.Error()), w, "")))
		}
		tools := 0
		for _, e := range entry.events {
			if e.Kind == "tool" {
				tools++
			}
		}
		key := foldKey{id: t.ID, turn: turn.Number}
		open := m.opened(key)
		fold := p.text(p.faint).Render(fmt.Sprintf("▸ %d 个工具调用 · 点击展开", tools))
		if open {
			fold = p.text(p.faint).Render(fmt.Sprintf("▾ %d 个工具调用 · 点击收起", tools))
		}
		if i == len(t.Turns)-1 && t.Status == "running" {
			lines = append(lines, p.text(p.ink).Bold(true).Render(fmt.Sprintf("%s · 第 %d 轮", t.Who(), turn.Number))+p.text(p.run).Render(" · "+app.Glyph("running")+" 正在执行 "+elapsed(t, m.now)))
			if !open {
				entry.events = entry.events[max(0, len(entry.events)-4):]
			}
			folds[rows(lines)] = key
			lines = append(lines, fold, m.eventRows(entry, w))
		} else if turn.StartedAt != nil {
			checks := "检查结果未记录"
			result := turn.Verify
			if result == nil && i == len(t.Turns)-1 {
				result = &t.Verify
			}
			if result != nil {
				checks = checkSummary(*result, len(t.Checks))
				if result.Status == verify.StatusFail || result.Status == verify.StatusUnavailable {
					checks += " 未通过"
				} else if result.Status == verify.StatusPass {
					checks += " 通过"
				}
			}
			lines = append(lines, p.text(p.ink).Bold(true).Render(fmt.Sprintf("%s · 第 %d 轮 · %s · %s", t.Who(), turn.Number, turnElapsed(turn, m.now), checks)))
			folds[rows(lines)] = key
			lines = append(lines, fold)
			if open {
				lines = append(lines, m.eventRows(entry, w))
			}
			if turn.FinalText != "" {
				lines = append(lines, m.reply(t.ID, turn, w))
			}
			if turn.Error != "" {
				lines = append(lines, p.text(p.bad).Render(ansi.Wrap(clean(turn.Error), w, "")))
			}
		}
		lines = append(lines, "")
	}
	if t.Status == "queued" {
		lines = append(lines, p.text(p.wait).Render("◌ 等待开始…"))
	}
	return strings.Join(lines, "\n"), folds
}

// rows counts the rows lines take once joined with newlines.
func rows(lines []string) int {
	n := 0
	for _, l := range lines {
		n += strings.Count(l, "\n") + 1
	}
	return n
}

// opened reports whether a fold shows its content: e sets all, a click flips one.
func (m model) opened(k foldKey) bool { return m.expand != m.flip[k] }

func (m model) reply(id string, turn app.Turn, w int) string {
	return m.markdownText("reply", id, turn.Number, turn.FinalText, w)
}

// Long prompts (a whole task brief) fold so the replies stay in view.
const promptLines = 12

// prompt reports long when the prompt ends in a fold line.
func (m model) prompt(id string, turn app.Turn, w int) (text string, long bool) {
	rendered := m.markdownText("prompt", id, turn.Number, turn.Prompt, w)
	lines := strings.Split(rendered, "\n")
	if len(lines) <= promptLines {
		return rendered, false
	}
	faint := m.palette.text(m.palette.faint)
	if m.opened(foldKey{id, turn.Number, true}) {
		return rendered + "\n" + faint.Render(fmt.Sprintf("  ▴ 共 %d 行 · 点击收起", len(lines))), true
	}
	more := faint.Render(fmt.Sprintf("  ▸ 还有 %d 行 · 点击展开", len(lines)-promptLines))
	return strings.Join(append(lines[:promptLines:promptLines], more), "\n"), true
}

func (m model) markdownText(kind, id string, turn int, text string, w int) string {
	key := markdownKey{kind: kind, id: id, turn: turn, width: w, theme: m.theme, length: len(text)}
	if rendered, ok := m.markdown[key]; ok {
		return rendered
	}
	// Tabs survive here: the renderer expands them so code keeps its indentation.
	source := strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
	rendered, err := m.render(source, w, m.theme)
	if err != nil {
		rendered = ansi.Wrap(clean(text), w, "")
	}
	// Keep one rendering per task/turn/kind rather than retaining every resize/frame.
	for old := range m.markdown {
		if old.kind == kind && old.id == id && old.turn == turn {
			delete(m.markdown, old)
		}
	}
	rendered = strings.Trim(rendered, "\n")
	m.markdown[key] = rendered
	return rendered
}
func (m model) eventRows(entry logSnapshot, w int) string {
	p := m.palette
	var lines []string
	if entry.err != nil {
		lines = append(lines, p.text(p.bad).Render(fit("读取 stdout 失败: "+entry.err.Error(), w)))
	}
	for _, e := range entry.events {
		icon, color := "▸", p.dim
		switch e.Category {
		case "read":
			icon = "◇"
		case "search":
			icon = "⌕"
		case "edit":
			icon, color = "✎", p.wait
		case "message":
			icon = "›"
		case "error":
			icon, color = "✗", p.bad
		}
		left := p.text(color).Render(icon) + " " + p.text(p.dim).Render(fit(e.Text, max(1, w-2)))
		lines = append(lines, rightRow(left, p.text(p.dim).Render(clean(e.Right)), w))
	}
	if len(lines) == 0 {
		return p.text(p.faint).Render("还没有事件。")
	}
	return strings.Join(lines, "\n")
}
func diffCounts(patch string) (add, del, files int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files++
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			add++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			del++
		}
	}
	return
}
func (m model) diffView(t *app.Task, w int) string {
	p := m.palette
	if t.Mode == "read" {
		return p.text(p.dim).Render("只读任务，没有改动")
	}
	patch := m.patches[t.ID]
	if patch.err != nil {
		return p.text(p.bad).Render(ansi.Wrap("读取 patch.diff 失败: "+clean(patch.err.Error()), w, ""))
	}
	if patch.text == "" {
		return p.text(p.dim).Render("还没有改动。")
	}
	lines := strings.Split(strings.TrimSuffix(clean(patch.text), "\n"), "\n")
	var out []string
	for _, line := range lines[:min(2000, len(lines))] {
		style := p.text(p.ink).Background(p.code)
		switch {
		case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			style = style.Bold(true)
		case strings.HasPrefix(line, "@@"):
			style = style.Foreground(p.kw)
		case strings.HasPrefix(line, "+"):
			style = style.Foreground(p.ok).Background(p.addBG)
		case strings.HasPrefix(line, "-"):
			style = style.Foreground(p.bad).Background(p.delBG)
		}
		out = append(out, style.Width(w).Render(fit(line, w)))
	}
	if len(lines) > 2000 {
		out = append(out, p.text(p.dim).Render(fmt.Sprintf("… 已截断，仅显示前 2000 行（共 %d 行）", len(lines))))
	}
	return strings.Join(out, "\n")
}
func checkStyle(p palette, status string) (string, lipgloss.Color) {
	switch status {
	case verify.StatusPass:
		return "✓", p.ok
	case verify.StatusFail, verify.StatusUnavailable:
		return "✗", p.bad
	default:
		return "◌", p.wait
	}
}
func (m model) checksView(t *app.Task, w int) string {
	p := m.palette
	if len(t.Checks) == 0 {
		return p.text(p.dim).Render("这个任务没有配置检查。")
	}
	var lines []string
	result := latestVerify(t)
	for i, command := range t.Checks {
		status, right := "pending", "◌ 等待中"
		var outcome verify.Outcome
		if i < len(result.Checks) {
			outcome = result.Checks[i]
			status = outcome.Status
			right = fmt.Sprintf("%.1fs · exit %d", float64(outcome.DurationMS)/1000, outcome.ExitCode)
		}
		icon, color := checkStyle(p, status)
		lines = append(lines, rightRow(p.text(color).Render(icon)+" $ "+clean(command), p.text(p.dim).Render(right), w))
		if status == verify.StatusFail || status == verify.StatusUnavailable {
			output := outcome.OutputTail
			if output == "" {
				output = outcome.Reason
			}
			tail := strings.Split(strings.TrimSpace(clean(output)), "\n")
			tail = tail[max(0, len(tail)-12):]
			for _, line := range tail {
				lines = append(lines, p.text(p.dim).Background(p.code).Width(w).Render(fit("  "+line, w)))
			}
		}
	}
	return strings.Join(lines, "\n")
}

var failureLocation = regexp.MustCompile(`\.go:\d+:`)

func failureLine(output string) string {
	lines := strings.Split(strings.TrimSpace(clean(output)), "\n")
	// Prefer source locations and diagnoses over the trailing package-level FAIL.
	for _, line := range lines {
		if failureLocation.MatchString(line) || strings.Contains(strings.ToLower(line), "panic") || strings.Contains(strings.ToLower(line), "error") {
			return strings.TrimSpace(line)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "FAIL") {
			return strings.TrimSpace(line)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
func failureAlert(t *app.Task) string {
	for _, c := range latestVerify(t).Checks {
		if c.Status == verify.StatusFail || c.Status == verify.StatusUnavailable {
			line := failureLine(c.OutputTail)
			if line == "" {
				line = c.Reason
			}
			return c.Command + " · " + line
		}
	}
	if len(t.Turns) > 0 {
		return t.Turns[len(t.Turns)-1].Error
	}
	return ""
}

func (m model) View() string {
	p := m.palette
	lw, lh, dw, dh := m.layout()
	pane := func(title, body string, w, h int, focused bool) string {
		color := p.faint
		if focused {
			color = p.accent
		}
		return p.text(p.ink).Background(p.term).Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(p.text(color).Render(" "+title) + "\n" + body)
	}
	listTitle, detailTitle := "对话", "详情"
	if m.settingsMode {
		listTitle, detailTitle = "设置", "说明"
	}
	details := pane(detailTitle, " "+strings.ReplaceAll(m.viewport.View(), "\n", "\n "), dw, dh, !m.settingsMode && (m.detail || m.zoom))
	body := details
	if !m.zoom {
		list := pane(listTitle, m.taskList(lw, max(1, lh-1)), lw, lh, m.settingsMode || !m.detail)
		color := p.line
		if m.dragging {
			color = p.accent
		}
		if m.width >= 100 {
			divider := p.text(color).Background(p.term).Render(strings.TrimSuffix(strings.Repeat("│\n", lh), "\n"))
			body = lipgloss.JoinHorizontal(lipgloss.Top, list, divider, details)
		} else {
			body = lipgloss.JoinVertical(lipgloss.Left, list, p.text(color).Render(strings.Repeat("─", m.width)), details)
		}
	}
	left := p.text(p.accent).Bold(true).Render("Baton")
	for i, status := range []string{"running", "queued", "failed", "succeeded"} {
		n := 0
		for _, t := range m.tasks {
			if t.Status == status {
				n++
			}
		}
		left += p.text(p.status(status)).Render(fmt.Sprintf("  %s %d %s", []string{"●", "◌", "✗", "✓"}[i], n, []string{"运行", "排队", "失败", "完成"}[i]))
	}
	if n := m.set.changed(); n > 0 {
		left += p.text(p.wait).Render(fmt.Sprintf("  ● %d 项设置未保存", n))
	}
	where := m.svc.Home
	if m.settingsMode {
		where = "设置 · " + m.set.path
	}
	right := p.text(p.faint).Render(clean(where) + " · " + m.now.Format("15:04:05"))
	header := left
	if available := m.width - lipgloss.Width(left) - 1; available > 0 {
		if lipgloss.Width(right) > available {
			right = ansi.TruncateLeft(right, lipgloss.Width(right)-available+1, "…")
		}
		header = rightRow(left, right, m.width)
	}
	if m.err != nil {
		header = p.text(p.bad).Render(fit("Error: "+m.err.Error(), m.width))
	}
	footer := m.footer()
	screen := paint(fitANSI(header, m.width)+"\n"+body+"\n"+footer, p.text(p.ink).Background(p.term), m.width)
	return lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(screen)
}
func (m model) footer() string {
	p := m.palette
	if m.cancelID != "" {
		return p.text(p.bad).Background(p.badBG).Width(m.width).Render(fit("取消「"+m.cancelTitle+"」？ y 确认  esc 返回", m.width))
	}
	if m.cleanPlan != nil {
		it := m.cleanPlan.Tasks[0]
		what := "记录"
		if it.Mode == "write" {
			what = "worktree、分支 " + it.Branch + " 和记录"
		}
		return p.text(p.bad).Background(p.badBG).Width(m.width).Render(fit(fmt.Sprintf("删除「%s」及其%s（%.1f MB）？ y 确认  esc 返回", it.Title, what, float64(it.Bytes)/1e6), m.width))
	}
	if m.settingsMode && m.set.quit {
		return p.text(p.bad).Background(p.badBG).Width(m.width).Render(fit(fmt.Sprintf("设置有 %d 项未保存，再按 q 放弃并退出，其他键返回", m.set.changed()), m.width))
	}
	pairs := [][2]string{{"↑↓", "选择"}, {"tab/点击", "切焦点"}, {"1-4", "标签"}, {"[ ]", "比例"}, {"z", "专注"}, {"e", "展开"}, {"c", "取消"}, {"d", "删除"}, {"x", "导出"}, {"s", "设置"}, {"q", "退出"}}
	right := fmt.Sprintf("列表 %d%% · 详情 %d%%", m.ratio, 100-m.ratio)
	if m.settingsMode {
		pairs = [][2]string{{"↑↓", "选择"}, {"←→", "切换"}, {"enter", "输入"}, {"d", "默认"}, {"u", "撤销"}, {"ctrl+s", "保存"}, {"esc", "返回"}}
		if m.set.editing {
			pairs = [][2]string{{"enter", "确定"}, {"esc", "取消"}}
			if m.set.fields[m.set.cursor].key == "name" {
				pairs = [][2]string{{"输入", "检索"}, {"↑↓", "选择"}, {"enter", "打开或新建"}, {"esc", "取消"}}
			}
		}
		right = fmt.Sprintf("列表 %d%% · 说明 %d%%", m.ratio, 100-m.ratio)
	}
	var keys []string
	for _, kv := range pairs {
		keys = append(keys, p.text(p.ink).Background(p.kbd).Bold(true).Render(kv[0])+" "+p.text(p.dim).Render(kv[1]))
	}
	if m.notice != "" {
		right = m.notice
	}
	return paint(rightRow(strings.Join(keys, "  "), p.text(p.faint).Render(right), m.width), p.text(p.dim).Background(p.bar), m.width)
}

// paint fills each line to w with st's colors. lipgloss ends every styled
// segment with a full SGR reset, which also clears an enclosing background,
// so st is re-applied after each reset.
func paint(s string, st lipgloss.Style, w int) string {
	on, _, ok := strings.Cut(st.Render("x"), "x")
	if !ok || on == "" {
		return lipgloss.NewStyle().Width(w).Render(s) // No color support.
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = fitANSI(line, w)
		lines[i] = on + strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+on) + strings.Repeat(" ", max(0, w-lipgloss.Width(line))) + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}
func rightRow(left, right string, w int) string {
	if right == "" {
		return fitANSI(left, w)
	}
	right = fitANSI(right, w)
	space := max(0, w-lipgloss.Width(right)-1)
	left = fitANSI(left, space)
	return left + strings.Repeat(" ", max(0, w-lipgloss.Width(left)-lipgloss.Width(right))) + right
}
func fitANSI(s string, width int) string { return ansi.Truncate(s, max(0, width), "…") }
func fit(s string, width int) string {
	return fitANSI(strings.ReplaceAll(clean(s), "\n", " "), width)
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
	}, ansi.Strip(s))
}
func value(s string) string {
	if s == "" {
		return "默认"
	}
	return clean(s)
}
func turnDuration(turn app.Turn, now time.Time) time.Duration {
	if turn.StartedAt == nil {
		return 0
	}
	if turn.EndedAt != nil {
		now = *turn.EndedAt
	}
	return max(time.Duration(0), now.Sub(*turn.StartedAt)).Round(time.Second)
}
func formatDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}
func turnElapsed(turn app.Turn, now time.Time) string {
	if turn.StartedAt == nil {
		return "−"
	}
	return formatDuration(turnDuration(turn, now))
}
func elapsed(t *app.Task, now time.Time) string {
	if len(t.Turns) == 0 {
		return "−"
	}
	return turnElapsed(t.Turns[len(t.Turns)-1], now)
}
func totalElapsed(t *app.Task, now time.Time) string {
	var d time.Duration
	for _, turn := range t.Turns {
		d += turnDuration(turn, now)
	}
	return formatDuration(d)
}
