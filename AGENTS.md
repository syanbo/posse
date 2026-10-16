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
- 改界面后用离屏预览自查（屏幕锁定时也能用）：`HIVE_PREVIEW_DIR=/tmp/hive-preview go test ./internal/app -run TestPreview`，生成深浅两套主界面、空工作区、首次使用、新建对话框、任务总览、派发对话框、历史页（含 Markdown 回复）、文件标签（含检索、Markdown 预览、快速打开）的 PNG。
- gofmt 用 `$(go env GOROOT)/bin/gofmt`：Homebrew 的 gofmt 版本较旧，对齐规则不同。
- 界面约定见 `internal/app/style.go` 开头，参照 Claude 桌面端的克制（依据见 `docs/plans/ui-claude-polish.html`）：暖灰底色分侧栏、顶栏、画布、卡片、浮层几层，靠明度差和 8% 透明细线分层；单闭合边框（有框的容器里按钮和输入不再加框）；陶土色给主按钮、焦点、工作中的星号、链接和图标，选中项是强调色淡底加左侧竖条（历史里自己的消息是靠右的强调色淡底，不加竖条），悬停是中性透明底；侧栏的工作区两行（名称，下面是路径或分支）；只有"待处理"用琥珀色加文字；状态用蜂巢格形状区分（实心工作中、空心带点待处理、空心完成）；不用"A · B"中点串拼接信息；字号和圆角用 style.go 里的常量，字重三档（只有 `titleText` 用 600）；框里的次级按钮用 `textButton`（填充底），框里的输入用 `fieldInput`，发送类输入用 `composer`（一体输入区，⌘↩ 发送），可滚动列表用 `fadeEdges` 提示还有内容。mygo 会把字重取整到固定档位（470 和 500 画出来一样），别指望细调字重。每帧开头 `SetTheme(hiveTheme(...))` 换成暖灰、Claude 橙（深色 #D97757、浅色 #C15F3C）和 8px 圆角，终端底色经 `terminalThemes` 跟卡片一致。侧栏是自绘的行（`navRow`），不用 mygo 的 Sidebar：它的选中样式改不了。
- `mygo.json` 用开发证书签名（`macos.signingIdentity` 写证书的 SHA-1：钥匙串里有一张同名的过期证书，写名字 codesign 会报 unknown exception）。临时签名（adhoc）时系统按每次构建的 cdhash 认 app，每打一次包访问桌面、文稿都要重新授权；用证书后按包 ID 加证书认，授权一直有效。正式打包会带强化运行时和时间戳（要联网），libghostty 在包里、同一证书签，照常加载。换证书改这里的 SHA-1（`security find-identity -v -p codesigning`）。
- 应用图标是 `design/icon.png`（由 `design/icon-art.png` 套 macOS 图标模板的圆角和投影得到），`mygo.json` 的 `icon` 指向它。不要放进 `resources/`：那里的文件会原样打进 app。
- 测试 Hive.app 时用 `open -n --env HIVE_HOME=/tmp/hive-e2e/home build/darwin-arm64/Hive.app` 启动。直接运行包内二进制时，Computer Use 按 bundle id 连接会经 LaunchServices 另起一个不带 `HIVE_HOME` 的实例，误用 `~/.hive`。
- 内存验收看 `footprint -p <pid>` 的 phys_footprint，不看 RSS：libghostty-vt 压缩回滚后 RSS 不会立即下降。

## 结构

```
cmd/hive            单一二进制：窗口（hive app，或从 Hive.app 无参数启动）、常驻进程（hive daemon run）和 CLI 共用
internal/rpc        unix socket 协议：每连接一次请求，请求/响应各一行 JSON
internal/daemon     常驻进程：项目、工作区、终端的 RPC 处理；task.go 是多 agent 编排
internal/term       PTY + libghostty-vt 维护屏幕状态；接入时给界面快照和输出流
internal/vt         libghostty-vt 绑定，复制自 MyGo 后只留常驻进程用得到的部分（键鼠编码、选区已删，RenderState 只在测试里）；snapshot.go 逐格生成快照内容
internal/client     CLI 与界面共用的客户端：拉起常驻进程、接入终端（Conn）
internal/store      SQLite（modernc，纯 Go）：项目、工作区、终端记录；task.go 是任务和消息
internal/history    会话历史：读 claude/codex/cursor/grok 的会话文件，增量索引进 $HIVE_HOME/history.db（FTS5），搜索和摘要
internal/gitx       调用系统 git：管理 worktree（常驻进程），以及界面的状态、diff、分支、推送、日志、冲突（status.go、branch.go、push.go、log.go、progress.go）
internal/agent      agent 启动命令与 hooks 事件到状态的映射
internal/cli        子命令解析与输出（--json 给 agent 用）
internal/app        MyGo 原生窗口：侧栏项目/工作区，标签页终端，底栏内存；只接入当前可见的终端；tasks.go 是任务总览和派发对话框，history.go 是历史页，markdown.go 用 goldmark 解析 agent 回复并搭成界面，usage.go 是终端右侧的"技能与 MCP"面板，files.go、filesview.go 是文件标签，filespanel.go 是右边的文件栏，gitview.go、gitpanel.go、branchpop.go、compare.go、logview.go 是 Git 界面，uistate.go 是只有界面关心的状态（ui.json），style.go 是配色和公共控件
internal/procmem    读取进程 phys_footprint（proc_pid_rusage）
internal/files      文件标签的纯逻辑：读写文件（原子保存、磁盘戳）、目录树、文件清单、模糊匹配、全文检索
internal/mobile     让 Orca 手机 App 连到 hive：配对、E2EE v2、它用到的那部分 RPC 和终端二进制流
third_party/mygo    带改动的 mygo v0.4.8（go.mod 用 replace 指向它）：终端链接悬停下划线、单击打开，改动和升级方法见其中的 HIVE.md
```

## 关键约定

- 常驻进程持有 `$HIVE_HOME/hived.lock` 的文件锁直到完全退出；socket 和 pid 文件只在持锁时创建和删除。
- 终端内核对程序的终端查询（DA/DSR 等）的应答写回 PTY，由 `Terminal.answer` 控制；界面连接后改由界面应答，避免重复回复。
- `terminal send` 直接写 PTY，不经过终端内核；带 `--enter` 时回车与文本分开发送，否则 TUI 会把回车当成粘贴内容。
- 新终端的环境会去掉 `ORCA_*`、`CLAUDE_CODE_*`、`CLAUDECODE` 等变量，并注入 `HIVE_HOME`、`HIVE_SOCKET`、`HIVE_TERMINAL_ID`、`HIVE_WORKSPACE_ID`；PATH 最前面是 hive 所在目录。
- 工作区三种：`main`（项目目录本身）、`folder`（同一目录上的额外工作区）、`worktree`（git worktree，放在 `$HIVE_HOME/worktrees/<项目>/<名称>`）。
- agent 状态靠启动参数注入的 hooks，不改用户的全局配置：Claude 用 `--settings $HIVE_HOME/claude-hooks.json` 和 `--session-id`；Codex 用 `-c hooks.<事件>=…` 加 `-c hooks.state=…` 预置信任 hash（算法见 `codexHookHash`，Codex 升级后用 `codex app-server` 的 `hooks/list` 核对是否仍为 trusted）；Cursor 用 `--plugin-dir $HIVE_HOME/cursor-plugin`，hooks 放在 hive 生成的插件里（`internal/agent/cursor.go`），不写 `~/.cursor/hooks.json`。
- `hive hook` 被 agent 同步调用：不得向 stdout 输出（Claude 会把它注入模型上下文），唯一例外是 Cursor：它要从 stdout 读一个 JSON 回应，hive 输出 `{}` 表示不干预；始终 exit 0，不拉起常驻进程。
- 没有 hook 的情况靠终端标题兜底：Claude 按 Esc 中断后标题回到 `✳` 前缀；Codex 等待操作时标题含 `Action Required`。
- 界面接入终端：`terminal.attach` 流式连接，常驻进程先发快照再推实时输出。`vt.Snapshot` 的格子内容由 hive 逐格生成（libghostty 格式化输出会省略末尾空行、并让被跳过的格子沿用前一格样式），模式和光标等状态仍取自格式化输出；快照与输出流在 `smu` 下衔接，不丢不重。界面接入期间由界面应答终端查询。
- 多 agent 编排（`hive task`）：派发方是调用方所在的终端（`HIVE_TERMINAL_ID`），为空表示用户（界面或终端之外）。worker 是新开的 agent 终端，带 `HIVE_TASK_ID`，启动提示由 `workerPrompt` 生成，写明 `hive task ask/check/done` 的用法。`ask/check/done` 只接受 worker 自己的终端，`reply/send/close` 只接受派发方，用户（调用方为空）可以接手任何任务。
- 派发方的收件箱是 question/done/exit 三类消息，worker 的是 answer/note。派发方 agent 空闲（hooks 或标题报告 idle）且 5 秒内没有人键入时，hive 往它终端里输入一行 `Hive: … Run \`hive task inbox\` to read.` 并回车；每条消息只提醒一次。worker 终端或其中的 agent 没汇报就退出时，任务记为 exited 并通知派发方；`task close` 先改状态再关终端，不算意外退出。常驻进程重启时还在进行的任务记为 lost。
- worker 最多嵌套 3 层。`hive task guide` 是给协调方 agent 看的说明（源文件 `internal/cli/task_guide.md`，测试限制在 80 行以内），`--skill` 输出带 frontmatter 的 SKILL.md。`hive task retry` 在 CLI 里组合 show 和 start，用原任务的说明、agent 和工作区重派，只接受 failed、exited、lost、stopped 的任务。
- 编排技能 `hive-tasks` 随 agent 启动按会话注入，不写全局目录，正文都指向 `hive task guide`（技能文本在 `internal/agent/skill.go`）：Claude 加 `--plugin-dir $HIVE_HOME/claude-plugin`（hive 生成的插件，只含这个技能，内容没变不重写）；Codex 和 Grok 没有按会话加载技能的办法，分别用 `-c developer_instructions=…` 和 `--rules` 放一句指路。`-c` 会整个替换 developer_instructions，用户的 `config.toml` 里设了它时不注入。这些参数由常驻进程生成，改了要重启常驻进程，之后新开的 agent 才带上。
- 所有子命令都认 `-h` / `--help`（在 `--` 之前），打印这条命令的用法：agent 常先这样试。
- grok 不注入 hooks（只能写进 `~/.grok/hooks`），状态靠终端标题：盲文转圈符开头是工作中，含 Action Required 是待处理，其余是空闲。
- Cursor（`cursor-agent`）的状态：beforeSubmitPrompt 和工具事件是工作中，stop 是空闲，sessionEnd 是退出；sessionStart 不改状态（命令行带初始提示时第一轮不发 beforeSubmitPrompt）。会话 ID 不能预先指定，由 hook 报上来。Cursor 没有"等待批准"的事件，标题只是会话名，待处理目前认不出来。
- `workspace merge` 在项目目录以 `--no-ff` 合入 worktree 分支：要求两边已跟踪文件干净、分支有新提交、项目目录在基准分支上；失败时 `merge --abort`。未跟踪文件不挡合并，但 `workspace rm` 仍会因它拒绝删除。
- Hive.app 里的可执行文件叫 `Hive`，agent 在终端里调用 `hive` 能找到它，依赖 APFS 默认的大小写不敏感。
- 会话历史（`hive history`）：
  - 只读各 agent 的会话目录，不写回（唯一的例外：用户在历史页点"移到废纸篓"，由窗口进程把会话文件或会话目录移到 macOS 废纸篓，只认各家会话目录里、路径和会话 ID 对得上的，见 `internal/app/trash.go`）：claude `~/.claude/projects/*/*.jsonl`（不含 subagents）、codex `~/.codex/sessions` 和 `archived_sessions`（标题取 `session_index.jsonl`）、cursor `~/.cursor/projects/*/agent-transcripts/*/*.jsonl`（标题、目录、时间取 `~/.cursor/chats/*/<会话>/meta.json`）、grok `~/.grok/sessions/*/*/chat_history.jsonl`（元数据取同目录 `summary.json`）。
  - 不入库的内容：agent 以用户身份注入的提醒、技能正文、AGENTS.md/环境信息、本地命令输出、压缩摘要、思考过程；不是用户发起的会话（claude/cursor 子 agent、codex 的 guardian/automation、grok 内部的目标规划）。工具调用和输出各截到 3072 个字符。新格式（`history_mode=paginated`）的 codex 会话只读 `event_msg` 的 `item_completed`。
  - 索引更新在子进程 `hive history index --progress` 里做（首次约 6 秒、读几 GB，常驻进程内存不受影响），按文件的 size/mtime 增量读新增的完整行；offset 前的字节对不上就整份重读，文件消失就删除。没有后台轮询：查询时索引超过 15 秒没更新才更新一次，最多等 3 秒，没做完先用已有结果。历史页开着时每 15 秒刷新一次。
  - 中文逐字切开再交给 unicode61 分词（`splitCJK`），中文按子串匹配；英文末尾按前缀匹配；各词 AND，全都没有时退回 OR 并告诉调用方。排序：标题命中优先，再按 bm25（用户 4、助手 2、工具 1）和命中消息数。
  - 只改了某一家的解析规则时改 `parserVersions`（index.go）：只重读这一家的文件，会话 ID 不变，跨版本继续运行的旧常驻进程照常能查。改表结构或分词才改 `schemaVersion`：旧索引清空重建，旧常驻进程会报 ErrStale，要重启它（会断掉所有终端）。
  - grok 的 `chat_history.jsonl` 不带时间：用户消息按 `prompt_index`、回复按开头文字、工具按调用 ID，对上同目录 `updates.jsonl` 里带时间戳的记录；对不上的沿用前一条的时间。增量更新时从状态里记的位置（`GrokUpdates`，最后对上的回复分段的行尾）接着读 `updates.jsonl`。
  - 解析前先按各家 parser 的 `wants` 粗筛（只认 `"type"` 等字段的值，冒号后有空格也能匹配），无关的行不做 JSON 解码；改粗筛规则要跑 `TestLineFilterSameResult`。
  - `history show` 的 `--tail`、`--tools` 由常驻进程在 SQL 里筛选，`--json` 输出同样生效。
  - `history resume` 在会话目录所在的工作区里开终端，先 `cd` 回原目录（claude 按目录找会话）；同一会话已在 hive 终端里运行时直接返回那个终端。没有对应工作区时报 not_found，界面会问要不要把目录加成项目。
- 文件：照 Orca 分两处，全部在窗口进程里，不经过常驻进程。
  - 文件栏（`filespanel.go`）停靠在主区域最右边，分段切换文件树、检索、改动；它挤窄标签栏和终端（编辑器），不浮在上面、点别处也不收起，和"图片 / 技能与 MCP / 历史"这些浮层面板分开（开关是 ui.json 的 `filesPanel`，旧版记在 `rightPanel: files` 的读进来自动改过来）。开关按钮照 Orca 固定在右上角：关着时在标签栏最右边，开着时在文件栏顶栏最右边，位置不变。打开、收起时文件栏从右边缘滑进滑出（200ms），终端只在打开开始、收起结束时改一次宽度，免得里面的程序跟着动画反复重排。宽度存在 `filesPanelWidth`，所有工作区共用；拖左边缘调宽（220–640），松手才写文件。快捷键：⌘L 开关，⇧⌘E 文件树、⇧⌘G 改动（已经在那一页时关掉；编辑器有焦点时让给它的"查找上一个"），⇧⌘F 检索，⌘K 提交。
  - 打开的文件照 Orca 是标签栏里的标签，排在终端标签后面（`docTabs`），带未保存的圆点。文件树里单击打开的是预览标签（斜体，`fileDoc.transient`），下一次单击换掉它；双击文件或标签、从别处打开（⌘P、检索、改动、链接）、改了内容都会常驻。`activeTerm[工作区]` 为 `filesTabID` 时选中的是文件（哪个看 `filesView.cur`），`currentTerminal` 返回 nil，终端断开接入。没有终端的工作区也能开。⌘P 快速打开（`路径:行号` 跳行，不离开终端，选中后才切过来），⌘S 保存，⌘W 关当前文件，关掉最后一个就回到之前的终端。
  - 编辑区是 mygo 的 `ui/editor`（行号、高亮、⌘F 查找替换都是它的）。未保存判断用 `editor.Buffer.Modified`，按缓冲区版本缓存；不能拿版本号判断，撤销也会让版本号加一。超过 5 MB 只读，超过 50 MB 或二进制不读内容。
  - 不用 fsnotify：macOS 上它基于 kqueue，监听一个目录要为其中每个文件各开一个描述符。改为文件栏或文件标签看得见时每秒 stat 一次展开的目录和打开的文件，磁盘戳带 inode，"写临时文件再改名"的保存也认得出。磁盘上变了：没有未保存的改动就重载，有就显示冲突横幅；文件没了标出已删除。
  - 保存在同一目录写临时文件再改名，保留权限位，符号链接写到它指向的文件；保存前磁盘戳和打开时不一样就先问，不默默覆盖 agent 的修改。有未保存的文件时 `Window.OnClose` 拦住关窗口和退出（退出会先关窗口）。
  - 变暗（被 gitignore 忽略）、快速打开、检索共用一份 `git ls-files -co --exclude-standard` 的清单（不是 git 仓库时遍历目录，跳过 node_modules 等），超过 10 秒就重列，文件栏和文件标签都看不见时放掉。检索在后台逐个读清单里的文件，每次检索带编号、旧的结果丢掉，有未保存改动的文件搜编辑器里的内容，最多 2000 行。
  - 打开的文件、当前文件和展开的目录按工作区存在 ui.json 的 `files`；`saveState` 把它深拷贝后交给写文件的协程。后台读文件的结果经 `a.update` 交回界面线程，测试里没有窗口时排到下一帧开头执行。
- 菜单由 `installMenu` 自己装：macOS 先把按键交给菜单，mygo 默认菜单的"关闭窗口"占着 ⌘W（界面收不到，关最后一个标签会连 app 一起关掉），View 菜单占着 ⌘=、⌘-、⌘0。⌘W 是"关闭标签"（`closeTab`），关窗口是 ⇧⌘W。
- Git 界面照 WebStorm 的用法做（依据和取舍见 `docs/plans/git-ops.html`），全部在窗口进程里调用系统 git，常驻进程不参与：
  - 路径都相对仓库根目录；仓库根目录换成工作区路径的写法（`repoRoot`），git 给的是解析过符号链接的（如 `/private/var`），混用会把同一个文件当成两个。界面调用的 gitx 函数用 `runRaw`（原样输出、`LC_ALL=C`、失败返回 `*gitx.Error`，`Msg` 是 git 自己的报错）。
  - 文件栏"改动"：文件栏或文件标签看得见时每 1.5 秒读一次 `status --porcelain=v2`，不用手动刷新；照 WebStorm 不分暂存和未暂存：每个文件一个勾选框（已跟踪的默认勾、新文件默认不勾），提交勾选的文件——新文件先 `add`，再 `commit --only -- <文件>`，暂存区里没勾的原样留着。合并、拣选、还原进行中 git 不许部分提交，这时按钮是"提交合并结果"：勾选的先 `add`，再整体提交。编辑器行号旁标出相对 HEAD 的改动（`State.DiffBase`），⌥⌘Z 回滚一段。
  - 图片（png/jpg/gif/webp）的 diff 左右对照两张图（`imageDiffView`），各标版本、像素尺寸和文件大小，透明处垫棋盘格；新增的一边写新文件、删除的写已删除。
  - diff 标签页用 mygo 的 `editor.DiffView`，默认左右对照、忽略空白（`third_party/mygo` 加的 `IgnoreWhitespace`，只忽略空白，不忽略空行）。`diffSpec` 的版本：newRev 空是工作区、`":"` 是暂存区，oldRev 空是"这一边没有"。F7 / ⇧F7 跨文件跳改动，F4 跳到编辑器；工作区 diff 的逐段回滚立刻写回磁盘。diff、比较、日志、Annotate 是 `virtual` 标签页，不轮询磁盘、不记进 ui.json。
  - 分支弹窗：点标签栏的分支名，⌥⌘N 新建分支，⇧⌘K Push（WebStorm 的 ⌘T 更新项目和 hive 的新建终端冲突，放进弹窗）。更新当前分支是 `pull --rebase --autostash`，其他分支只快进。被另一个 worktree 检出的分支，检出改成切到那个工作区；"基于它新建工作区"走 hive 的 worktree 工作区。检出、rebase、合并、更新、reset 之类改动工作目录的操作，工作区里有 agent 正在工作时先确认（`guardAgent`）。
  - 自动 fetch：分支按钮画出来时，按项目每 5 分钟在后台 `fetch --all` 一次（打开后先拉一次），同一项目的 worktree 共用计时；失败不报错，下次再试。进行中占着改动页的 busy，不和手动 git 操作同时改引用。传入、传出提交数每 10 秒只用本地的远端引用算。自动、手动 fetch 和其他 git 操作之后，同一项目里开着的分支比较都会重读（`reloadCompares`）。
  - Git 日志：标签栏右侧常驻入口（分支按钮旁）。左边分支树照 WebStorm 分 HEAD、本地、远程（按远端分组）、标签，点一项只看它的提交，再点一次回到全部分支；分支和标签随分支弹窗一起读（`loadBranches`）。
  - 强制推送一律 `--force-with-lease`。改提交信息、和上一个提交合并、删除提交用交互式 rebase，hive 生成脚本改写待办列表和提交信息（`rebaseTodo`），不打开编辑器；范围里有合并提交时拒绝。rebase、合并等停在冲突上时，改动列表顶部有继续 / 跳过 / 中止的横幅，冲突文件可以整个采用一方（rebase 时 ours 是目标分支）。
- 界面状态（侧栏项目顺序、上次选中项、右侧面板开关）存在 `$HIVE_HOME/ui.json`，由窗口进程读写，不进常驻进程：改常驻进程的协议要重启它，会断掉所有终端。
- 底栏的内存按钮打开浮窗：hive 自己（常驻进程、窗口）和各工作区终端里的程序。终端按 `terminalInfo.Pid` 统计整棵进程树的 phys_footprint（`procmem.TreeFootprint`，子进程关系取自 sysctl `kern.proc.all`），浮窗开着时每 3 秒按工作区算一次；底栏显示的是总内存（hive 自己加终端），终端合计在每秒的轮询里每 5 秒算一次，按 MB 变了才重建界面。都不经过常驻进程。点终端展开进程明细（`memBreakdown`）：shell、agent 本体，和 agent 拉起的每棵子进程（MCP 服务等），名字取自 argv（`procmem.Name`，跳过 npm、node、python 这类启动器；npm 会把整条命令改写进第一个参数）。
- "技能与 MCP"面板由窗口进程增量只读解析当前终端的会话文件（`history.UsageScanner`），路径优先用 hooks 报的 `transcriptPath`，否则按会话 ID 在各家目录里找；只认实际使用的记录（注入的技能说明、读 SKILL.md 的调用、MCP 调用），系统提示里的技能列表不算。面板顶上是每个技能、MCP 服务的次数，下面是时间线（`Usage.Events`，取每行的 timestamp，grok、cursor 的会话文件没有时间），连着调同一个工具合成一项。
- agent 图标（`brandIcon`）：Claude 取自 simple-icons，Codex、Grok、Cursor 取自 LobeHub Icons，都是单色填充路径，放在各家颜色的方块里。
- 终端里的链接：鼠标放上去有下划线和手形指针，原地单击或 Cmd+点击打开，拖动是选文字（改在 `third_party/mygo` 的终端插件里）；指向不存在文件的链接不显示下划线、点不了。终端环境带 `FORCE_HYPERLINK=1`（和 Orca 一样），Claude Code 才会把 `[Image #N]` 输出成超链接。网址交给默认浏览器；文件链接（如 Claude 的 `[Image #N]` 指向的 `file://`）和路径弹出链接卡片（`internal/app/links.go`）。
- 界面每秒轮询常驻进程，数据和上次相同就不重建界面；只有连不上 socket 时才去拉起常驻进程。靠时间推进的内容（右侧面板的定时刷新、"几分钟前"）要在视图里用 `c.After` 约下一帧，不能指望每秒重建。操作失败的报错留在错误条上，等用户关掉。
- 手机遥控（`internal/mobile`）复用 Orca 的手机 App（App Store / GitHub 上的 APK），hive 扮演 Orca 桌面端，协议照 stablyai/orca 的 `mobile/` 和 `src/shared/` 实现：
  - 只做局域网/Tailscale 直连（Orca 的 local-only），不做云端中转和推送（推送只能经 Orca 的网关）。手机 App 在前台时每 3 秒轮询 `worktree.ps`，靠它看到"待处理"。
  - 常驻进程在 `0.0.0.0:6788` 开明文 WebSocket（避开 Orca 的 6768，两边可同时开），安全靠 E2EE v2（X25519 + HKDF + secretbox，`e2ee_test.go` 有 Orca 的测试向量）和设备 token。没有配对过的手机时不监听，生成配对码时才开始。端到端测试用 `HIVE_MOBILE_PORT` 换端口，免得和日常的常驻进程冲突。
  - 主机密钥和配对的手机存在 `$HIVE_HOME/mobile.json`（0600）。手机按公钥认主机，换密钥等于所有手机重新配对。配对码在窗口底栏的手机图标、"新建"菜单或 `hive mobile pair`。
  - 映射：项目 → repo，工作区 → worktree（ID 是 `<项目 ID>::<工作区 ID>`），运行中的终端 → 标签（手机只认 UUID 形状的 leaf，由终端 ID 派生）。agent 状态 waiting → `waiting`（行状态 `permission`），working/starting → `working`，idle → `done`。
  - 手机看终端时终端按手机屏幕排版（`Terminal.HoldSize`），界面这时的尺寸变化先记下，手机全部离开后恢复；手机接入不接管终端查询的应答（`Watch`）。终端输出走 Orca 的 16 字节头二进制帧，按 UTF-8 字符边界切块。
  - 手机上传的图片存成 `$TMPDIR/hive-mobile-*`，App 再把路径输入终端。
