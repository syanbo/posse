# hive

多 agent 终端管理器，替代 Orca。目标是低内存：一个 Go 常驻进程托管所有终端，界面可以随时关闭。

## 构建与测试

```sh
export GOPROXY=https://goproxy.cn,direct   # 本机直连 proxy.golang.org / storage.googleapis.com 会超时
go build -o bin/hive ./cmd/hive
go vet ./... && go test ./... && go tool mygo vet
go tool mygo build -platform darwin/arm64 -skip-dmg   # 打包 build/darwin-arm64/Hive.app
```

- go.mod 要求 Go 1.27.1（MyGo 的要求），本机 Go 1.26 会通过 GOTOOLCHAIN=auto 自动下载。
- 终端内核 libghostty-vt 首次运行时由 MyGo 下载到 `~/Library/Caches/mygo/natives/`。
- 端到端测试用独立目录，不要碰 `~/.hive`：`HIVE_HOME=/tmp/hive-e2e/home ./bin/hive ...`
- 改界面后用离屏预览自查（屏幕锁定时也能用）：`HIVE_PREVIEW_DIR=/tmp/hive-preview go test ./internal/app -run TestPreview`，生成深浅两套主界面、空工作区、首次使用、新建对话框、任务总览、派发对话框、历史页（含 Markdown 回复）的 PNG。
- gofmt 用 `$(go env GOROOT)/bin/gofmt`：Homebrew 的 gofmt 版本较旧，对齐规则不同。
- 界面约定见 `internal/app/style.go` 开头，质感参照 AndroidMirror：顶栏、画布、卡片三层底色；选中项是强调色淡底加左侧竖条；只有"待处理"用琥珀色加文字；状态用蜂巢格形状区分（实心工作中、空心带点待处理、空心完成）；不用"A · B"中点串拼接信息。每帧开头 `SetTheme(hiveTheme(...))` 换成 Claude 橙（深色 #D97757、浅色 #C15F3C）和 8px 圆角，终端底色经 `terminalThemes` 跟主题一致。侧栏是自绘的行（`navRow`），不用 mygo 的 Sidebar：它的选中样式改不了。
- 应用图标是 `design/icon.png`（由 `design/icon-art.png` 套 macOS 图标模板的圆角和投影得到），`mygo.json` 的 `icon` 指向它。不要放进 `resources/`：那里的文件会原样打进 app。
- 测试 Hive.app 时用 `open -n --env HIVE_HOME=/tmp/hive-e2e/home build/darwin-arm64/Hive.app` 启动。直接运行包内二进制时，Computer Use 按 bundle id 连接会经 LaunchServices 另起一个不带 `HIVE_HOME` 的实例，误用 `~/.hive`。
- 内存验收看 `footprint -p <pid>` 的 phys_footprint，不看 RSS：libghostty-vt 压缩回滚后 RSS 不会立即下降。

## 结构

```
cmd/hive            单一二进制：窗口（hive app，或从 Hive.app 无参数启动）、常驻进程（hive daemon run）和 CLI 共用
internal/rpc        unix socket 协议：每连接一次请求，请求/响应各一行 JSON
internal/daemon     常驻进程：项目、工作区、终端的 RPC 处理；task.go 是多 agent 编排
internal/term       PTY + libghostty-vt 维护屏幕状态；接入时给界面快照和输出流
internal/vt         libghostty-vt 绑定，复制自 MyGo；snapshot.go 逐格生成快照内容
internal/client     CLI 与界面共用的客户端：拉起常驻进程、接入终端（Conn）
internal/store      SQLite（modernc，纯 Go）：项目、工作区、终端记录；task.go 是任务和消息
internal/history    会话历史：读 claude/codex/cursor/grok 的会话文件，增量索引进 $HIVE_HOME/history.db（FTS5），搜索和摘要
internal/gitx       调用系统 git 管理 worktree
internal/agent      agent 启动命令与 hooks 事件到状态的映射
internal/cli        子命令解析与输出（--json 给 agent 用）
internal/app        MyGo 原生窗口：侧栏项目/工作区，标签页终端，底栏内存；只接入当前可见的终端；tasks.go 是任务总览和派发对话框，history.go 是历史页，markdown.go 用 goldmark 解析 agent 回复并搭成界面，usage.go 是终端右侧的"技能与 MCP"面板，uistate.go 是只有界面关心的状态（ui.json），style.go 是配色和公共控件
internal/procmem    读取进程 phys_footprint（proc_pid_rusage）
```

## 关键约定

- 常驻进程持有 `$HIVE_HOME/hived.lock` 的文件锁直到完全退出；socket 和 pid 文件只在持锁时创建和删除。
- 终端内核对程序的终端查询（DA/DSR 等）的应答写回 PTY，由 `Terminal.answer` 控制；界面连接后改由界面应答，避免重复回复。
- `terminal send` 直接写 PTY，不经过终端内核；带 `--enter` 时回车与文本分开发送，否则 TUI 会把回车当成粘贴内容。
- 新终端的环境会去掉 `ORCA_*`、`CLAUDE_CODE_*`、`CLAUDECODE` 等变量，并注入 `HIVE_HOME`、`HIVE_SOCKET`、`HIVE_TERMINAL_ID`、`HIVE_WORKSPACE_ID`；PATH 最前面是 hive 所在目录。
- 工作区三种：`main`（项目目录本身）、`folder`（同一目录上的额外工作区）、`worktree`（git worktree，放在 `$HIVE_HOME/worktrees/<项目>/<名称>`）。
- agent 状态靠启动参数注入的 hooks，不改用户的全局配置：Claude 用 `--settings $HIVE_HOME/claude-hooks.json` 和 `--session-id`；Codex 用 `-c hooks.<事件>=…` 加 `-c hooks.state=…` 预置信任 hash（算法见 `codexHookHash`，Codex 升级后用 `codex app-server` 的 `hooks/list` 核对是否仍为 trusted）。
- `hive hook` 被 agent 同步调用：不得向 stdout 输出（Claude 会把它注入模型上下文），始终 exit 0，不拉起常驻进程。
- 没有 hook 的情况靠终端标题兜底：Claude 按 Esc 中断后标题回到 `✳` 前缀；Codex 等待操作时标题含 `Action Required`。
- 界面接入终端：`terminal.attach` 流式连接，常驻进程先发快照再推实时输出。`vt.Snapshot` 的格子内容由 hive 逐格生成（libghostty 格式化输出会省略末尾空行、并让被跳过的格子沿用前一格样式），模式和光标等状态仍取自格式化输出；快照与输出流在 `smu` 下衔接，不丢不重。界面接入期间由界面应答终端查询。
- 多 agent 编排（`hive task`）：派发方是调用方所在的终端（`HIVE_TERMINAL_ID`），为空表示用户（界面或终端之外）。worker 是新开的 agent 终端，带 `HIVE_TASK_ID`，启动提示由 `workerPrompt` 生成，写明 `hive task ask/check/done` 的用法。`ask/check/done` 只接受 worker 自己的终端，`reply/send/close` 只接受派发方，用户（调用方为空）可以接手任何任务。
- 派发方的收件箱是 question/done/exit 三类消息，worker 的是 answer/note。派发方 agent 空闲（hooks 或标题报告 idle）且 5 秒内没有人键入时，hive 往它终端里输入一行 `Hive: … Run \`hive task inbox\` to read.` 并回车；每条消息只提醒一次。worker 终端或其中的 agent 没汇报就退出时，任务记为 exited 并通知派发方；`task close` 先改状态再关终端，不算意外退出。常驻进程重启时还在进行的任务记为 lost。
- worker 最多嵌套 3 层。`hive task guide` 是给协调方 agent 看的说明（源文件 `internal/cli/task_guide.md`），`--skill` 输出带 frontmatter 的 SKILL.md。
- grok 不注入 hooks（只能写进 `~/.grok/hooks`），状态靠终端标题：盲文转圈符开头是工作中，含 Action Required 是待处理，其余是空闲。
- `workspace merge` 在项目目录以 `--no-ff` 合入 worktree 分支：要求两边已跟踪文件干净、分支有新提交、项目目录在基准分支上；失败时 `merge --abort`。未跟踪文件不挡合并，但 `workspace rm` 仍会因它拒绝删除。
- Hive.app 里的可执行文件叫 `Hive`，agent 在终端里调用 `hive` 能找到它，依赖 APFS 默认的大小写不敏感。
- 会话历史（`hive history`）：
  - 只读各 agent 的会话目录，不写回（唯一的例外：用户在历史页点"移到废纸篓"，由窗口进程把会话文件或会话目录移到 macOS 废纸篓，只认各家会话目录里、路径和会话 ID 对得上的，见 `internal/app/trash.go`）：claude `~/.claude/projects/*/*.jsonl`（不含 subagents）、codex `~/.codex/sessions` 和 `archived_sessions`（标题取 `session_index.jsonl`）、cursor `~/.cursor/projects/*/agent-transcripts/*/*.jsonl`（标题、目录、时间取 `~/.cursor/chats/*/<会话>/meta.json`）、grok `~/.grok/sessions/*/*/chat_history.jsonl`（元数据取同目录 `summary.json`）。
  - 不入库的内容：agent 以用户身份注入的提醒、技能正文、AGENTS.md/环境信息、本地命令输出、压缩摘要、思考过程；不是用户发起的会话（claude/cursor 子 agent、codex 的 guardian/automation、grok 内部的目标规划）。工具调用和输出各截到 3072 个字符。新格式（`history_mode=paginated`）的 codex 会话只读 `event_msg` 的 `item_completed`。
  - 索引更新在子进程 `hive history index --progress` 里做（首次约 6 秒、读几 GB，常驻进程内存不受影响），按文件的 size/mtime 增量读新增的完整行；offset 前的字节对不上就整份重读，文件消失就删除。没有后台轮询：查询时索引超过 15 秒没更新才更新一次，最多等 3 秒，没做完先用已有结果。历史页开着时每 15 秒刷新一次。
  - 中文逐字切开再交给 unicode61 分词（`splitCJK`），中文按子串匹配；英文末尾按前缀匹配；各词 AND，全都没有时退回 OR 并告诉调用方。排序：标题命中优先，再按 bm25（用户 4、助手 2、工具 1）和命中消息数。
  - 只改了某一家的解析规则时改 `parserVersions`（index.go）：只重读这一家的文件，会话 ID 不变，跨版本继续运行的旧常驻进程照常能查。改表结构或分词才改 `schemaVersion`：旧索引清空重建，旧常驻进程会报 ErrStale，要重启它（会断掉所有终端）。
  - grok 的 `chat_history.jsonl` 不带时间：用户消息按 `prompt_index`、回复按开头文字、工具按调用 ID，对上同目录 `updates.jsonl` 里带时间戳的记录；对不上的沿用前一条的时间。
  - `history resume` 在会话目录所在的工作区里开终端，先 `cd` 回原目录（claude 按目录找会话）；同一会话已在 hive 终端里运行时直接返回那个终端。没有对应工作区时报 not_found，界面会问要不要把目录加成项目。
- 界面状态（侧栏项目顺序、上次选中项、右侧面板开关）存在 `$HIVE_HOME/ui.json`，由窗口进程读写，不进常驻进程：改常驻进程的协议要重启它，会断掉所有终端。
- "技能与 MCP"面板由窗口进程增量只读解析当前终端的会话文件（`history.UsageScanner`），路径优先用 hooks 报的 `transcriptPath`，否则按会话 ID 在各家目录里找；只认实际使用的记录（注入的技能说明、读 SKILL.md 的调用、MCP 调用），系统提示里的技能列表不算。cursor 不支持。
- agent 图标（`brandIcon`）：Claude 取自 simple-icons，Codex、Grok、Cursor 取自 LobeHub Icons，都是单色填充路径，放在各家颜色的方块里。
