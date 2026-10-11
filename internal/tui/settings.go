package tui

import (
	"baton/internal/agent"
	"baton/internal/app"
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// settings edits the user's config.json in place of the task list; s toggles it.
// The draft outlives leaving the mode, so unsaved edits are never dropped silently.
type settings struct {
	loaded       bool
	path         string
	raw          []byte // the file as loaded, nil when missing; SaveConfig refuses if it changed
	err          error  // load error; fields are read-only until r reloads
	saved, draft app.Config
	exe          map[string]executable
	cursor       int
	editing      bool
	input        textinput.Model
	quit         bool // q pressed with unsaved changes; a second q quits
}

type executable struct {
	path string
	err  error
}

// field is one config value. Fields with options cycle with ←/→; every field
// accepts typed input. "" means the default.
type field struct {
	agent, key string // agent is empty for global fields
	options    []string
	get        func(app.Config) string
	set        func(*app.Config, string)
}

// Levels listed by each CLI's --help; grok lists none.
var efforts = map[string][]string{
	"codex":  {"minimal", "low", "medium", "high", "xhigh"},
	"claude": {"low", "medium", "high", "xhigh", "max"},
	"agy":    {"low", "medium", "high", "xhigh", "max"},
	"pi":     {"off", "minimal", "low", "medium", "high", "xhigh", "max"},
}

var fields = settingsFields()

func settingsFields() []field {
	fs := []field{
		{key: "agent", options: append([]string{""}, app.Agents...),
			get: func(c app.Config) string { return c.Agent }, set: func(c *app.Config, v string) { c.Agent = v }},
		{key: "timeout", options: []string{"", "30m", "1h", "2h", "4h"},
			get: func(c app.Config) string { return c.Timeout }, set: func(c *app.Config, v string) { c.Timeout = v }},
	}
	for _, name := range app.Agents {
		one := func(key string, options []string, p func(*app.AgentConfig) *string) field {
			if options != nil {
				options = append([]string{""}, options...)
			}
			return field{agent: name, key: key, options: options,
				get: func(c app.Config) string { a := c.Agents[name]; return *p(&a) },
				set: func(c *app.Config, v string) {
					a := c.Agents[name]
					*p(&a) = v
					// Copy on write: saved and draft may share the map.
					c.Agents = maps.Clone(c.Agents)
					if c.Agents == nil {
						c.Agents = map[string]app.AgentConfig{}
					}
					c.Agents[name] = a
				}}
		}
		fs = append(fs,
			one("model", nil, func(a *app.AgentConfig) *string { return &a.Model }),
			one("effort", efforts[name], func(a *app.AgentConfig) *string { return &a.Effort }),
			one("path", nil, func(a *app.AgentConfig) *string { return &a.Path }))
	}
	return fs
}

// check reuses Config.Validate on a config holding only this value.
func (f field) check(v string) error {
	var c app.Config
	f.set(&c, v)
	return c.Validate()
}

func (f field) title() string {
	if f.agent == "" {
		return f.key
	}
	return f.agent + " · " + f.key
}

func (f field) help() string {
	switch f.key {
	case "agent":
		return "baton do、ask 和 MCP 没指定 Agent 时用哪个。环境变量 BATON_AGENT 优先于这里。"
	case "timeout":
		return "单轮上限，包含 Agent 和检查时间。命令行 -timeout 优先。"
	case "model":
		return fmt.Sprintf("传给 %s 的模型名，原样传入，不校验。命令行 -model 优先；留空时用 %s 自己的配置。", f.agent, f.agent)
	case "effort":
		if f.options == nil {
			return fmt.Sprintf("推理强度。%s --help 没列出取值，请按 enter 输入。命令行 -effort 优先。", f.agent)
		}
		return fmt.Sprintf("推理强度，取值来自 %s --help，也可以按 enter 输入其他值。命令行 -effort 优先。", f.agent)
	}
	return fmt.Sprintf("可执行文件。留空时在 PATH 里找 %s（忽略 shell alias），写命令名也会在 PATH 里找。", f.agent)
}

func (s *settings) load() {
	cfg, path, raw, err := app.LoadConfig()
	s.loaded, s.path, s.raw, s.err = true, path, raw, err
	if err == nil {
		s.saved, s.draft = cfg, cfg
	}
	s.resolve()
}

// resolve looks up each agent's executable once per change instead of per frame.
func (s *settings) resolve() {
	s.exe = map[string]executable{}
	for _, name := range app.Agents {
		argv, err := agent.Argv(name, agent.Options{Path: s.draft.Agents[name].Path})
		if err == nil {
			s.exe[name] = executable{path: argv[0]}
		} else {
			s.exe[name] = executable{err: err}
		}
	}
}

func (s settings) changed() int {
	n := 0
	for _, f := range fields {
		if f.get(s.draft) != f.get(s.saved) {
			n++
		}
	}
	return n
}

func (m model) enterSettings() model {
	m.settingsMode, m.zoom = true, false
	if !m.set.loaded || m.set.changed() == 0 {
		m.set.load() // pick up outside edits unless that would drop a draft
	}
	m.viewport.GotoTop()
	return m
}

func (m *model) flash(text string) {
	m.notice, m.noticeUntil = text, m.now.Add(3*time.Second)
}

func (m model) settingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.set
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if s.editing {
		f := fields[s.cursor]
		switch key {
		case "enter":
			f.set(&s.draft, strings.TrimSpace(s.input.Value()))
			s.editing = false
			if f.key == "path" {
				s.resolve()
			}
		case "esc":
			s.editing = false
		default:
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			m.syncViewport()
			return m, cmd
		}
		m.syncViewport()
		return m, nil
	}
	if s.quit {
		s.quit = false
		if key == "q" {
			return m, tea.Quit
		}
		return m, nil
	}
	if s.err != nil && key != "r" && key != "esc" && key != "s" && key != "q" {
		return m, nil
	}
	f := fields[s.cursor]
	switch key {
	case "up", "k":
		m.moveField(-1)
	case "down", "j":
		m.moveField(1)
	case "left", "h", "right", "l":
		if f.options == nil {
			m.flash("这一项按 enter 输入")
			break
		}
		i := max(0, indexOf(f.options, f.get(s.draft)))
		step := 1
		if key == "left" || key == "h" {
			step = len(f.options) - 1
		}
		f.set(&s.draft, f.options[(i+step)%len(f.options)])
	case "enter":
		s.editing = true
		s.input = textinput.New()
		s.input.Prompt = ""
		s.input.Cursor.SetMode(cursor.CursorStatic)
		s.input.SetValue(f.get(s.draft))
		s.input.CursorEnd()
		s.input.Focus()
	case "d", "u":
		v := ""
		if key == "u" {
			v = f.get(s.saved)
		}
		f.set(&s.draft, v)
		if f.key == "path" {
			s.resolve()
		}
	case "ctrl+s":
		m.save()
	case "r":
		s.load()
		m.flash("已重新读取配置文件")
	case "[", "]", "=":
		ratio := 38
		if key == "[" {
			ratio = m.ratio - 5
		} else if key == "]" {
			ratio = m.ratio + 5
		}
		m.setRatio(ratio)
		m.saveRatio()
	case "pgup", "pgdown", "home", "end":
		m.viewport, _ = m.viewport.Update(msg)
		return m, nil
	case "esc", "s":
		m.settingsMode = false
		m.viewport.GotoTop()
		if n := s.changed(); n > 0 {
			m.flash(fmt.Sprintf("设置有 %d 项未保存，按 s 回去保存", n))
		}
	case "q":
		if s.changed() > 0 {
			s.quit = true
			break
		}
		return m, tea.Quit
	}
	m.syncViewport()
	return m, nil
}

func (m *model) moveField(d int) {
	if m.set.err == nil && !m.set.editing {
		m.set.cursor = max(0, min(len(fields)-1, m.set.cursor+d))
		m.viewport.GotoTop()
	}
}

func (m *model) save() {
	s := &m.set
	if s.changed() == 0 {
		m.flash("没有改动")
		return
	}
	for i, f := range fields {
		if f.check(f.get(s.draft)) != nil {
			s.cursor = i
			m.flash("✗ 这一项有误，改正后再保存")
			return
		}
	}
	raw, err := app.SaveConfig(s.draft, s.raw)
	switch {
	case errors.Is(err, app.ErrConfigChanged):
		m.flash("✗ 文件已在外部修改；按 r 重新读取（放弃未保存的改动）")
	case err != nil:
		m.flash("✗ 保存失败: " + err.Error())
	default:
		s.raw, s.saved = raw, s.draft
		m.flash("✓ 已保存 · 下次派活生效，已有对话不变")
	}
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// settingsLines is the list pane in settings mode, with each line's field
// index (-1 for group headers) for mouse hits.
func (m model) settingsLines(w int) ([]string, []int, int) {
	p, s := m.palette, m.set
	var lines []string
	var owners []int
	if s.err != nil {
		for _, l := range strings.Split(ansi.Wrap(clean(s.err.Error())+"\n修好后按 r 重新读取", max(1, w-2), ""), "\n") {
			lines, owners = append(lines, " "+p.text(p.bad).Render(l)), append(owners, -1)
		}
		return lines, owners, 0
	}
	selected, group := 0, "-"
	for i, f := range fields {
		if g := cmp.Or(f.agent, "全局"); g != group {
			group = g
			lines, owners = append(lines, p.text(p.faint).Render(fit(" "+strings.ToUpper(g), w))), append(owners, -1)
		}
		bar, bg := p.term, p.term
		if i == s.cursor {
			selected = len(lines)
			bar, bg = p.accent, p.sel
		}
		v := f.get(s.draft)
		val := m.fieldValue(f, v)
		if i == s.cursor && s.editing {
			in := s.input
			in.Width = max(1, w-15)
			val = in.View()
		} else if i == s.cursor && f.options != nil {
			val = p.text(p.accent).Render("‹ ") + val + p.text(p.accent).Render(" ›")
		}
		err, warn := f.check(v), m.fieldWarn(f, v)
		mark := ""
		switch {
		case err != nil:
			mark = p.text(p.bad).Render("✗")
		case v != f.get(s.saved):
			mark = p.text(p.wait).Render("●")
		case warn != "":
			mark = p.text(p.wait).Render("!")
		}
		stripe := p.text(bar).Background(bg).Render("▎")
		style := p.text(p.ink).Background(bg)
		left := stripe + "  " + p.text(p.dim).Render(fmt.Sprintf("%-8s", f.key)) + val
		lines, owners = append(lines, paint(rightRow(left, mark, max(1, w-1)), style, w)), append(owners, i)
		note, color := warn, p.wait
		if err != nil {
			note, color = err.Error(), p.bad
		}
		if note != "" {
			lines, owners = append(lines, paint(stripe+strings.Repeat(" ", 11)+p.text(color).Render(fit(note, max(1, w-13))), style, w)), append(owners, i)
		}
	}
	return lines, owners, selected
}

func (m model) fieldValue(f field, v string) string {
	p := m.palette
	if v != "" {
		return p.text(p.ink).Render(clean(v))
	}
	text := "默认（Agent 自己的配置）"
	switch f.key {
	case "agent":
		text = "默认（" + app.Config{}.DefaultAgent() + "）"
	case "timeout":
		text = "默认（" + shortDuration(app.DefaultTimeout) + "）"
	case "path":
		text = "默认 → " + cmp.Or(m.set.exe[f.agent].path, "PATH 中没有 "+f.agent)
	}
	return p.text(p.faint).Render(clean(text))
}

func (m model) fieldWarn(f field, v string) string {
	if f.key == "path" && v != "" && m.set.exe[f.agent].err != nil {
		return "未找到可执行文件"
	}
	return ""
}

func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// settingsDetail explains the selected field: options, the agent's command
// with the configured parts highlighted, and the file a save would write.
func (m model) settingsDetail(w int) string {
	p, s := m.palette, m.set
	if s.err != nil {
		return p.text(p.dim).Render("配置文件读取失败，见左栏。")
	}
	f := fields[s.cursor]
	v, old := f.get(s.draft), f.get(s.saved)
	label, color := "默认", p.off
	if v != old {
		label, color = "已修改", p.wait
	} else if v != "" {
		label, color = "已设置", p.ok
	}
	pill := p.text(color).Background(p.code).Bold(true).Padding(0, 1).Render(label)
	head := rightRow(p.text(p.ink).Bold(true).Render(f.title()), pill, max(1, w-1)) + "\n" + p.text(p.dim).Render(ansi.Wrap(f.help(), max(1, w-1), ""))
	if f.key == "agent" && os.Getenv("BATON_AGENT") != "" {
		head += "\n" + p.text(p.wait).Render(fit("当前 BATON_AGENT="+os.Getenv("BATON_AGENT")+"，这一项暂不生效", w-1))
	}
	if err := f.check(v); err != nil {
		head += "\n" + p.text(p.bad).Render(ansi.Wrap("✗ "+clean(err.Error()), max(1, w-1), ""))
	}
	lines := []string{p.box(head, color, max(1, w-1)), ""}
	section := func(title, body string) {
		lines = append(lines, p.text(p.faint).Render(title), body, "")
	}
	if f.options != nil {
		var chips []string
		for _, o := range f.options {
			chip := p.text(p.dim).Background(p.code).Render(" " + cmp.Or(o, "默认") + " ")
			if o == v {
				chip = p.text(p.term).Background(p.accent).Bold(true).Render(" " + cmp.Or(o, "默认") + " ")
			}
			chips = append(chips, chip)
		}
		section("取值（←→ 切换）", ansi.Wrap(strings.Join(chips, " "), w, ""))
	}
	if v != old {
		section("修改", p.text(p.faint).Strikethrough(true).Render(cmp.Or(clean(old), "默认"))+p.text(p.accent).Render(" → ")+
			p.text(p.ink).Bold(true).Render(cmp.Or(clean(v), "默认"))+p.text(p.faint).Render("  u 撤销"))
	}
	if f.agent != "" {
		section("命令预览（baton do，高亮为配置带来的参数）", m.preview(f.agent, w))
	}
	section("保存后的 config.json", m.configPreview(w))
	return strings.TrimSuffix(strings.Join(lines, "\n"), "\n")
}

// preview is the agent's real argv for a write turn, built by the agent package.
func (m model) preview(name string, w int) string {
	p, a := m.palette, m.set.draft.Agents[name]
	args, _ := agent.Args(name, agent.Options{Mode: "write", Dir: "<worktree>", Prompt: "<任务>", LastFile: "<last>", Model: a.Model, Effort: a.Effort})
	argv := append([]string{cmp.Or(m.set.exe[name].path, a.Path, name)}, args...)
	hot := make([]bool, len(argv))
	for i, arg := range argv {
		if (i == 0 && a.Path != "") || (a.Model != "" && arg == a.Model) || (a.Effort != "" && (arg == a.Effort || strings.HasSuffix(arg, "="+strconv.Quote(a.Effort)))) {
			hot[i] = true
			if i > 0 && strings.HasPrefix(argv[i-1], "-") {
				hot[i-1] = true
			}
		}
	}
	words := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsAny(arg, " \t") {
			arg = strconv.Quote(arg)
		}
		style := p.text(p.ink)
		switch {
		case hot[i]:
			style = p.text(p.accent).Bold(true)
		case strings.HasPrefix(arg, "<"):
			style = p.text(p.faint)
		}
		words[i] = style.Render(clean(arg))
	}
	return paint(ansi.Wrap(strings.Join(words, " "), max(1, w-2), ""), p.text(p.ink).Background(p.code), w)
}

var agentBlock = regexp.MustCompile(`^    "(\w+)": \{$`)

// configPreview marks lines a save would add or change. Lines are keyed by
// their agent block so equal lines under different agents still compare.
func (m model) configPreview(w int) string {
	p := m.palette
	keyed := func(c app.Config) []string {
		b, _ := c.Marshal()
		block := ""
		var out []string
		for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if g := agentBlock.FindStringSubmatch(line); g != nil {
				block = g[1]
			}
			out = append(out, block+"|"+line)
		}
		return out
	}
	old := map[string]bool{}
	for _, k := range keyed(m.set.saved) {
		old[k] = true
	}
	var out []string
	for _, k := range keyed(m.set.draft) {
		line, style := "  "+k[strings.IndexByte(k, '|')+1:], p.text(p.ink).Background(p.code)
		if !old[k] {
			line, style = "+"+line[1:], style.Foreground(p.ok).Background(p.addBG)
		}
		out = append(out, style.Width(w).Render(fit(line, w)))
	}
	return strings.Join(out, "\n")
}
