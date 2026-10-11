package main

import (
	"baton/internal/agent"
	"baton/internal/app"
	"baton/internal/mcpserver"
	"baton/internal/tui"
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
)

// version is set by the release build: -ldflags "-X main.version=...".
var version = "dev"

const usage = `用法：
  baton                          打开面板（同 baton tui）
  baton do [agent] <任务>         派出改代码的任务，默认 codex（可在配置中修改）
  baton ask [agent] <问题>        派出只读任务
  baton reply [任务] <消息>       在同一会话里继续
  baton wait|result|status|cancel [任务]
  baton list | tui | mcp | version
  baton config                   终端里打开设置面板；管道或 -json 时打印配置

<任务> 可以是文字、@文件，或 - 从标准输入读；[任务] 默认最近一个，可写 ID 的前缀或后缀。
常用参数（可写在任意位置）：-w 等完成并打印结果  -check 命令  -no-check  -title 标题
  -repo 路径（默认当前目录）  -model  -effort  -timeout（默认 2h）  -json
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

// splitAgent takes a leading agent name when more text follows it.
func splitAgent(pos []string) (string, []string) {
	if len(pos) >= 2 && slices.Contains(app.Agents, pos[0]) {
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

func terminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

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
		fs.StringVar(&req.Agent, "agent", "", "codex|claude|grok|agy|pi (default: BATON_AGENT, config, then codex)")
		fs.StringVar(&req.Repo, "repo", "", "git repository (default: current directory)")
		fs.StringVar(&req.Task, "task", "", "text or @file")
		fs.StringVar(&req.Title, "title", "", "conversation title (inferred from task when omitted)")
		fs.StringVar(&req.Model, "model", "", "model (default: Baton config, then the agent's own config)")
		fs.StringVar(&req.Effort, "effort", "", "reasoning effort (default: Baton config, then the agent's own config)")
		fs.StringVar(&req.Timeout, "timeout", "", "turn timeout, including checks (default: config, then 2h)")
		fs.Var(&commands, "check", "check command (repeatable; default: detected from the project)")
		fs.BoolVar(&req.NoChecks, "no-check", false, "run no checks")
		wait := fs.Bool("w", false, "wait for the task and print its result")
		req.Mode = map[string]string{"do": "write", "ask": "read"}[cmd]
		if cmd == "run" {
			fs.StringVar(&req.Mode, "mode", "write", "read|write")
		}
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		agent, pos := splitAgent(pos)
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
		pos, err := parse(fs, args[1:])
		if err != nil {
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
	case "list", "ls":
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		list, err := svc.List()
		if err != nil || *asJSON {
			return orEmit(list, err)
		}
		for _, t := range list.Tasks {
			fmt.Println(line(t))
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
		fmt.Printf("已派出 %s\n查看：baton wait %s · baton tui\n", line(t), short(id))
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
		err = fmt.Errorf("task %s %s", short(id), r.Status)
	}
	return err
}

var glyphs = map[string]string{"running": "●", "queued": "◌", "succeeded": "✓", "failed": "✗", "canceled": "■", "stale": "!"}
var modes = map[string]string{"read": "读", "write": "写"}

func short(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func title(t *app.Task) string {
	if t.Title != "" {
		return t.Title
	}
	if len(t.Turns) > 0 {
		return app.TitleFromPrompt(t.Turns[0].Prompt)
	}
	return "未命名对话"
}

// line is one row: short id, status, title, agent, mode, turns, repo.
func line(t *app.Task) string {
	return fmt.Sprintf("%s %s %s · %s · %s · %s · 第 %d 轮 · %s", short(t.ID), glyphs[t.Status], t.Status, title(t), t.Agent, modes[t.Mode], len(t.Turns), filepath.Base(t.Repo))
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
		v := agentConfig{Model: ac.Model, Effort: ac.Effort}
		if argv, err := agent.Argv(name, agent.Options{Path: ac.Path}); err != nil {
			v.Error = err.Error()
		} else {
			v.Path = argv[0]
		}
		agents[name] = v
	}
	if asJSON {
		return emit(map[string]any{"path": path, "exists": raw != nil, "agent": cfg.DefaultAgent(),
			"timeout": cfg.TurnTimeout().String(), "theme": cmp.Or(cfg.Theme, "auto"), "agents": agents})
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
	fmt.Printf("默认 Agent %s%s · 超时 %s · 主题 %s\n", cfg.DefaultAgent(), source, cfg.TurnTimeout(), cmp.Or(cfg.Theme, "auto"))
	or := func(s string) string { return cmp.Or(s, "默认") }
	for _, name := range app.Agents {
		v := agents[name]
		fmt.Printf("%-7s 模型 %s · effort %s · %s\n", name, or(v.Model), or(v.Effort), cmp.Or(v.Path, v.Error))
	}
	return nil
}
