package main

import (
	"baton/internal/agent"
	"baton/internal/app"
	"baton/internal/mcpserver"
	"baton/internal/tui"
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"
)

// version is set by the release build: -ldflags "-X main.version=...".
var version = "dev"

const usage = `用法：
  baton                          打开面板（同 baton tui）
  baton do [agent|角色] <任务>    派出改代码的任务，默认 codex（可在配置中修改）
  baton ask [agent|角色] <问题>   派出只读任务
  baton reply [任务] <消息>       在同一会话里继续
  baton wait [任务…] [-any]      等任务结束；写多个时等全部，-any 等到第一个
  baton result|status|cancel [任务]
  baton agents                   列出 Agent 和角色（.baton/agents/、~/.config/baton/agents/ 下的 *.md）
  baton sessions [-repo 路径|-all]  列出仓库里任务的会话记录：运行目录、Agent 自己的会话文件、Baton 存的每轮输出
  baton export [任务…] [-all|-group 名字]  把对话导出为 Markdown，写到仓库的 .baton/history/；-all 导出当前仓库全部任务
  baton clean [任务…] [-all] [-force]  删除已结束的任务（worktree、分支和记录），在终端里确认后执行；默认当前仓库，
                                 写任务的分支须已合并，-force 连未合并的一起删
  baton list | tui | mcp | version
  baton config                   终端里打开设置面板；管道或 -json 时打印配置

<任务> 可以是文字、@文件，或 - 从标准输入读；[任务] 默认最近一个，可写 ID 的前缀或后缀。
常用参数（可写在任意位置）：-w 等完成并打印结果  -check 命令  -no-check  -title 标题
  -repo 路径（默认当前目录）  -model  -effort  -timeout（默认 2h）  -json
  -base 任务：接着另一个写任务做（do 从它的分支继续，ask 在它的 worktree 里只读）
  -group 名字：给一起完成同一目标的任务打标签，baton list -group 名字 只看这一组
各 Agent 的默认模型、effort、可执行文件写在 ~/.config/baton/config.json，baton config 查看。`

type checks []string

func (c *checks) String() string         { return fmt.Sprint([]string(*c)) }
func (c *checks) Set(value string) error { *c = append(*c, value); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "baton:", err)
		os.Exit(1)
	}
}

func emit(value any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(value)
}

// parse lets flags appear anywhere among positional arguments; "--" ends flags.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return append(pos, tail...), nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// splitAgent takes a leading agent or role name when more text follows it.
func splitAgent(pos []string, repo string) (string, []string) {
	if len(pos) < 2 {
		return "", pos
	}
	names := slices.Clone(app.Agents)
	for _, r := range app.Roles(repo) {
		names = append(names, r.Name)
	}
	if slices.Contains(names, pos[0]) {
		return pos[0], pos[1:]
	}
	return "", pos
}

// text joins positional words; "-" or no words with piped stdin reads stdin.
func text(pos []string, stdin io.Reader, piped bool) (string, error) {
	if (len(pos) == 1 && pos[0] == "-") || (len(pos) == 0 && piped) {
		b, err := io.ReadAll(stdin)
		return string(b), err
	}
	if len(pos) == 0 {
		return "", fmt.Errorf("missing task text\n\n%s", usage)
	}
	return strings.Join(pos, " "), nil
}

// terminal is a real TTY; /dev/null is a character device too.
func terminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func stdinPiped() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice == 0
}

func run(args []string) error {
	if len(args) == 0 {
		args = []string{"tui"}
	}
	switch args[0] {
	case "version", "-v", "--version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	case "mcp":
		return mcpserver.Serve(version)
	}
	svc, err := app.New()
	if err != nil {
		return err
	}
	cmd := args[0]
	fs := flag.NewFlagSet("baton "+cmd, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	switch cmd {
	case "_worker":
		if len(args) != 2 {
			return fmt.Errorf("worker requires task id")
		}
		return svc.Worker(args[1])
	case "tui":
		theme := fs.String("theme", "", "auto|light|dark (default: config, then auto)")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		return tui.Run(svc, *theme, false)
	case "do", "ask", "run":
		var req app.RunRequest
		var commands checks
		fs.StringVar(&req.Agent, "agent", "", "codex|claude|grok|agy|pi or a role (default: BATON_AGENT, config, then codex)")
		fs.StringVar(&req.Repo, "repo", "", "git repository (default: current directory)")
		fs.StringVar(&req.Task, "task", "", "text or @file")
		fs.StringVar(&req.Base, "base", "", "finished write task to continue from")
		fs.StringVar(&req.Group, "group", "", "label for tasks working on one goal")
		fs.StringVar(&req.Title, "title", "", "conversation title (inferred from task when omitted)")
		fs.StringVar(&req.Model, "model", "", "model (default: Baton config, then the agent's own config)")
		fs.StringVar(&req.Effort, "effort", "", "reasoning effort (default: Baton config, then the agent's own config)")
		fs.StringVar(&req.Timeout, "timeout", "", "turn timeout, including checks (default: config, then 2h)")
		fs.Var(&commands, "check", "check command (repeatable; default: detected from the project)")
		fs.BoolVar(&req.NoChecks, "no-check", false, "run no checks")
		wait := fs.Bool("w", false, "wait for the task and print its result")
		req.Mode = map[string]string{"do": "write", "ask": "read"}[cmd]
		if cmd == "run" {
			fs.StringVar(&req.Mode, "mode", "", "read|write (default: the role's mode, then write)")
		}
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		agent, pos := splitAgent(pos, cmp.Or(req.Repo, "."))
		if req.Agent == "" {
			req.Agent = agent
		} else if agent != "" {
			pos = append([]string{agent}, pos...)
		}
		if req.Task == "" {
			if req.Task, err = text(pos, os.Stdin, stdinPiped()); err != nil {
				return err
			}
		} else if len(pos) > 0 {
			return fmt.Errorf("task given twice: -task and %q", strings.Join(pos, " "))
		}
		if commands != nil {
			req.Checks = commands
		}
		sub, err := svc.Run(req)
		if err != nil {
			return err
		}
		return submitted(svc, sub.ID, *wait, *asJSON)
	case "reply":
		var msg, timeout string
		fs.StringVar(&msg, "msg", "", "text or @file")
		fs.StringVar(&timeout, "timeout", "", "turn timeout (default: config, then 2h)")
		wait := fs.Bool("w", false, "wait for the turn and print its result")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		ref := ""
		if msg == "" && len(pos) >= 2 && !strings.ContainsAny(pos[0], " \n") {
			if _, err := svc.Resolve(pos[0]); err == nil {
				ref, pos = pos[0], pos[1:]
			}
		} else if msg != "" && len(pos) == 1 {
			ref, pos = pos[0], nil
		} else if msg != "" && len(pos) > 1 {
			return fmt.Errorf("message given twice: -msg and %q", strings.Join(pos, " "))
		}
		if msg == "" {
			if msg, err = text(pos, os.Stdin, stdinPiped()); err != nil {
				return err
			}
		}
		id, err := svc.Resolve(ref)
		if err != nil {
			return err
		}
		if _, err := svc.Reply(app.ReplyRequest{ID: id, Message: msg, Timeout: timeout}); err != nil {
			return err
		}
		return submitted(svc, id, *wait, *asJSON)
	case "status", "wait", "result", "cancel":
		timeout := fs.Duration("timeout", app.DefaultTimeout, "wait timeout")
		first := new(bool)
		if cmd == "wait" {
			fs.BoolVar(first, "any", false, "with several tasks, return when the first finishes")
		}
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if cmd == "wait" && len(pos) > 1 {
			ids := make([]string, len(pos))
			for i, ref := range pos {
				if ids[i], err = svc.Resolve(ref); err != nil {
					return err
				}
			}
			tasks, err := svc.WaitMany(context.Background(), ids, *first, *timeout)
			if *asJSON {
				_ = emit(map[string]any{"tasks": tasks})
			} else {
				for _, t := range tasks {
					fmt.Println(line(t))
				}
			}
			return err
		}
		if len(pos) > 1 {
			return fmt.Errorf("%s takes at most one task", cmd)
		}
		id, err := svc.Resolve(strings.Join(pos, ""))
		if err != nil {
			return err
		}
		switch cmd {
		case "status":
			t, err := svc.Status(id)
			if err != nil || *asJSON {
				return orEmit(t, err)
			}
			fmt.Println(line(t))
			return nil
		case "wait":
			t, err := svc.Wait(context.Background(), id, *timeout)
			if *asJSON {
				_ = emit(t)
			} else if t != nil {
				fmt.Println(line(t))
			}
			return err
		case "result":
			r, err := svc.Result(id)
			if err != nil || *asJSON {
				return orEmit(r, err)
			}
			printResult(r)
			return nil
		default:
			t, err := svc.Cancel(id)
			if err != nil || *asJSON {
				return orEmit(t, err)
			}
			fmt.Println(line(t))
			return nil
		}
	case "config":
		theme := fs.String("theme", "", "auto|light|dark (default: config, then auto)")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		if !*asJSON && terminal(os.Stdin) && terminal(os.Stdout) {
			return tui.Run(svc, *theme, true)
		}
		return printConfig(*asJSON)
	case "agents":
		repo := fs.String("repo", "", "git repository whose roles to include (default: current directory)")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		list, err := app.AgentsFor(cmp.Or(*repo, "."), true)
		if err != nil || *asJSON {
			return orEmit(list, err)
		}
		for _, a := range list.Agents {
			var parts []string
			if a.Agent != "" {
				parts = append(parts, "角色 → "+a.Agent)
			}
			if a.Mode != "" {
				parts = append(parts, "只"+app.ModeName(a.Mode))
			}
			if a.Model != "" || a.Effort != "" {
				parts = append(parts, cmp.Or(a.Model, "默认模型")+" / "+cmp.Or(a.Effort, "默认 effort"))
			}
			if a.Default {
				parts = append(parts, "默认")
			}
			if a.Hidden {
				parts = append(parts, "对主 Agent 隐藏")
			}
			if a.Description != "" {
				parts = append(parts, a.Description)
			}
			if a.Error != "" {
				parts = append(parts, "✗ "+a.Error)
			}
			fmt.Printf("%-10s %s\n", a.Name, strings.Join(parts, " · "))
		}
		return nil
	case "list", "ls":
		group := fs.String("group", "", "only tasks in this group")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		list, err := svc.List(*group)
		if err != nil || *asJSON {
			return orEmit(list, err)
		}
		for _, t := range list.Tasks {
			fmt.Println(line(t))
		}
		return nil
	case "export":
		var req app.ExportRequest
		all := fs.Bool("all", false, "every task of the repository (-repo, default current directory)")
		fs.StringVar(&req.Repo, "repo", "", "every task of this git repository")
		fs.StringVar(&req.Group, "group", "", "every task in this group")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if *all && req.Repo == "" {
			req.Repo = "."
		}
		req.IDs = pos
		out, err := svc.Export(req)
		if err != nil || *asJSON {
			return orEmit(out, err)
		}
		for _, f := range out.Files {
			fmt.Println(f)
		}
		return nil
	case "clean":
		var req app.CleanRequest
		repo := fs.String("repo", ".", "git repository")
		all := fs.Bool("all", false, "every repository")
		fs.BoolVar(&req.Force, "force", false, "also remove write tasks whose branch is not merged")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if req.IDs = pos; !*all {
			req.Repo = *repo
		}
		plan, err := svc.PlanClean(req)
		if err != nil {
			return err
		}
		fmt.Print(plan.Text())
		if plan.Removable() == 0 {
			return nil
		}
		// A person confirms on a terminal; an agent's shell has none and uses
		// baton_clean, which asks the user through the MCP client.
		if !terminal(os.Stdin) {
			return fmt.Errorf("clean asks for confirmation on a terminal; run it in one")
		}
		fmt.Print("确认删除？[y/N] ")
		if answer, _ := bufio.NewReader(os.Stdin).ReadString('\n'); strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("未删除")
			return nil
		}
		svc.Clean(plan)
		if *asJSON {
			return emit(plan)
		}
		fmt.Printf("已删除 %d 个任务\n", len(plan.Removed))
		for _, it := range plan.Tasks {
			if it.Error != "" {
				fmt.Printf("  %s 删除失败：%s\n", app.ShortID(it.ID), it.Error)
			}
		}
		return nil
	case "sessions":
		repo := fs.String("repo", ".", "git repository")
		all := fs.Bool("all", false, "every repository")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		if *all {
			*repo = ""
		}
		sessions, err := svc.Sessions(*repo)
		if err != nil || *asJSON {
			return orEmit(map[string]any{"sessions": sessions}, err)
		}
		for _, s := range sessions {
			fmt.Printf("%s %s · %s · %s · %s\n  目录 %s\n", app.ShortID(s.Task), s.Title, s.Agent, app.ModeName(s.Mode), filepath.Base(s.Repo), s.Dir)
			if s.Transcript != "" {
				fmt.Println("  会话", s.Transcript)
			}
			for _, turn := range s.Turns {
				fmt.Println("  轮次", turn)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

func orEmit(value any, err error) error {
	if err != nil {
		return err
	}
	return emit(value)
}

// submitted reports a dispatched turn, or with -w waits for it and prints the
// result; a failed task then exits non-zero for scripts.
func submitted(svc *app.Service, id string, wait, asJSON bool) error {
	t, err := svc.Status(id)
	if err != nil {
		return err
	}
	if !wait {
		if asJSON {
			return emit(app.Submitted{ID: id, Status: t.Status})
		}
		fmt.Printf("已派出 %s\n查看：baton wait %s · baton tui\n", line(t), app.ShortID(id))
		return nil
	}
	if !asJSON {
		fmt.Fprintf(os.Stderr, "已派出 %s，等待完成（Ctrl-C 只停止等待，任务继续）…\n", line(t))
	}
	if _, err := svc.Wait(context.Background(), id, 24*time.Hour); err != nil {
		return err
	}
	r, err := svc.Result(id)
	if err != nil {
		return err
	}
	if asJSON {
		err = emit(r)
	} else {
		printResult(r)
	}
	if err == nil && r.Status != "succeeded" {
		err = fmt.Errorf("task %s %s", app.ShortID(id), r.Status)
	}
	return err
}

// line is one row: short id, status, title, agent, mode, turns, repo.
func line(t *app.Task) string {
	group := ""
	if t.Group != "" {
		group = " · #" + t.Group
	}
	return fmt.Sprintf("%s %s %s · %s · %s · %s · 第 %d 轮 · %s%s", app.ShortID(t.ID), app.Glyph(t.Status), t.Status, t.Name(), t.Who(), app.ModeName(t.Mode), len(t.Turns), filepath.Base(t.Repo), group)
}

func printResult(r *app.Result) {
	fmt.Println(line(r.Task))
	if r.Branch != "" {
		fmt.Printf("分支 %s · patch %s\n", r.Branch, r.PatchPath)
	}
	if len(r.Verify.Checks) > 0 {
		var cs []string
		for _, c := range r.Verify.Checks {
			mark := map[string]string{"pass": "✓", "fail": "✗"}[c.Status]
			if mark == "" {
				mark = "◌"
			}
			cs = append(cs, mark+" "+c.Command)
		}
		fmt.Println("检查", strings.Join(cs, "  "))
	}
	if strings.TrimSpace(r.Diffstat) != "" {
		fmt.Println(strings.TrimRight(r.Diffstat, "\n"))
	}
	for _, w := range r.Warnings {
		fmt.Println("提示:", w)
	}
	if r.Error != "" {
		fmt.Println("错误:", r.Error)
	}
	if r.FinalText != "" {
		fmt.Println("\n" + r.FinalText)
	}
}

type agentConfig struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Path   string `json:"path,omitempty"` // resolved executable
	Error  string `json:"error,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// printConfig shows the config file and the defaults each agent gets from it.
func printConfig(asJSON bool) error {
	cfg, path, raw, err := app.LoadConfig()
	if err != nil {
		return err
	}
	agents := map[string]agentConfig{}
	for _, name := range app.Agents {
		ac := cfg.Agents[name]
		v := agentConfig{Model: ac.Model, Effort: ac.Effort, Hidden: ac.Hidden}
		if argv, err := agent.Argv(name, agent.Options{Path: ac.Path}); err != nil {
			v.Error = err.Error()
		} else {
			v.Path = argv[0]
		}
		agents[name] = v
	}
	if asJSON {
		return emit(map[string]any{"path": path, "exists": raw != nil, "agent": cfg.DefaultAgent(),
			"timeout": cfg.TurnTimeout().String(), "theme": cmp.Or(cfg.Theme, "auto"), "max_parallel": cfg.MaxParallel, "agents": agents})
	}
	note := ""
	if raw == nil {
		note = "（不存在，使用默认值）"
	}
	fmt.Printf("配置 %s%s\n", path, note)
	source := ""
	if os.Getenv("BATON_AGENT") != "" {
		source = "（BATON_AGENT）"
	}
	parallel := "不限"
	if cfg.MaxParallel > 0 {
		parallel = fmt.Sprint(cfg.MaxParallel)
	}
	fmt.Printf("默认 Agent %s%s · 超时 %s · 主题 %s · 同时运行 %s\n", cfg.DefaultAgent(), source, cfg.TurnTimeout(), cmp.Or(cfg.Theme, "auto"), parallel)
	or := func(s string) string { return cmp.Or(s, "默认") }
	for _, name := range app.Agents {
		v := agents[name]
		hidden := ""
		if v.Hidden {
			hidden = " · 对主 Agent 隐藏"
		}
		fmt.Printf("%-7s 模型 %s · effort %s · %s%s\n", name, or(v.Model), or(v.Effort), cmp.Or(v.Path, v.Error), hidden)
	}
	return nil
}
