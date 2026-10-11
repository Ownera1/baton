# Baton

Baton 是 Go 编写的 Agent 派活中间层：主 Agent 选择任务和执行者，Baton 负责后台执行、Git 隔离、运行检查、保存结果。没有 Planner、Reviewer 或数据库。

```sh
brew install Ownera1/tap/baton   # 安装；以后用 brew upgrade baton 更新
claude mcp add baton -- /opt/homebrew/bin/baton mcp   # 接入 Claude Code

make build                  # bin/baton，Go 1.27.1；依赖已缓存时可离线构建
make test                   # go vet ./... + go test -p 1 ./...

bin/baton run -agent codex -repo /path/to/repo -task '实现这个功能' \
  -check 'go test ./...' -timeout 30m
bin/baton status <id> -json
bin/baton wait <id> -timeout 10m -json
bin/baton result <id> -json
bin/baton reply <id> -msg '再补上边界处理' -timeout 30m
bin/baton cancel <id>
bin/baton list -json
bin/baton tui                # 每秒刷新的任务面板

bin/baton run -agent claude -repo /path/to/repo -mode read -task @question.txt
```

`run`/`reply` 立即返回 `{ "id": "…", "status": "queued" }`。独立 session 中的 `_worker` 执行任务，关闭 CLI、终端或 MCP 连接不会取消任务。`-task` 和 `-msg` 支持文本或 `@文件`，`-check` 可以重复传入；`-model`/`-effort` 透传给 Agent。默认 `write`，单轮超时默认 30 分钟，包含 Agent 和检查时间。CLI `wait` 默认 10 分钟，等待超时输出当前 JSON 并以非零状态退出。

| Agent | read | write | 续接 | 输出 |
|---|---|---|---|---|
| codex | read-only | workspace-write | exec resume thread_id | JSONL |
| claude | permission-mode plan | acceptEdits | --resume session_id | --output-format stream-json --verbose |
| grok | permission-mode plan + --tools read_file,list_dir,grep | permission-mode auto + --sandbox workspace | -r sessionId | --output-format streaming-json |
| agy | mode plan | accept-edits | --conversation conversation_id | --output-format stream-json |
| pi | read,grep,find,ls | 默认工具 | --session id | JSONL |

Pi 始终加 `-ne`，禁用用户扩展。Codex resume 没有 `-s`/`-C`，使用 `-c sandbox_mode="…"` 和工作目录保留原模式。可执行文件通过 `exec.LookPath` 查找并直接执行，忽略 shell alias；stdin 为 `/dev/null`。opencode 暂不支持。

`baton tui` 读取当前 `BATON_HOME`，每秒刷新任务列表和选中任务最新一轮 stdout 的最后 40 条事件。宽屏左右分栏，窄屏上下堆叠；列表显示短 ID、Agent、模式、状态、轮数、当前轮耗时、检查状态和仓库名。详情包含 prompt、实时工具/消息/错误、最终回复、diffstat、检查退出码、警告和当前轮成本。`↑/↓` 或 `k/j` 选择任务；`tab` 切换列表/详情焦点，详情中用同样的方向键滚动，也支持 PgUp/PgDown；`c` 发起取消，必须再按 `y` 确认，其他键关闭确认；`q` 或 `ctrl+c` 退出。退出面板不会停止任务。除确认取消外，面板只读，不写任务文件。

损坏或缺失的任务文件会显示读取错误并继续刷新；列表读取失败时保留上次成功的列表。未写完的 JSONL 行会暂时忽略，下一次刷新再解析。grok read 只开放三个只读工具，避免 plan 模式中的终端工具尝试取消整轮任务；write 的 auto + workspace sandbox 保持原样。

write 要求源仓库干净且已有 HEAD。worker 从记录的 HEAD 创建 `<task>/wt` 和 `baton/<id>` 分支；每轮 Agent 结束后，将改动提交到这条分支，生成相对初始 HEAD 的累计 `patch.diff`。自动提交关闭 Git hooks 和签名，避免后台交互和 hook 的额外动作；Git 作者身份沿用环境或仓库配置。然后逐条运行检查，记录对应 commit、退出码、耗时和日志。没有检查时为 `none`；资源监测不可用、内存超限、检查超时、锁等待超时或工具缺失时为 `unavailable`。检查失败或 unavailable 使任务为 `failed`。

检查之后再次确认分支和工作区状态；检查造成未提交改动或改变 HEAD 时结果包含“检查修改了工作区”提示，patch 对应检查前的提交。续接使用原 session、worktree 和检查列表；脏 worktree 必须先由用户处理，正在执行或 stale 的任务不能续接。取消通过标记通知 worker，终止当前 Agent 或检查的进程组；Agent 被取消或超时前已产生的改动也会按轮提交，任务仍记录 canceled 或 failed。

```sh
# MCP 配置示例（command 必须为已构建二进制的绝对路径）
bin/baton mcp
```

```json
{
  "mcpServers": {
    "baton": {
      "command": "/absolute/path/to/bin/baton",
      "args": ["mcp"],
      "env": { "BATON_HOME": "/absolute/path/to/baton-data" }
    }
  }
}
```

stdio 提供 `baton_run`、`baton_status`、`baton_wait`、`baton_result`、`baton_reply`、`baton_cancel`、`baton_list`。参数与 CLI 共用服务：run 为 `agent/repo/task/mode/model/effort/checks/timeout`；reply 为 `id/msg/timeout`；其余任务工具传 `id`。`baton_wait` 用 `timeout_seconds`，默认 120，上限 600；超时返回当前任务。结构化结果与 CLI 的 JSON 相同。

数据默认保存在 `~/.local/share/baton`，可通过 `BATON_HOME` 修改。每个 `tasks/<id>/` 保存原子更新的 `task.json`、原样的 `turn-n.stdout/stderr`、Codex 的 `turn-n.last`、`patch.diff`、`checks/turn-n/` 和 `worker.log`。`request.json` 是启动/续接信箱，`lock` 串行保护同一任务，`worker.pid` 记录启动 PID，`cancel-n` 是取消标记；只有 worker 写 `task.json`。同一个 BATON_HOME 下的 `check-gate.lock` 跨进程串行运行所有检查。

安全边界：Baton 不 push、不切换用户分支、不传任何 bypass / skip-permissions / always-approve / danger-full-access 参数。worktree 隔离代码改动，Agent 仍沿用自身权限机制。**Pi write 没有沙箱**，结果中也会提示；用户提供的检查通过 `/bin/sh -c` 执行，同样没有安全沙箱。RSS 每秒采样，默认 3072 MB 上限，CPU 并行提示为 2、nice 为 10；这些限制不能约束主动脱离进程组的程序。应对所有实例使用同一 BATON_HOME 才能共享全机检查锁。

已知限制：仅支持 macOS/Linux；PID 存活检查无法识别 PID 复用，意外死亡报告 stale；SIGKILL 无法执行 worker 的清理逻辑，可能遗留子进程。read 使用 Agent 的只读模式，允许源仓库已有改动；它不是独立的 OS 沙箱。任务、分支和 worktree 保留供审阅，暂未提供清理命令。未提供成本的 Agent 保持成本未知，不估算；result 的顶层回复/用量/成本属于最近一轮，历轮记录在 turns。适配器通过真实输出样本和假 Agent 验证，本项目测试不调用真实服务。

发布：`scripts/release.sh v0.1.1` 跑测试、打 tag、推送，并更新 `Ownera1/homebrew-tap` 的 `Formula/baton.rb`（从源码构建）。

从同作者 CW 复制并裁剪 `guard`、`workspace`、`verify`，以及 detached worker 和 stdio MCP 写法；删除主机负载预检、未使用的格式化/清理/配置接口，不引入 CW 的编排、评审、SQLite 或其他运行层。
