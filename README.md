# Baton

Baton 是 Go 编写的 Agent 派活中间层：主 Agent 选择任务和执行者，Baton 负责后台执行、Git 隔离、运行检查、保存结果。没有 Planner、Reviewer 或数据库。

```sh
brew install Ownera1/tap/baton   # 安装；以后用 brew upgrade baton 更新
claude mcp add baton -- /opt/homebrew/bin/baton mcp   # 接入 Claude Code

make build                  # bin/baton
make test                   # go vet ./... + go test -p 1 ./...
make cover                  # 覆盖率，含端到端测试里 worker 子进程跑到的代码
make bench                  # 基准：事件解析吞吐、一次派活的自身开销；用 benchstat 对比前后
```

## 日常用法

```sh
baton do 给用户列表 API 加分页            # 改代码：默认 codex、当前仓库、自动检查
baton do -w @brief.md                     # 派出后等它做完，直接打印结果
baton ask claude 解释 guard 包的内存采样   # 只读提问，可在任务前写 Agent 名
git diff | baton ask 评审这段改动          # 任务文字也可以从管道读
baton reply 再补上边界处理                 # 继续最近一个任务的同一会话
baton wait                                # 等最近一个任务；也可写 ID 后 8 位
baton wait a1b2c3d4 e5f6a7b8 -any          # 等多个任务，-any 等到第一个结束
baton do -base a1b2c3d4 补上测试            # 接着另一个写任务的分支继续
baton result 2fa5f633                     # 结果、分支、检查、diffstat、最终回复
baton agents                              # 可派的 Agent 和角色
baton sessions -json                      # 当前仓库各任务的会话记录在哪
baton stats -group bench                  # 按 Agent、角色、模型汇总成功率、用时、token 和花费
baton export -all                         # 把当前仓库的对话导出为 Markdown，写到 .baton/history/
baton config                              # 设置面板；管道或 -json 时打印各 Agent 实际用的模型、effort、可执行文件
baton                                     # 打开面板（同 baton tui）
```

| 默认值 | 说明 |
|---|---|
| Agent | `codex`；用配置的 `agent` 或 `BATON_AGENT` 改，或在任务前写 `claude`、`grok`、`agy`、`pi` 或[角色](#角色)名 |
| 仓库 | 当前目录所在的 git 仓库；`-repo` 指定其他仓库。MCP 默认取服务启动目录 |
| 模型 / effort | 配置的 `agents.<名字>`，没有就用各 Agent 自己的配置；`-model`、`-effort` 只在需要时覆盖 |
| 检查（仅改代码任务） | 依次查找 `.baton/checks`（一行一条命令，`#` 注释，空文件表示不检查）、`go.mod`（`go vet` + `go test`）、`Cargo.toml`（`cargo test`）、Makefile 的 `test` 目标。npm 项目不自动推断：worktree 里没有 `node_modules`，请写进 `.baton/checks`。`-check` 指定后不再推断，`-no-check` 跳过检查 |
| 任务 | 不写就是最近一个；写完整 ID、唯一前缀或后缀都行，至少 4 个字符 |
| 超时 | 单轮 2 小时，包含 Agent 和检查时间；配置的 `timeout` 可改 |

### 配置

`~/.config/baton/config.json`（`XDG_CONFIG_HOME` 或 `BATON_CONFIG_DIR` 可改位置），每项都可省略，文件不存在时全用默认值：

```json
{
  "agent": "codex",
  "timeout": "2h",
  "theme": "dark",
  "max_parallel": 4,
  "memory_mb": 8192,
  "cpus": 4,
  "agents": {
    "codex":  { "model": "gpt-5.5", "effort": "high" },
    "claude": { "model": "opus", "effort": "xhigh" },
    "grok":   { "path": "/opt/homebrew/bin/grok" },
    "pi":     { "hidden": true }
  }
}
```

优先级：命令行或 MCP 参数 > `BATON_AGENT`（仅 Agent）> 配置 > 内置默认。`effort` 原样传给 Agent（codex 为 `model_reasoning_effort`，pi 为 `--thinking`），取值以各 Agent 为准。`path` 写绝对路径或 PATH 里的命令名。配置在派活时读取，模型、effort、路径写入任务，`reply` 沿用，之后改配置不影响已有对话；MCP 每次派活重新读取，无需重启。未知字段、未知 Agent 名或无法解析的超时会直接报错，不会悄悄回退。`max_parallel` 限制同一个 `BATON_HOME` 下同时运行的轮数（Agent 加检查），多出的任务保持 queued，有空位再开始，排队时也能取消；派活时记下，不写或 0 为不限。`memory_mb` 和 `cpus` 是每轮 Agent 和每条检查各自的资源预算，默认 3072 MB 和 2：内存是整个进程组的硬上限，超过就停掉，这一轮记为失败，大仓库构建或测试常被停掉时调高；`cpus` 设置 `GOMAXPROCS`、`GOFLAGS=-p`、`MAKEFLAGS=-j`、`CARGO_BUILD_JOBS` 等并降低调度优先级，不是硬限制。两者也在派活时记下。`hidden: true` 让主 Agent 看不到这个 Agent：MCP 的 `baton_agents` 不列出，`baton_run`、`baton_reply`（包括没写 Agent 而落到默认值的情况）直接报错；状态、结果、取消不受影响，你在终端里用 CLI 也不受影响。

在终端里运行 `baton config`，或在面板里按 `s`，左栏换成设置：26 项配置（全局 `agent`、`timeout`、`max_parallel`，面板 `theme`，资源 `memory_mb`、`cpus`，每个 Agent 的 `model`、`effort`、`path`、`hidden`），再是当前目录可见的[角色](#角色)：`name` 一行选角色，下面是它的 `description`、`agent`、`model`、`effort`、`mode`、`hidden` 和角色文件，右栏说明当前项：可选值、该 Agent 一次写任务的完整命令（配置带来的参数高亮）和保存后的文件（改动行标 `+`）。`←`/`→` 切换取值（effort 取值来自各 CLI 的 `--help`，grok 未列出只能输入），`enter` 输入任意值，`d` 恢复默认，`u` 撤销本项，`ctrl+s` 保存，`esc` 或 `s` 回到对话。在 `name` 一行按 `enter` 输入名称检索角色（前缀优先，再按 `-` 分隔的词，再到包含），`↑↓` 选择、`enter` 打开；输入还没有的名称再 `enter` 新建全局角色，填好 `description` 后 `ctrl+s` 创建文件，保存前在文件一项按 `d` 放弃。`←`/`→` 依次切换角色，右栏列出全部角色，未保存的标 `●`；切换角色不丢改动，`ctrl+s` 一起保存。角色的 `effort` 取值跟随它的 Agent；保存只改动过的 front matter 行，注释、其他字段和正文不动。在角色文件一项按 `enter` 用 `$VISUAL` 或 `$EDITOR`（默认 `vi`）编辑整个文件，退出编辑器后重新读取；有未保存改动的角色要先保存。读取出错的角色只列出文件一项和错误，可以直接打开修。空值即默认，灰色显示实际会用什么；`path` 找不到时提示但允许保存，校验不过的值标红且不能保存。保存先写临时文件再改名替换，权限 0600；打开后文件被外部改过则拒绝覆盖，按 `r` 重新读取。未保存的改动离开设置后仍保留，顶栏提示数量，按 `q` 退出前要再确认一次。

### 接力

`-base <任务>`（MCP 的 `base`）让新任务接着一个已结束的写任务：写任务从它的 `baton/<id>` 分支最新提交建新 worktree 和分支，`patch.diff` 只含自己这一段，检查沿用它的检查列表，源仓库有未提交改动也可以；只读任务在它的 worktree 里运行，适合评审。第 1 轮提示里会告诉子 Agent 用 `git diff <它的起点>` 查看前一个任务的改动。`repo` 默认取它的仓库，写了别的仓库会报错；只读任务或仍在运行的任务不能作为 base。Baton 不做合并，子 Agent 也合并不了：沙箱不允许写 worktree 的 git 元数据（实测 codex 的 `git merge` 失败）。整合并行任务时，由主 Agent 先审核各分支，再自己合并通过审核的 `baton/<id>`。

`-group <名字>`（MCP 的 `group`）给一起完成同一目标的任务打标签，以 base 派出的任务不写时沿用 base 的组；`baton list -group 名字`、MCP `baton_list` 的 `group` 只列这一组，列表和面板里显示为 `#名字`。

`baton wait` 可以写多个任务，默认等全部结束，`-any` 等到第一个结束就返回；MCP 的 `baton_wait` 传 `ids`（和 `any`），结果放在 `tasks` 里，单个 `id` 的返回不变。

### 会话记录

写任务在 worktree（`$BATON_HOME/tasks/<id>/wt`）里运行，claude、pi、grok 按运行目录存会话，这些记录不会归到仓库名下。`baton sessions` 列出当前仓库（`-repo` 指定其他仓库，`-all` 列出全部）每个任务的运行目录、Agent 自己的会话文件和 Baton 存的每轮输出（`turn-N.stdout`，完整事件流）；`-json` 供其他工具读取。会话文件按默认位置查找，跟随 `CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`PI_CODING_AGENT_DIR`，找不到时只给 Baton 的那份。

### 导出对话

`baton export [任务…]`（默认最近一个；`-all` 导出当前仓库的全部任务，`-repo` 指定其他仓库，`-group` 导出一组）、面板里的 `x`、MCP 的 `baton_export` 把对话写成 Markdown，放在任务所属仓库的 `.baton/history/<日期>-<标题>-<ID 后 8 位>.md`，在这个项目里工作的 Agent 都能读到。每轮包含指令、最终回复（附工具调用数）和检查结果；再次导出会覆盖同一个文件。第一次导出时目录里会生成只含 `*` 的 `.gitignore`，导出文件不会让仓库变脏、影响下次派写任务；想提交这些记录就删掉它。

### 统计

`baton stats` 按 Agent（或角色）、模型、effort 汇总当前仓库已结束的任务（`-repo` 指定其他仓库，`-all` 全部仓库，`-group` 只看一组，不限仓库）：任务数、成功数、第 1 轮就成功的数量、轮数，Agent 用时和检查用时，token 和花费，以及每个成功任务平均摊到的 token 和花费（失败任务的消耗也算进去）。`-json` 输出原始数字。

各 Agent 的 usage 字段不同，Baton 读任务时换成同一口径，放在每轮的 `tokens` 里：输入含缓存读写，缓存单列；输出含推理。codex 报的是整个会话的累计值，`tokens` 取与上一轮的差，是这一轮自己的用量。`baton result` 显示各轮合计，`-json`、MCP 的 `baton_result` 和导出的 Markdown 有每轮的明细；原始 `usage` 照旧保留。花费只有 claude、grok、pi 自己报，codex 和 agy 只有 token。Agent 用时是每轮开始到结束减去检查时间，含 Baton 建 worktree、提交和生成 patch，不含排队。想比较几个 Agent 时，把同一批任务派给它们并打上同一个 `-group`。

### 清理任务

`baton clean [任务…]` 删除已结束的任务：每个任务连同它的 worktree、`baton/<id>` 分支和 `tasks/<id>/` 记录一起删，不会只删一部分。不写任务时扫描当前仓库（`-repo` 指定其他仓库，`-all` 扫描全部仓库）的已结束任务。写任务的分支须已合并进仓库当前分支才会删：是它的祖先、`git cherry` 能在当前分支找到它的每个提交（rebase 或 cherry-pick），或者它的 squash 副本能找到（squash 合并，思路来自 [git-delete-merged-branches](https://github.com/hartwork/git-delete-merged-branches)，未复制代码）；`-force` 连未合并的一起删。排队、运行中或 stale 的任务，以及仍有任务以它为 base 的任务都会保留；worktree 有未提交改动或未跟踪文件时 git 拒绝删除，这个任务也保留。`task.json` 读不出来的任务在 `list` 里显示为失败，`-force` 才删，只删 `tasks/<id>/`，它的分支要自己删。命令先列出要删和保留的任务及占用空间，必须在终端里输入 `y` 才执行；没有终端（管道、Agent 的 shell）时只列出不删。面板里选中任务按 `d` 也按同样的规则删除单个任务，`y` 确认。

MCP 的 `baton_clean`（`ids`、`repo`、`force`）给主 Agent 用同样的规则清理，但删除前一定由用户确认：Baton 通过 MCP elicitation 让客户端弹出同样的列表，用户勾选“删除以上任务”并接受后才删，拒绝时返回 `declined`。Claude Code（2026-07-28 协议，带着回答重新调用）和 Codex（2025-06-18 协议，调用中途询问）都支持；不支持 elicitation 的客户端会收到错误，任何东西都不删。确认期间任务列表变了会重新询问。

### 角色

角色是带固定说明的子 Agent，一个 Markdown 文件一个，文件名即角色名（小写字母、数字、`-`、`_`，不能和 Agent 重名）。仓库的 `.baton/agents/<名字>.md` 覆盖配置目录（默认 `~/.config/baton/`）下的 `agents/<名字>.md`。

```markdown
---
description: 只读评审：找出改动里的 bug 和缺失的测试
agent: claude
model: opus
mode: read
---
你是代码评审。只列出有证据的问题，每条给出文件和行号。
```

front matter 每行一个 `key: value`（值可加引号，`#` 开头为注释，空值即默认，不支持多行值和列表）：`description` 必填，主 Agent 靠它挑角色；`name` 可省略，写了须与文件名一致；`agent` 不写用默认 Agent；另有 `model`、`effort`、`mode`、`hidden`。未知字段直接报错，从 Claude Code 复制来的 `tools` 等字段要删掉。正文在第 1 轮放在任务前面，`reply` 不再重复；角色名和正文在派活时写入任务，之后改文件不影响已有对话。

用法：`baton do tester 给分页补测试`、`baton ask reviewer 看看这次改动`，或 `-agent reviewer`；MCP 在 `agent` 里写角色名。命令行或 MCP 参数优先于角色。角色写了 `mode` 就只能以该模式运行（`baton do reviewer …` 报错），`run`/MCP 不写 `mode` 时取角色的值。`baton agents` 列出全部 Agent 和角色（含隐藏的和读取出错的），MCP 的 `baton_agents` 只列出未隐藏的。`hidden: true` 的角色对主 Agent 隐藏；角色可以使用被隐藏的 Agent，例如隐藏 `pi`，只暴露一个 `agent: pi`、`mode: read` 的角色。仓库级角色随仓库提交，克隆来的仓库也会带上，用之前先看一眼它的说明；`baton_agents` 用 `repo: true` 标出它们。仓库级角色不能以写模式运行 pi（没有沙箱），信任它就复制到全局的 `agents/` 下。

参数可以写在任意位置；任务文字以 `-` 开头时放在 `--` 之后。输出默认给人看，加 `-json` 输出结构化结果。`-w` 等待完成并打印结果，任务失败时退出码非零；Ctrl-C 只停止等待，任务继续。`baton run -agent … -repo … -task …` 等旧写法继续可用。

`do`/`ask`/`reply` 立即返回，独立 session 中的 `_worker` 执行任务，关闭 CLI、终端或 MCP 连接不会取消任务。`-task` 和 `-msg` 支持文本或 `@文件`。主 Agent 派活时应通过 `-title`（MCP 的 `title`）传入对话标题；未传时取任务的首个非空行，去掉行首 Markdown 标记及成对的反引号/粗体标记，按终端显示宽度缩到 40 列，超出加 `…`（中文占两列）。标题保存在任务中；旧任务只在显示时从第 1 轮 prompt 推导，不改旧文件。

| Agent | read | write | 续接 | 输出 |
|---|---|---|---|---|
| codex | read-only，`approval_policy=never` | workspace-write，`approval_policy=never`（沙箱是硬边界，不能申请越权） | exec resume thread_id | JSONL |
| claude | permission-mode plan | acceptEdits | --resume session_id | --output-format stream-json --verbose |
| grok | permission-mode plan + --tools read_file,list_dir,grep | permission-mode auto + --sandbox workspace | -r sessionId | --output-format streaming-json |
| agy | mode plan | accept-edits | --conversation conversation_id | --output-format stream-json |
| pi | read,grep,find,ls | 默认工具 | --session id | JSONL |

Pi 始终加 `-ne`，禁用用户扩展。Codex resume 没有 `-s`/`-C`，使用 `-c sandbox_mode="…"` 和工作目录保留原模式。可执行文件取配置的 `path`，否则通过 `exec.LookPath` 查找，直接执行，忽略 shell alias；stdin 为 `/dev/null`。opencode 暂不支持。

`baton tui -theme auto|light|dark` 读取当前 `BATON_HOME`。不加 `-theme` 时用配置的 `theme`，没配则根据终端背景选择浅色或深色；在设置里切换 `theme` 会立即换色，`ctrl+s` 保存后下次打开沿用。左栏按需要关注、进行中、已完成分组，显示标题、耗时和检查进度；详情先显示状态摘要、检查标签、改动统计及失败诊断，再进入四个标签页：历轮对话（glamour Markdown）、最新轮全部事件、着色 `patch.diff`、检查及失败输出末尾最多 12 行。改动最多显示 2000 行，超过会标明截断；只读任务显示没有改动。新轮次会保存独立检查结果，旧轮次缺少记录时显示“检查结果未记录”。

| 按键 | 行为 |
|---|---|
| `↑/↓`、`k/j` | 列表有焦点时选择任务，详情有焦点时滚动 |
| `tab`、鼠标点击 | 切换列表/详情焦点；点任务直接选中，点详情顶部的标签切换标签页 |
| `s` | 打开设置（见上文「配置」），`esc` 返回 |
| `PgUp/PgDn`、`home/end` | 详情翻页、到顶/到底 |
| `1`–`4` | 对话、事件、改动、检查 |
| `[` / `]`、`=` | 列表比例每次减/加 5%，范围 20–70%；复位到 38% |
| `z` | 专注详情，再按恢复分栏 |
| `e` | 展开/折叠全部工具调用和超过 12 行的提问；点击某处折叠行只切换那一处；运行中默认显示最近 4 条事件 |
| `c`、`y` | 请求取消后确认；`esc` 或其他键关闭确认 |
| `d`、`y` | 删除选中的已结束任务，规则同 `baton clean`（不带 `-force`）：底栏先列出要删的内容和占用空间，`y` 确认；需要保留的（未结束、分支未合并、仍被当作 base）只提示原因 |
| `x` | 把选中的对话导出到仓库的 `.baton/history/`（见「导出对话」），底栏显示文件名 |
| `q`、`ctrl+c` | 退出面板，任务继续运行；确认取消期间只关闭确认 |

分隔线支持鼠标拖动，滚轮滚动指针所在的那一栏，不改变焦点。宽度小于 100 列时上下堆叠，比例作用于高度。比例保存到 `$BATON_HOME/tui.json`（例如 `{"ratio":38}`），下次启动恢复；配置缺失、损坏或读写失败不影响运行。数据仍每秒刷新；有任务运行时 braille 转圈每 100ms 只重绘左栏，不读取文件，详情的状态用静止的 ● 随每秒刷新更新；stdout 大小不变时不再解析，Markdown 按任务、轮次、详情宽度、主题和回复长度缓存。窄终端会截短底栏提示和过长事件/改动行，方向键与快捷键仍可使用。除了确认取消、确认删除、比例配置和导出，面板不写任务文件；设置只在 `ctrl+s` 时写 `config.json`。

损坏或缺失的任务文件会显示读取错误并继续刷新；列表读取失败时保留上次成功的列表。未写完的 JSONL 行会暂时忽略，下一次刷新再解析。grok read 只开放三个只读工具，避免 plan 模式中的终端工具尝试取消整轮任务；write 的 auto + workspace sandbox 保持原样。

write 要求源仓库干净且已有 HEAD。worker 从记录的 HEAD 创建 `<task>/wt` 和 `baton/<id>` 分支；每轮 Agent 结束后，将改动提交到这条分支，生成相对初始 HEAD 的累计 `patch.diff`。未被忽略的新文件里，有可执行权限的二进制文件（如 `go build` 留在包目录的程序）不提交，移到 `<task>/artifacts/` 下同样的路径，并在 warnings 里写明；脚本等文本文件照常提交。自动提交关闭 Git hooks 和签名，避免后台交互和 hook 的额外动作；Git 作者身份沿用环境或仓库配置。然后逐条运行检查，记录对应 commit、退出码、耗时和日志。没有检查时为 `none`；资源监测不可用、内存超限、检查超时、锁等待超时或工具缺失时为 `unavailable`。检查失败或 unavailable 使任务为 `failed`。

检查之后再次确认分支和工作区状态；检查造成未提交改动或改变 HEAD 时结果包含“检查修改了工作区”提示，patch 对应检查前的提交。检查改动了已跟踪文件、索引或 HEAD 时，通过的不是交付的 patch，验证记为 `unavailable`，任务为 `failed`；只新增未跟踪文件（如构建产物）不影响验证结果。续接使用原 session、worktree 和检查列表；脏 worktree 必须先由用户处理，正在执行或 stale 的任务不能续接。取消通过标记通知 worker，终止当前 Agent 或检查的进程组；Agent 被取消或超时前已产生的改动也会按轮提交，任务仍记录 canceled 或 failed。

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

stdio 提供 `baton_agents`、`baton_run`、`baton_status`、`baton_wait`、`baton_result`、`baton_reply`、`baton_cancel`、`baton_list`、`baton_export`、`baton_clean`（见[清理任务](#清理任务)）。参数与 CLI 共用服务：run 只有 `task` 必填，另有 `agent/repo/title/mode/model/effort/checks/no_checks/timeout`，默认值与 CLI 相同（`repo` 取 MCP 服务启动目录）；`base`、`group` 见[接力](#接力)；`baton_list` 可传 `group`；reply 为 `id/msg/timeout`；`baton_wait` 另有 `ids`、`any`；`baton_agents` 可传 `repo`（默认服务启动目录），列出未隐藏的 Agent 和角色及其说明、实际的 Agent、模型和 effort、角色限定的 mode、是否默认，以及可执行文件缺失、角色文件有误等派活会失败的原因；run 的 `agent` 也可写角色名；其余任务工具传 `id`。`id` 必填（`baton_wait` 也可只传 `ids`），可写唯一前缀或后缀；不像 CLI 那样默认最近一个任务，因为它可能属于另一个主 Agent 会话。`baton_export` 须传 `ids`、`group` 或 `repo`。`baton_wait` 用 `timeout_seconds`，默认 120，上限 600；超时返回当前任务。`baton_status`、`baton_wait`、`baton_cancel`、`baton_list` 只返回任务摘要：id、标题、Agent 和角色、组、模式、状态、分支、轮数、最新一轮的检查状态（`check_status`、`check_notes`，检查跑完前为空）和错误、更新时间。`baton_result` 与 CLI `result -json` 相同，但不含角色说明和各轮的检查记录，`verify` 即最新一轮的检查。

MCP 服务在连接时带上 instructions，告诉主 Agent 怎么分派：先用 `baton_agents` 挑 Agent 或角色；独立任务用同一个 `group` 并行派出，`baton_wait` 传 `ids` 和 `any` 先到先处理；同一任务派给两个 Agent 择优；写任务结束后用 `base` 派只读评审，问题用 `baton_reply` 退回原任务；用 `base` 接力；子 Agent 合并不了分支，由主 Agent 审核每个分支（`baton_result` 的 patch 和检查，必要时加评审任务）后自己合并通过的 `baton/<id>`；遇到沙箱拒绝不能改派给沙箱更弱的 Agent（pi 的写模式没有沙箱）；子 Agent 之间不直接通信，结果只经主 Agent 或 `base` 传递；合并后可以用 `baton_clean` 清理已合并的任务，由用户确认。例如让 Claude Code「给用户列表加分页，后端和前端分开做，做完评审再合到一起」，它大致会这样调用：

```text
baton_agents                                              → codex、claude、角色 reviewer（只读）
baton_run {agent: codex,  task: 后端分页…, group: paging}   → a1
baton_run {agent: claude, task: 前端分页组件…, group: paging} → b2
baton_wait {ids: [a1, b2], any: true}                     → a1 先完成
baton_run {agent: reviewer, base: a1, task: 评审后端分页}     → 有一处越界
baton_reply {id: a1, msg: 修复评审指出的越界…}
baton_wait {ids: [a1, b2]}
baton_result a1、baton_result b2                           → 主 Agent 审核两份 patch 和检查
git merge baton/<a1 的 ID> baton/<b2 的 ID>                 → 主 Agent 用自己的 git 合并，冲突自己解决
→ 告诉用户：合并了哪些分支，没合并的是哪个、为什么
```

数据默认保存在 `~/.local/share/baton`，可通过 `BATON_HOME` 修改。每个 `tasks/<id>/` 保存原子更新的 `task.json`、原样的 `turn-n.stdout/stderr`、Codex 的 `turn-n.last`、`patch.diff`、`checks/turn-n/`、`artifacts/` 和 `worker.log`。`request.json` 是启动/续接信箱，`lock` 串行保护同一任务，`worker.pid` 记录启动 PID，`cancel-n` 是取消标记；只有 worker 写 `task.json`。`cache/` 是 codex 写任务共用的 Go 构建缓存：沙箱不让写默认缓存，codex 会在 worktree 里建 `.cache/go-build`（几百 MB），被 Baton 的提交带进分支；现在 Baton 把它设为沙箱里唯一的额外可写目录，并通过 `shell_environment_policy.set.GOCACHE` 指过去（这会替换 codex 配置里的 `writable_roots`）。grok 写任务的 `--sandbox workspace` 同样不让写默认缓存，也不让写 `cache/`，只放行临时目录，所以它的 `GOCACHE` 设为 `$TMPDIR/baton-go-build`，grok 写任务之间共用。一个任务的检查依次运行，不同任务的检查同时运行；配置了 `max_parallel` 时 `slots/<n>.lock` 限制同时运行的轮数，也就限制了同时运行的检查。检查的完整输出写进 `checks/turn-n/check-<id>.log`，结果里只留最后 4000 字节。

安全边界：Baton 不 push、不切换用户分支、不传任何 bypass / skip-permissions / always-approve / danger-full-access 参数。子 Agent 不能再派活：worker 给 Agent 和检查设置 `BATON_DEPTH=1`，带着它的 `do`/`ask`/`run`/`reply`（含 MCP）直接报错；codex 启动 MCP 服务时不传父进程环境，所以还会沿父进程往上找 `baton _worker`（每级一次 `ps`），找到同样拒绝；仓库位于 `$BATON_HOME/tasks/` 下（任务 worktree）时也拒绝。worktree 隔离代码改动，Agent 仍沿用自身权限机制。**Pi write 没有沙箱**，结果中也会提示；检查通过 `/bin/sh -c` 在本机执行，没有沙箱，有网络，能读你的家目录；写任务的检查跑的是子 Agent 刚写的代码（例如它写的测试），等于你亲手运行了它。Agent 自身的沙箱拦不住这一步，所以别对不信任的仓库或任务开写模式检查（`-no-check` 或 MCP 的 `no_checks` 关掉）。RSS 每秒采样，默认 3072 MB 上限，CPU 并行提示为 2（`memory_mb`、`cpus` 可改）、nice 为 10；这些限制不能约束主动脱离进程组的程序。内存上限按每条检查计算，几个任务同时检查时总占用会叠加，机器吃紧就设 `max_parallel`；所有实例用同一 BATON_HOME 才能共享这个上限。

已知限制：仅支持 macOS/Linux；PID 存活检查无法识别 PID 复用，意外死亡报告 stale；SIGKILL 无法执行 worker 的清理逻辑，可能遗留子进程。read 使用 Agent 的只读模式，允许源仓库已有改动；它不是独立的 OS 沙箱。任务、分支和 worktree 保留供审阅，用 [`baton clean`](#清理任务) 清理。未提供成本的 Agent 保持成本未知，不估算；result 的顶层回复/用量/成本属于最近一轮，历轮记录在 turns。适配器通过真实输出样本和假 Agent 验证，本项目测试不调用真实服务。

发布：`scripts/release.sh v0.1.1` 先等 `main` 当前提交的 CI 通过，再跑测试、打 tag、推送，并更新 `Ownera1/homebrew-tap` 的 `Formula/baton.rb`（从源码构建）。

从同作者 CW 复制并裁剪 `guard`、`workspace`、`verify`，以及 detached worker 和 stdio MCP 写法；删除主机负载预检、未使用的格式化/清理/配置接口，不引入 CW 的编排、评审、SQLite 或其他运行层。
