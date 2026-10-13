package tui

import (
	"baton/internal/agent"
	"baton/internal/app"
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// settings edits the user's config.json and roles in place of the task list;
// s toggles it. The draft outlives leaving the mode, so unsaved edits are
// never dropped silently.
type settings struct {
	loaded       bool
	path         string
	raw          []byte // the file as loaded, nil when missing; SaveConfig refuses if it changed
	err          error  // load error; fields are read-only until r reloads
	saved, draft values
	fields       []field // the config fields, the role name row, then the shown role's
	role         string  // the role shown below the name row
	pick         int     // the highlighted match while the name row is searched
	exe          map[string]executable
	cursor       int
	editing      bool
	input        textinput.Model
	quit         bool   // q pressed with unsaved changes; a second q quits
	shownTheme   string // the theme value the panel last applied, to preview edits live
}

// values is what the fields edit: config.json and the roles visible from the
// working directory, by name.
type values struct {
	app.Config
	Roles map[string]app.Role
}

// roleAgent is the agent a role runs on.
func (v values) roleAgent(name string) string {
	return cmp.Or(v.Roles[name].Agent, v.DefaultAgent())
}

type executable struct {
	path string
	err  error
}

// field is one config or role value. Fields with options cycle with ←/→;
// every field accepts typed input. "" means the default.
type field struct {
	agent, role, key string // both empty for global fields
	label            string // shorter list label than key, which the title and file use
	group            string // list heading when not an agent's; default 全局
	options          []string
	get              func(values) string
	set              func(*values, string) // nil for the role name row and a role's file
}

// Levels listed by each CLI's --help; grok lists none.
var efforts = map[string][]string{
	"codex":  {"minimal", "low", "medium", "high", "xhigh"},
	"claude": {"low", "medium", "high", "xhigh", "max"},
	"agy":    {"low", "medium", "high", "xhigh", "max"},
	"pi":     {"off", "minimal", "low", "medium", "high", "xhigh", "max"},
}

var configFields = settingsFields()

func settingsFields() []field {
	fs := []field{
		{key: "agent", options: append([]string{""}, app.Agents...),
			get: func(c values) string { return c.Agent }, set: func(c *values, v string) { c.Agent = v }},
		{key: "timeout", options: []string{"", "30m", "1h", "2h", "4h"},
			get: func(c values) string { return c.Timeout }, set: func(c *values, v string) { c.Timeout = v }},
		{key: "max_parallel", label: "parallel", options: []string{"", "1", "2", "4", "8"},
			get: func(c values) string {
				return map[bool]string{true: strconv.Itoa(c.MaxParallel)}[c.MaxParallel != 0]
			},
			set: func(c *values, v string) {
				n, err := strconv.Atoi(v)
				if err != nil && v != "" {
					n = -1 // Validate rejects it, so a typo is flagged instead of dropped
				}
				c.MaxParallel = n
			}},
		{group: "面板", key: "theme", options: []string{"", "light", "dark"},
			get: func(c values) string { return c.Theme }, set: func(c *values, v string) { c.Theme = v }},
	}
	for _, name := range app.Agents {
		update := func(c *values, edit func(*app.AgentConfig)) {
			a := c.Agents[name]
			edit(&a)
			// Copy on write: saved and draft may share the map.
			c.Agents = maps.Clone(c.Agents)
			if c.Agents == nil {
				c.Agents = map[string]app.AgentConfig{}
			}
			c.Agents[name] = a
		}
		one := func(key string, options []string, p func(*app.AgentConfig) *string) field {
			if options != nil {
				options = append([]string{""}, options...)
			}
			return field{agent: name, key: key, options: options,
				get: func(c values) string { a := c.Agents[name]; return *p(&a) },
				set: func(c *values, v string) { update(c, func(a *app.AgentConfig) { *p(a) = v }) }}
		}
		fs = append(fs,
			one("model", nil, func(a *app.AgentConfig) *string { return &a.Model }),
			one("effort", efforts[name], func(a *app.AgentConfig) *string { return &a.Effort }),
			one("path", nil, func(a *app.AgentConfig) *string { return &a.Path }),
			field{agent: name, key: "hidden", options: []string{"", "true"},
				get: func(c values) string { return map[bool]string{true: "true"}[c.Agents[name].Hidden] },
				set: func(c *values, v string) { update(c, func(a *app.AgentConfig) { a.Hidden = v == "true" }) }})
	}
	return fs
}

// roleFields edit a role's front matter, then open its file in an editor; a
// role whose file is broken only has the file.
func roleFields(d values, name string) []field {
	file := field{role: name, key: "file", group: "角色", get: func(v values) string { return v.Roles[name].Path }}
	if d.Roles[name].Err != nil {
		return []field{file}
	}
	update := func(v *values, edit func(*app.Role)) {
		r := v.Roles[name]
		edit(&r)
		v.Roles = maps.Clone(v.Roles) // copy on write, as for agents
		if v.Roles == nil {
			v.Roles = map[string]app.Role{}
		}
		v.Roles[name] = r
	}
	one := func(key, label string, options []string, p func(*app.Role) *string) field {
		return field{role: name, key: key, label: label, group: "角色", options: options,
			get: func(v values) string { r := v.Roles[name]; return *p(&r) },
			set: func(v *values, s string) { update(v, func(r *app.Role) { *p(r) = s }) }}
	}
	return []field{
		one("description", "desc", nil, func(r *app.Role) *string { return &r.Description }),
		one("agent", "", append([]string{""}, app.Agents...), func(r *app.Role) *string { return &r.Agent }),
		one("model", "", nil, func(r *app.Role) *string { return &r.Model }),
		one("effort", "", nil, func(r *app.Role) *string { return &r.Effort }),
		one("mode", "", []string{"", "read", "write"}, func(r *app.Role) *string { return &r.Mode }),
		{role: name, key: "hidden", group: "角色", options: []string{"", "true"},
			get: func(v values) string { return map[bool]string{true: "true"}[v.Roles[name].Hidden] },
			set: func(v *values, s string) { update(v, func(r *app.Role) { r.Hidden = s == "true" }) }},
		file,
	}
}

// roleNames are the draft's roles, sorted.
func (v values) roleNames() []string { return slices.Sorted(maps.Keys(v.Roles)) }

// rank keeps the names containing q, ignoring case: those starting with it
// first, then those with a hyphenated word starting with it, then the rest,
// each in the given order. Same ranking as rag-go's model search.
func rank(names []string, q string) []string {
	q = strings.ToLower(q)
	var tiers [3][]string
	for _, n := range names {
		l := strings.ToLower(n)
		switch {
		case strings.HasPrefix(l, q):
			tiers[0] = append(tiers[0], n)
		case strings.Contains(l, "-"+q):
			tiers[1] = append(tiers[1], n)
		case strings.Contains(l, q):
			tiers[2] = append(tiers[2], n)
		}
	}
	return slices.Concat(tiers[:]...)
}

// matches are what enter can open while the name row is searched: every role
// when nothing is typed, else the ranked names, then the typed name to create
// a role when none has it.
func (s settings) matches() []string {
	q := strings.TrimSpace(s.input.Value())
	if q == "" {
		return s.draft.roleNames()
	}
	out := rank(s.draft.roleNames(), q)
	if !slices.Contains(out, q) {
		out = append(out, q)
	}
	return out
}

// choices are the values ←/→ cycle through; a role's effort follows its agent.
func (f field) choices(d values) []string {
	if f.key == "name" {
		return d.roleNames()
	}
	if f.role != "" && f.key == "effort" && efforts[d.roleAgent(f.role)] != nil {
		return append([]string{""}, efforts[d.roleAgent(f.role)]...)
	}
	return f.options
}

// check reuses Config.Validate on a config holding only this value, and
// Role.Set for a role's.
func (f field) check(v string) error {
	if f.set == nil {
		return nil
	}
	if f.role != "" {
		r := app.Role{Name: f.role}
		return r.Set(f.key, v)
	}
	var c values
	f.set(&c, v)
	return c.Validate()
}

func (f field) title() string {
	switch {
	case f.key == "name":
		return "角色"
	case f.role != "":
		return "角色 " + f.role + " · " + f.key
	case f.agent != "":
		return f.agent + " · " + f.key
	}
	return f.key
}

func (f field) help(d values) string {
	if f.key == "name" {
		return "enter 后输入名称检索角色（前缀优先，再按 - 分隔的词，再到包含），↑↓ 选择，enter 打开；输入还没有的名称再 enter 新建全局角色。←→ 依次切换。下面只显示当前角色，各角色的改动都会保留，ctrl+s 一起保存。"
	}
	if f.role != "" {
		agent := d.roleAgent(f.role)
		switch f.key {
		case "description":
			return "必填。主 Agent 靠它挑角色：写清楚这个角色做什么、该用读还是写派、任务里要写什么。"
		case "agent":
			return "角色跑在哪个 Agent 上，留空时用全局 agent（现在是 " + d.DefaultAgent() + "）。"
		case "model", "effort":
			return fmt.Sprintf("传给 %s 的 %s。命令行 -%s 优先；留空时用 %s 的 %s 设置。", agent, f.key, f.key, agent, f.key)
		case "mode":
			return "read 只能派只读任务，write 只能派写任务；留空两种都行，没指定时按写任务。"
		case "hidden":
			return "true 时主 Agent 看不到这个角色：baton_agents 不列出，MCP 拒绝派给它。你在终端里用 CLI 不受影响。"
		}
		return "角色文件。enter 用 $VISUAL 或 $EDITOR（默认 vi）打开，改正文：第 1 轮放在任务前面的说明。编辑器退出后重新读取。新角色先 ctrl+s 保存。"
	}
	switch f.key {
	case "agent":
		return "baton do、ask 和 MCP 没指定 Agent 时用哪个。环境变量 BATON_AGENT 优先于这里。"
	case "timeout":
		return "单轮上限，包含 Agent 和检查时间。命令行 -timeout 优先。"
	case "max_parallel":
		return "同一个 BATON_HOME 下最多同时运行几轮任务（Agent 加检查），多出的保持排队，有空位再开始。派活时记下，之后改只影响新派的任务。"
	case "theme":
		return "面板配色。默认跟随终端背景；切换时立即预览，ctrl+s 保存后下次打开也用它。命令行 -theme 优先。"
	case "model":
		return fmt.Sprintf("传给 %s 的模型名，原样传入，不校验。命令行 -model 优先；留空时用 %s 自己的配置。", f.agent, f.agent)
	case "hidden":
		return fmt.Sprintf("true 时主 Agent 看不到 %s：baton_agents 不列出，MCP 的 baton_run、baton_reply 拒绝它。你在终端里用 CLI 不受影响。", f.agent)
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
	s.loaded, s.path, s.raw, s.err, s.fields = true, path, raw, err, configFields
	if err == nil {
		roles := map[string]app.Role{}
		for _, r := range app.Roles(".") {
			roles[r.Name] = r
		}
		s.saved = values{cfg, roles}
		s.draft = s.saved
		s.refields()
	}
	s.cursor = min(s.cursor, len(s.fields)-1)
	s.resolve()
}

// refields shows the selected role, or the first when it is gone, after a
// role was picked, added, dropped or reread.
func (s *settings) refields() {
	names := s.draft.roleNames()
	if !slices.Contains(names, s.role) {
		s.role = ""
		if len(names) > 0 {
			s.role = names[0]
		}
	}
	role := s.role
	s.fields = append(slices.Clone(configFields), field{key: "name", role: role, group: "角色", get: func(values) string { return role }})
	if role != "" {
		s.fields = append(s.fields, roleFields(s.draft, role)...)
	}
	s.cursor = min(s.cursor, len(s.fields)-1)
}

// all is every field, the roles not shown included, for saving and counting changes.
func (s settings) all() []field {
	fs := slices.Clone(configFields)
	for _, name := range s.draft.roleNames() {
		fs = append(fs, roleFields(s.draft, name)...)
	}
	return fs
}

// fieldAt is the index of a role's field, or -1.
func (s settings) fieldAt(role, key string) int {
	return slices.IndexFunc(s.fields, func(f field) bool { return f.role == role && f.key == key })
}

// reloadRole rereads one role after the editor, keeping the other drafts.
func (s *settings) reloadRole(name string) {
	for _, v := range []*values{&s.saved, &s.draft} {
		v.Roles = maps.Clone(v.Roles)
		delete(v.Roles, name)
	}
	for _, r := range app.Roles(".") {
		if r.Name == name {
			s.saved.Roles[name], s.draft.Roles[name] = r, r
		}
	}
	s.role = name
	s.refields()
	s.cursor = max(0, s.fieldAt(name, "file"))
}

// roleSet is a role's changed front matter, as SaveRole takes it.
func (s settings) roleSet(name string) [][2]string {
	return slices.DeleteFunc(s.pending()[name], func(kv [2]string) bool { return kv[0] == "file" })
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

// pending is the changed values by owner: "" for config.json, else a role.
// A new role's file counts, so an unsaved role is never dropped silently.
func (s settings) pending() map[string][][2]string {
	out := map[string][][2]string{}
	for _, f := range s.all() {
		if v := f.get(s.draft); v != f.get(s.saved) {
			out[f.role] = append(out[f.role], [2]string{f.key, v})
		}
	}
	return out
}

func (s settings) changed() int {
	n := 0
	for _, kv := range s.pending() {
		n += len(kv)
	}
	return n
}

type editedMsg struct {
	name string
	err  error
}

// editRole opens a saved role's file in $VISUAL or $EDITOR, vi by default.
func (m *model) editRole(name string) tea.Cmd {
	r, ok := m.set.saved.Roles[name]
	if !ok || m.set.pending()[name] != nil {
		m.flash("先 ctrl+s 保存这个角色，再打开文件")
		return nil
	}
	editor := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	return tea.ExecProcess(exec.Command(editor[0], append(editor[1:], r.Path)...), func(err error) tea.Msg { return editedMsg{name, err} })
}

// addRole adds a global role to the draft; ctrl+s creates its file.
func (m *model) addRole(name string) error {
	s := &m.set
	if err := app.CheckRoleName(name); err != nil {
		return err
	}
	if _, ok := s.draft.Roles[name]; ok {
		return fmt.Errorf("已有角色 %s", name)
	}
	s.draft.Roles = maps.Clone(s.draft.Roles)
	if s.draft.Roles == nil {
		s.draft.Roles = map[string]app.Role{}
	}
	s.draft.Roles[name] = app.Role{Name: name, Path: filepath.Join(filepath.Dir(s.path), "agents", name+".md")}
	s.role = name
	s.refields()
	s.cursor = s.fieldAt(name, "description")
	m.flash("填好 description 后 ctrl+s 保存；在 file 一项按 d 放弃")
	return nil
}

func (m model) enterSettings() model {
	m.settingsMode, m.zoom = true, false
	if !m.set.loaded {
		m.set.load()
		m.set.shownTheme = m.set.draft.Theme // keep whatever theme the panel opened with
	} else if m.set.changed() == 0 {
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
	if s.editing && s.fields[s.cursor].key == "name" {
		return m.searchKey(msg)
	}
	if s.editing {
		f := s.fields[s.cursor]
		switch key {
		case "enter":
			f.set(&s.draft, strings.TrimSpace(s.input.Value()))
			s.editing = false
			if f.key == "path" {
				s.resolve()
			}
			m.previewTheme()
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
	f := s.fields[s.cursor]
	switch key {
	case "up", "k":
		m.moveField(-1)
	case "down", "j":
		m.moveField(1)
	case "left", "h", "right", "l":
		options := f.choices(s.draft)
		if f.key == "file" {
			m.flash("这一项按 enter 用编辑器打开")
			break
		}
		if options == nil {
			m.flash("这一项按 enter 输入")
			break
		}
		i := max(0, indexOf(options, f.get(s.draft)))
		step := 1
		if key == "left" || key == "h" {
			step = len(options) - 1
		}
		if f.key == "name" {
			s.role = options[(i+step)%len(options)]
			s.refields()
			break
		}
		f.set(&s.draft, options[(i+step)%len(options)])
	case "enter":
		if f.key == "file" {
			return m, m.editRole(f.role)
		}
		s.editing = true
		s.input = textinput.New()
		s.input.Prompt = ""
		s.input.Cursor.SetMode(cursor.CursorStatic)
		if f.key == "name" {
			// The search starts empty, listing every role from the shown one.
			s.input.Placeholder, s.pick = s.role, max(0, indexOf(s.draft.roleNames(), s.role))
		} else {
			s.input.SetValue(f.get(s.draft))
		}
		s.input.CursorEnd()
		s.input.Focus()
	case "d", "u":
		if f.key == "name" {
			break
		}
		if f.set == nil {
			// Only a new role's file differs; dropping it drops the role.
			if _, saved := s.saved.Roles[f.role]; !saved {
				s.draft.Roles = maps.Clone(s.draft.Roles)
				delete(s.draft.Roles, f.role)
				s.refields()
				s.cursor = min(s.cursor, len(s.fields)-1)
			}
			break
		}
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
	m.previewTheme()
	m.syncViewport()
	return m, nil
}

// previewTheme repaints the panel when the theme value changes, saved or not.
// searchKey handles keys while the name row is searched.
func (m model) searchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.set
	matches := s.matches()
	switch msg.String() {
	case "up", "down":
		if n := len(matches); n > 0 {
			s.pick = (s.pick + map[string]int{"up": n - 1, "down": 1}[msg.String()]) % n
		}
	case "enter":
		if len(matches) > 0 {
			name := matches[min(s.pick, len(matches)-1)]
			if _, ok := s.draft.Roles[name]; ok {
				s.role = name
				s.refields()
			} else if err := m.addRole(name); err != nil {
				m.flash("✗ " + err.Error())
				return m, nil
			}
		}
		s.editing = false
	case "esc":
		s.editing = false
	default:
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		s.pick = 0
		m.syncViewport()
		return m, cmd
	}
	m.syncViewport()
	return m, nil
}

func (m *model) previewTheme() {
	if v := m.set.draft.Theme; v != m.set.shownTheme && (app.Config{Theme: v}).Validate() == nil {
		m.set.shownTheme = v
		m.useTheme(v)
	}
}

func (m *model) moveField(d int) {
	if m.set.err == nil && !m.set.editing {
		m.set.cursor = max(0, min(len(m.set.fields)-1, m.set.cursor+d))
		m.viewport.GotoTop()
	}
}

// save writes config.json and each changed role file, stopping at the first
// failure; what was written stays saved.
func (m *model) save() {
	s := &m.set
	pending := s.pending()
	if len(pending) == 0 {
		m.flash("没有改动")
		return
	}
	for i, f := range s.all() {
		if f.check(f.get(s.draft)) != nil {
			if f.role != "" {
				s.role = f.role
				s.refields()
				i = s.fieldAt(f.role, f.key)
			}
			s.cursor = i
			m.flash("✗ 这一项有误，改正后再保存")
			return
		}
	}
	var err error
	if pending[""] != nil {
		var raw []byte
		if raw, err = app.SaveConfig(s.draft.Config, s.raw); err == nil {
			s.raw, s.saved.Config = raw, s.draft.Config
		}
	}
	for _, name := range slices.Sorted(maps.Keys(pending)) {
		if name == "" || err != nil {
			continue
		}
		r := s.draft.Roles[name]
		if r.Raw, err = app.SaveRole(r.Path, s.saved.Roles[name].Raw, s.roleSet(name)); err == nil {
			s.saved.Roles = maps.Clone(s.saved.Roles)
			s.saved.Roles[name] = r
		}
	}
	switch {
	case errors.Is(err, app.ErrConfigChanged):
		m.flash("✗ 文件已在外部修改；按 r 重新读取（放弃未保存的改动）")
	case err != nil:
		m.flash("✗ 保存失败: " + err.Error())
	default:
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
	for i, f := range s.fields {
		if g := cmp.Or(f.agent, f.group, "全局"); g != group {
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
			in.Width = max(1, w-16)
			val = in.View()
		} else if i == s.cursor && f.choices(s.draft) != nil {
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
		left := stripe + "  " + p.text(p.dim).Render(fmt.Sprintf("%-9s", cmp.Or(f.label, f.key))) + val
		lines, owners = append(lines, paint(rightRow(left, mark, max(1, w-1)), style, w)), append(owners, i)
		note, color := warn, p.wait
		if err != nil {
			note, color = err.Error(), p.bad
		}
		if note != "" {
			lines, owners = append(lines, paint(stripe+strings.Repeat(" ", 12)+p.text(color).Render(fit(note, max(1, w-14))), style, w)), append(owners, i)
		}
		if i == s.cursor && s.editing && f.key == "name" {
			for _, l := range m.matchLines(max(1, w-14)) {
				lines, owners = append(lines, paint(stripe+strings.Repeat(" ", 12)+l, style, w)), append(owners, i)
			}
			selected = len(lines) - 1 // keep the matches in view
		}
	}
	return lines, owners, selected
}

// matchLines are the search's matches around the highlighted one, with each
// role's description or a note that enter creates it.
func (m model) matchLines(w int) []string {
	p, s := m.palette, m.set
	matches := s.matches()
	if len(matches) == 0 {
		return []string{p.text(p.faint).Render(fit("还没有角色，输入名称新建", w))}
	}
	var out []string
	start := max(0, min(s.pick-3, len(matches)-8))
	for i := start; i < min(len(matches), start+8); i++ {
		lead, style := "  ", p.text(p.ink)
		if i == s.pick {
			lead, style = "> ", p.text(p.accent).Bold(true)
		}
		note := "新建全局角色"
		if r, ok := s.draft.Roles[matches[i]]; ok {
			note = cmp.Or(r.Description, "未填写 description")
		}
		out = append(out, fitANSI(style.Render(lead+clean(matches[i]))+p.text(p.faint).Render("  "+clean(note)), w))
	}
	return out
}

func (m model) fieldValue(f field, v string) string {
	p := m.palette
	if f.key == "hidden" {
		if v == "true" {
			return p.text(p.ink).Render("隐藏（主 Agent 看不到）")
		}
		return p.text(p.faint).Render("默认（暴露给主 Agent）")
	}
	if f.role != "" || f.key == "name" {
		return m.roleValue(f, v)
	}
	if v != "" {
		return p.text(p.ink).Render(clean(v))
	}
	text := "默认（Agent 自己的配置）"
	switch f.key {
	case "agent":
		text = "默认（" + app.Config{}.DefaultAgent() + "）"
	case "timeout":
		text = "默认（" + shortDuration(app.DefaultTimeout) + "）"
	case "max_parallel":
		text = "默认（不限）"
	case "theme":
		text = "默认（跟随终端，当前" + map[bool]string{true: "深色", false: "浅色"}[m.darkTerminal] + "）"
	case "path":
		text = "默认 → " + cmp.Or(m.set.exe[f.agent].path, "PATH 中没有 "+f.agent)
	}
	return p.text(p.faint).Render(clean(text))
}

// roleValue shows a role's value, or what an empty one falls back to.
func (m model) roleValue(f field, v string) string {
	p, d := m.palette, m.set.draft
	r, agent := d.Roles[f.role], d.roleAgent(f.role)
	_, saved := m.set.saved.Roles[f.role]
	text := ""
	switch {
	case f.key == "name" && f.role == "":
		text = "还没有角色 · enter 输入名称新建"
	case f.key == "name":
		names, where := d.roleNames(), "全局"
		if filepath.Dir(r.Path) != filepath.Join(filepath.Dir(m.set.path), "agents") {
			where = "仓库"
		}
		return p.text(p.ink).Render(clean(f.role)) + p.text(p.faint).Render(fmt.Sprintf("  %d/%d · %s", indexOf(names, f.role)+1, len(names), where))
	case f.key == "file" && r.Err != nil:
		text = "文件有误 · enter 编辑"
	case f.key == "file" && !saved:
		text = "新角色 · ctrl+s 创建文件"
	case f.key == "file" && r.Prompt == "":
		text = "正文为空 · enter 编辑"
	case f.key == "file":
		text = fmt.Sprintf("正文 %d 行 · enter 编辑", strings.Count(r.Prompt, "\n")+1)
	case v != "":
		return p.text(p.ink).Render(clean(v))
	case f.key == "description":
		text = "未填写"
	case f.key == "agent":
		text = "默认（" + agent + "）"
	case f.key == "model":
		text = "默认（" + cmp.Or(d.Agents[agent].Model, agent+" 自己的配置") + "）"
	case f.key == "effort":
		text = "默认（" + cmp.Or(d.Agents[agent].Effort, agent+" 自己的配置") + "）"
	case f.key == "mode":
		text = "默认（读写都可）"
	}
	return p.text(p.faint).Render(clean(text))
}

func (m model) fieldWarn(f field, v string) string {
	if f.key == "path" && v != "" && m.set.exe[f.agent].err != nil {
		return "未找到可执行文件"
	}
	if r := m.set.draft.Roles[f.role]; f.key == "file" && r.Err != nil {
		return strings.TrimPrefix(r.Err.Error(), r.Path+": ")
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
	f := s.fields[s.cursor]
	v, old := f.get(s.draft), f.get(s.saved)
	label, color := "默认", p.off
	if v != old {
		label, color = "已修改", p.wait
	} else if v != "" {
		label, color = "已设置", p.ok
	}
	pill := p.text(color).Background(p.code).Bold(true).Padding(0, 1).Render(label)
	head := rightRow(p.text(p.ink).Bold(true).Render(f.title()), pill, max(1, w-1)) + "\n" + p.text(p.dim).Render(ansi.Wrap(f.help(s.draft), max(1, w-1), ""))
	if f.key == "agent" && os.Getenv("BATON_AGENT") != "" {
		head += "\n" + p.text(p.wait).Render(fit("当前 BATON_AGENT="+os.Getenv("BATON_AGENT")+"，这一项暂不生效", w-1))
	}
	if err := f.check(v); err != nil {
		head += "\n" + p.text(p.bad).Render(ansi.Wrap("✗ "+clean(err.Error()), max(1, w-1), ""))
	} else if warn := m.fieldWarn(f, v); warn != "" {
		head += "\n" + p.text(p.wait).Render(ansi.Wrap("! "+clean(warn), max(1, w-1), ""))
	}
	lines := []string{p.box(head, color, max(1, w-1)), ""}
	section := func(title, body string) {
		lines = append(lines, p.text(p.faint).Render(title), body, "")
	}
	if options := f.choices(s.draft); options != nil && f.key != "name" {
		var chips []string
		for _, o := range options {
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
	r := s.draft.Roles[f.role]
	switch {
	case f.key == "name":
		section(fmt.Sprintf("全部角色（%d）", len(s.draft.Roles)), m.roleOverview(w))
	case f.agent != "":
		section("命令预览（baton do，高亮为配置带来的参数）", m.preview(f.agent, "write", s.draft.Agents[f.agent], w))
		section("保存后的 config.json", m.configPreview(w))
	case f.role == "":
		section("保存后的 config.json", m.configPreview(w))
	case r.Err == nil:
		name, a := s.draft.roleAgent(f.role), s.draft.Agents[s.draft.roleAgent(f.role)]
		a.Model, a.Effort = cmp.Or(r.Model, a.Model), cmp.Or(r.Effort, a.Effort)
		section("命令预览（baton do "+f.role+"，高亮为配置和角色带来的参数）", m.preview(name, cmp.Or(r.Mode, "write"), a, w))
		section("保存后的 "+r.Path, m.rolePreview(f.role, w))
		if f.key == "file" {
			section("正文（第 1 轮放在任务前面）", p.text(p.ink).Render(ansi.Wrap(clean(cmp.Or(r.Prompt, "（空）")), max(1, w-1), "")))
		}
	}
	return strings.TrimSuffix(strings.Join(lines, "\n"), "\n")
}

// roleOverview lists every role with its agent, mode and description,
// marking unsaved and broken ones.
func (m model) roleOverview(w int) string {
	p, s := m.palette, m.set
	pending := s.pending()
	var out []string
	for _, name := range s.draft.roleNames() {
		r := s.draft.Roles[name]
		mark, meta := p.text(p.faint).Render("  "), cmp.Or(r.Agent, s.draft.DefaultAgent())+" · "+map[string]string{"": "读写", "read": "只读", "write": "只写"}[r.Mode]+" · "+r.Description
		switch {
		case pending[name] != nil:
			mark = p.text(p.wait).Render("● ")
		case r.Err != nil:
			mark, meta = p.text(p.bad).Render("! "), "文件有误"
		}
		style := p.text(p.ink)
		if name == s.role {
			style = style.Bold(true)
		}
		out = append(out, fitANSI(mark+style.Render(clean(name))+p.text(p.faint).Render("  "+clean(meta)), max(1, w-1)))
	}
	if len(out) == 0 {
		return p.text(p.faint).Render("还没有角色")
	}
	return strings.Join(out, "\n")
}

// preview is the agent's real argv for a turn, built by the agent package.
func (m model) preview(name, mode string, a app.AgentConfig, w int) string {
	p := m.palette
	args, _ := agent.Args(name, agent.Options{Mode: mode, Dir: "<worktree>", Prompt: "<任务>", LastFile: "<last>", Model: a.Model, Effort: a.Effort})
	argv := append([]string{cmp.Or(m.set.exe[name].path, a.Path, name)}, args...)
	if agent.PromptOnStdin(name) {
		argv = append(argv, "<", "<任务>")
	}
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
	return m.added(keyed(m.set.saved.Config), keyed(m.set.draft.Config), w)
}

// rolePreview is the front matter a save would write, changed lines marked.
func (m model) rolePreview(name string, w int) string {
	keyed := func(b []byte) []string {
		lines := strings.Split(string(b), "\n")
		if end := slices.Index(lines[1:], "---"); end >= 0 {
			lines = lines[:end+2]
		}
		for i := range lines {
			lines[i] = "|" + lines[i]
		}
		return lines
	}
	old := m.set.saved.Roles[name].Raw
	return m.added(keyed(old), keyed(app.RoleFile(old, m.set.roleSet(name))), w)
}

// added shows lines, marking those not in old; text before the first | is
// a key that tells equal lines in different blocks apart.
func (m model) added(old, lines []string, w int) string {
	p := m.palette
	seen := map[string]bool{}
	for _, k := range old {
		seen[k] = true
	}
	var out []string
	for _, k := range lines {
		line, style := "  "+k[strings.IndexByte(k, '|')+1:], p.text(p.ink).Background(p.code)
		if !seen[k] {
			line, style = "+"+line[1:], style.Foreground(p.ok).Background(p.addBG)
		}
		out = append(out, style.Width(w).Render(fit(line, w)))
	}
	return strings.Join(out, "\n")
}
