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
- 改界面后用离屏预览自查（屏幕锁定时也能用）：`HIVE_PREVIEW_DIR=/tmp/hive-preview go test ./internal/app -run TestPreview`，生成主要界面深浅两套的 PNG（清单见 `preview_test.go`）。
- gofmt 用 `$(go env GOROOT)/bin/gofmt`：Homebrew 的 gofmt 版本较旧，对齐规则不同。
- 界面约定见 `internal/app/style.go` 开头和 `docs/features/ui.md`（参照 Claude 桌面端的克制，依据见 `docs/plans/ui-claude-polish.html`）。侧栏是自绘的行（`navRow`），不用 mygo 的 Sidebar：它的选中样式改不了。mygo 会把字重取整到固定档位，别指望细调字重。
- `mygo.json` 用开发证书签名（`macos.signingIdentity` 写证书的 SHA-1：钥匙串里有一张同名的过期证书，写名字 codesign 会报 unknown exception）。临时签名（adhoc）时系统按每次构建的 cdhash 认 app，每打一次包访问桌面、文稿都要重新授权；用证书后按包 ID 加证书认，授权一直有效。正式打包会带强化运行时和时间戳（要联网），libghostty 在包里、同一证书签，照常加载。换证书改这里的 SHA-1（`security find-identity -v -p codesigning`）。
- 应用图标的原图是 `design/icon-art.png`（满版、不带圆角）。桌面端用 Icon Composer 文档 `design/Hive.icon`（`mygo.json` 的 `macos.icon`，里面一层就是这张图，关了玻璃、高光和阴影），由 Xcode 的 actool 编成 Assets.car，圆角由系统画、和别的 App 一样大：自带留白和圆角的图在新版 macOS 上会被缩小放进一块底板。`icon` 指向的 `design/icon.png` 是同一张图，只在编不了 .icon 时用。手机端的 AppIcon、安卓 mipmap 和 `mobile/assets/icon.png` 直接由原图缩放。旧版图标留在 `design/old-icon/`。不要放进 `resources/`：那里的文件会原样打进 app。
- 测试 Hive.app 时用 `open -n --env HIVE_HOME=/tmp/hive-e2e/home build/darwin-arm64/Hive.app` 启动。直接运行包内二进制时，Computer Use 按 bundle id 连接会经 LaunchServices 另起一个不带 `HIVE_HOME` 的实例，误用 `~/.hive`；同时开着两份包时按 .app 路径连接也一样，测试包比常驻进程新时它还会把日常的常驻进程交接到测试包上（用 build 里的包 `hive daemon handoff` 换回来）。
- 内存验收看 `footprint -p <pid>` 的 phys_footprint，不看 RSS：libghostty-vt 压缩回滚后 RSS 不会立即下降。

## 结构

```
cmd/hive            单一二进制：窗口（hive app，或从 Hive.app 无参数启动）、常驻进程（hive daemon run）和 CLI 共用
internal/rpc        unix socket 协议：每连接一次请求，请求/响应各一行 JSON
internal/daemon     常驻进程：项目、工作区、终端的 RPC 处理；task.go 是多 agent 编排
internal/host       常驻进程和手机操作终端的边界（Host、Terminal 接口），包着 internal/term 的进程内实现；升级交接（Pause 导出、Adopt 重建）也在这层，方案见 docs/plans/terminal-host.md
internal/term       PTY + libghostty-vt 维护屏幕状态；接入时给界面快照和输出流
internal/vt         libghostty-vt 绑定，复制自 MyGo 后只留常驻进程用得到的部分（键鼠编码、选区已删，RenderState 只在测试里）；snapshot.go 逐格生成快照内容
internal/client     CLI 与界面共用的客户端：拉起常驻进程、接入终端（Conn）
internal/store      SQLite（modernc，纯 Go）：项目、工作区、终端记录；task.go 是任务和消息
internal/history    会话历史：读 claude/codex/cursor/grok 的会话文件，增量索引进 $HIVE_HOME/history.db（FTS5），搜索和摘要
internal/gitx       调用系统 git：管理 worktree（常驻进程），以及界面的状态、diff、分支、推送、日志、冲突（status.go、branch.go、push.go、log.go、progress.go）
internal/agent      agent 启动命令与 hooks 事件到状态的映射
internal/namer      会话标题（hive session title）：规整 `MMDD｜类型｜摘要`，写 Codex、Grok 自己存标题的地方
internal/cli        子命令解析与输出（--json 给 agent 用）
internal/app        MyGo 原生窗口：侧栏项目/工作区，标签页终端，状态栏用量和内存；只接入当前可见的终端；tasks.go 是任务总览和派发对话框，history.go 是历史页，markdown.go 用 goldmark 解析 agent 回复并搭成界面，usage.go 是终端右侧的"技能与工具"面板，files.go、filesview.go 是文件标签，filespanel.go 是右边的文件栏，gitview.go、gitpanel.go、changepop.go、rollback.go、branchpop.go、compare.go、logview.go、logrefs.go、filehistory.go、gitconsole.go 是 Git 界面，uistate.go 是只有界面关心的状态（ui.json），style.go 是配色和公共控件
internal/procmem    读取进程 phys_footprint（proc_pid_rusage）
internal/usage      各 agent 套餐用量：解析 Claude statusLine 输入和缓存、Codex 会话文件和 app-server、Grok 日志、Cursor 接口，合并后存 $HIVE_HOME/usage.json
internal/files      文件标签的纯逻辑：读写文件（原子保存、磁盘戳）、目录树、文件清单、模糊匹配、全文检索
internal/mobile     让 Orca 手机 App 连到 hive：配对、E2EE v2、它用到的那部分 RPC 和终端二进制流
mobile              Hive 手机客户端（Flutter，用 fvm 固定版本，命令见其中的 README；`third_party/flterm` 是带改动的终端组件，改动见其中的 HIVE.md）
third_party/mygo    带改动的 mygo v0.4.8（go.mod 用 replace 指向它）：终端链接悬停下划线、单击打开，改动和升级方法见其中的 HIVE.md
```

## 关键约定

- 常驻进程持有 `$HIVE_HOME/hived.lock` 的文件锁直到完全退出；socket 和 pid 文件只在持锁时创建和删除。
- 升级交接（`daemon.handoff`，`hive daemon handoff` 手动触发，`internal/daemon/handoff.go`）：旧进程用调用方给的 hive 拉起 `daemon run`（环境 `HIVE_HANDOFF_FD` 指向 socketpair 的一端），停接新连接、停读所有 PTY，经 SCM_RIGHTS 交出 hived.sock 的监听描述符和每个 PTY 主端（前面的交接记录对方没读完时 macOS 的 sendmsg 报 EMSGSIZE 而不是等，`sendFile` 隔 5ms 重发，最多 10 秒），连同交接记录（`term.State`：快照、标题、读到一半的转义序列或 UTF-8 字符 `tail`、agent 状态、hooks 标记）。新进程按同尺寸重建内核、接着读 PTY，用 kqueue（`EVFILT_PROC` `NOTE_EXITSTATUS`）盯不是自己子进程的 pid 拿退出码，再把 PTY 行数改一下又改回来让程序整屏重画；接过来的终端回话成功后才开始读 PTY（`Resume`），失败时输出还留在内核里给旧进程。回话之后新进程马上开始服务（hook 只等 2 秒，不能等旧进程退出），旧进程退出，不关终端、不删 socket 和 pid 文件；新进程在后台一直等锁，拿到才开手机服务（端口在旧进程手里），等不到也不退出。普通启动拿到锁后 socket 还有人应答就退出，免得交接空档里被拉起的进程删掉新进程的 socket。交接的终端和以它们为 worker 的任务不记为 lost。旧进程只把 PTY 已交出去的终端的退出留给新进程（交接窗口里退出的，新进程收不到退出码，记 -1），还没交出去就退出的照常处理；新建终端和交接开始经 `handoffState.startMu` 互斥；交接期间 `terminal wait --for idle` 不算空闲。交接拉起新进程时和 `client.EnsureDaemon` 一样经 `daemon.OpenLog` 重新打开日志、太大先轮转。新进程没回话或报错时旧进程杀掉它、恢复读 PTY 照常运行。协议版本是 `handoffVersion`，改了记录或帧格式要加一。手机看着时终端是手机的尺寸，记录里带上界面的尺寸（`DeskCols`、`DeskRows`），新进程换回去。
- 常驻进程退出（交接或重启）时 `rpc.Server` 先等正在处理的请求写完回复（最多 3 秒），被打断的长等待回 `restarting`（`rpc.Retryable` 也认回复前就断开的连接）。CLI 的 `terminal wait`、`task wait`、`task ask` 走 `doWait`，到新进程上按剩下的时间接着等；`task ask` 的 restarting 带回问题 ID（`Error.Data` 的 `resume`），重发时改成 `--resume`，不会再问一遍。Claude 的权限挂起（`hook.permission.hold`）被打断时 hook 到新进程上再挂，总时间仍是 35 秒。
- PTY 主端是非阻塞、交给 Go 轮询器的描述符（`term.pollable`），交接时靠读期限停下读协程。不要对它调 `Fd()` 或 `pty.Setsize`（经 `Fd()` 改回阻塞模式，读期限就失效了，而且阻塞标志和交出去的描述符共用），改尺寸用 `setPTYSize`、取描述符用 `SyscallConn`。
- `daemon.log` 由拉起常驻进程的一方（`client.EnsureDaemon`、交接时的旧进程，都经 `daemon.OpenLog`）打开后交给它当 stdout/stderr；拉起前超过 5 MB 就改名成 `daemon.log.1`（只留一份），运行期间不轮转。`EnsureDaemon` 拉起后在后台 `Wait`，窗口开着时常驻进程退出不留僵尸进程。
- 程序关掉鼠标跟踪（9、1000、1002、1003 都关）时，如果最后写进 PTY 的是鼠标报告，常驻进程清掉 PTY 里还没读的输入（`dropStaleMouse`，主端 `TIOCFLUSH`）：Codex 退出时先不读输入、最后才关鼠标跟踪，这期间鼠标移动的报告会被接着运行的 shell 读成一串 `35;12;8M`。
- 终端内核对程序的终端查询（DA/DSR 等）的应答写回 PTY，由 `Terminal.answer` 控制；界面连接后改由界面应答，避免重复回复。
- `terminal send` 直接写 PTY，不经过终端内核；带 `--enter` 时回车与文本分开发送，否则 TUI 会把回车当成粘贴内容。
- 新终端的环境会去掉 `ORCA_*`、`CLAUDE_CODE_*`、`CLAUDECODE` 等变量，并注入 `HIVE_HOME`、`HIVE_SOCKET`、`HIVE_TERMINAL_ID`、`HIVE_WORKSPACE_ID`，以及给 Git 操作记录用的 `GIT_TRACE2_EVENT`、`GIT_TRACE2_ENV_VARS`；PATH 最前面是 hive 所在目录。agent 给工具调用设的非交互变量（`NO_COLOR`、`FORCE_COLOR`、`PAGER`、`GIT_EDITOR`、`GROK_*` 等，见 `term.Environ`）也去掉：agent 在终端里用 `open` 拉起 Hive.app 时，窗口和常驻进程会继承它们，Claude Code 看到 `NO_COLOR` 就整屏没颜色。
- 工作区三种：`main`（项目目录本身）、`folder`（同一目录上的额外工作区）、`worktree`（git worktree，放在 `$HIVE_HOME/worktrees/<项目>/<名称>`）。别的工具建的 worktree 用 `attach` 模式加进来时记成 `folder`（路径是那个 worktree），删工作区不碰它的目录和分支。
- agent 状态靠启动参数注入的 hooks，不改用户的全局配置：Claude 用 `--settings $HIVE_HOME/claude-hooks.json` 和 `--session-id`；Codex 用 `-c hooks.<事件>=…` 加 `-c hooks.state=…` 预置信任 hash（算法见 `codexHookHash`，Codex 升级后用 `codex app-server` 的 `hooks/list` 核对是否仍为 trusted）；Cursor 用 `--plugin-dir $HIVE_HOME/cursor-plugin`，hooks 放在 hive 生成的插件里（`internal/agent/cursor.go`），不写 `~/.cursor/hooks.json`。
- `hive hook` 被 agent 同步调用：不得向 stdout 输出（Claude 会把它注入模型上下文）。例外有三处：Cursor 要从 stdout 读一个 JSON 回应，hive 输出 `{}` 表示不干预；Claude 的 PermissionRequest 在手机给出 allow 或 deny 时向 stdout 写决策 JSON，没有决定就不输出；Claude 的 UserPromptSubmit 在有待交的会话标题时写 `hookSpecificOutput.sessionTitle`。始终 exit 0，不拉起常驻进程。
- 没有 hook 的情况靠终端标题兜底：Claude 按 Esc 中断后标题回到 `✳` 前缀；Codex 等待操作时标题含 `Action Required`。
- 界面接入终端：`terminal.attach` 流式连接，常驻进程先发快照再推实时输出。`vt.Snapshot` 的格子内容由 hive 逐格生成（libghostty 格式化输出会省略末尾空行、并让被跳过的格子沿用前一格样式），模式和光标等状态仍取自格式化输出；快照与输出流在 `smu` 下衔接，不丢不重；快照后面接上内核已经读进、还没写完的转义序列或 UTF-8 字符（`tail`，和交接带过去的是同一份），PTY 读到一半时接入，接入方的解析器也和内核停在同一处。界面接入期间由界面应答终端查询。行数变多而光标不在最后一行时（libghostty 只会在底部加空行），常驻进程等尺寸稳定 150ms 后重建内核、去掉底部多出来的空行让内容贴着底部（`term.pullBack`），然后断开所有接入者（`Subscriber.Kicked`）：它们按自己当下的尺寸重新接入拿新快照，不在输出流里广播快照（拖窗口时送达时尺寸可能已经变了）。
- 所有子命令都认 `-h` / `--help`（在 `--` 之前），打印这条命令的用法：agent 常先这样试。
- `workspace merge` 在项目目录以 `--no-ff` 合入 worktree 分支：要求两边已跟踪文件干净、分支有新提交、项目目录在基准分支上；失败时 `merge --abort`。未跟踪文件不挡合并，但 `workspace rm` 仍会因它拒绝删除。
- Hive.app 里的可执行文件叫 `Hive`，agent 在终端里调用 `hive` 能找到它，依赖 APFS 默认的大小写不敏感。
- 菜单由 `installMenu` 自己装：macOS 先把按键交给菜单，mygo 默认菜单的"关闭窗口"占着 ⌘W（界面收不到，关最后一个标签会连 app 一起关掉），View 菜单占着 ⌘=、⌘-、⌘0。⌘W 是"关闭标签"（`closeTab`），关窗口是 ⇧⌘W。
- 界面状态（侧栏项目顺序、上次选中项、右侧面板开关）存在 `$HIVE_HOME/ui.json`，由窗口进程读写，不进常驻进程：改常驻进程的协议要换掉它，`hive daemon handoff` 交接不断终端，重启会断掉所有终端。
- 窗口所在的 Hive.app 被换掉（重新打包、覆盖安装）后，系统核实不了窗口进程的代码身份，TCC 直接拒绝它读桌面、文稿等目录（EPERM，不弹授权）。窗口按 inode 每秒查一次自己的可执行文件（`internal/app/relaunch.go`），变了就在侧栏底部提示重开；重开先处理未保存的文件，再退出，由后台脚本等进程退出后带上 `HIVE_*` 环境 `open -n` 新包。终端不受影响。
- 界面每秒轮询常驻进程，数据和上次相同就不重建界面；只有连不上 socket 时才去拉起常驻进程。靠时间推进的内容（右侧面板的定时刷新、"几分钟前"）要在视图里用 `c.After` 约下一帧，不能指望每秒重建。操作失败的报错留在错误条上，等用户关掉。
- 功能说明按模块放在 `docs/features/`：`agents.md`（agent 识别、手动启动、编排、会话标题、权限）、`history.md`、`files.md`、`git.md`、`mobile.md`、`usage.md`（套餐用量、防休眠）、`ui.md`（界面各部分）。改了功能同步更新对应的文档。
- 改了 `$HIVE_HOME/bin` 的转发脚本、shell 集成文件，或常驻进程注入 agent 的参数（hooks、技能插件、`developer_instructions`、`--rules`），要重启常驻进程，之后新开的终端才生效。Codex 的 `-c developer_instructions` 会整个替换用户 `config.toml` 里的同名设置，所以用户设了它时不注入。
- 会话历史只读各 agent 的会话目录，不写回（例外：会话标题、用户在历史页移到废纸篓）。只改了某一家的解析规则改 `parserVersions`（index.go），只重读这一家；改表结构或分词才改 `schemaVersion`（旧索引重建，跨版本的旧常驻进程报 ErrStale，要重启）。改粗筛规则（`wants`）要跑 `TestLineFilterSameResult`。索引只有一个连接，结果集没关之前不能再查。
- 文件栏不用 fsnotify（macOS 上基于 kqueue，监听一个目录要为其中每个文件开一个描述符），看得见时每秒 stat 一次。保存前磁盘戳变了先问，不默默覆盖 agent 的修改。
- Git 界面全部在窗口进程里调系统 git，常驻进程不参与：路径都相对仓库根目录，仓库根目录换成工作区路径的写法（`repoRoot`，git 给的是解析过符号链接的）；界面调用的 gitx 函数用 `runRaw`；gitx 自己调 git 时设 `GIT_TRACE2_EVENT=0`。强制推送一律 `--force-with-lease --force-if-includes`。
- 手机遥控的端到端测试用 `HIVE_MOBILE_PORT` 换端口，免得和日常的常驻进程冲突；主机密钥在 `$HIVE_HOME/mobile.json`，换密钥等于所有手机重新配对。
- 菜单都是 Hive 自绘（`menu.go`）：按钮弹出用 `menuButton`，右键用 `contextMenu`（mygo 的 `ContextMenuAsked` 沿用它的右键路由，只报位置），终端内容区由插件的 `Options.ContextMenu` 交来位置；不用 mygo 的 `Menu`、`ContextMenu`（系统 NSMenu，悬停高亮改不了）。build 每帧先跑一遍收集菜单项，点了某项后同一帧再跑一遍报告 `Chosen`，两遍加的项要一样。"在访达中显示/打开"一律用 `iconFolder`。mygo 自己的菜单（文字输入框、可选文字、编辑器的编辑菜单）经 `Context.PresentMenus` 交给 Hive（`presentMenu`、`sysMenuView`），菜单项的英文在 `menuLabels` 里换成中文，mygo 加了新的菜单项要补上。能点的元素主题已给手形指针，新控件不用单独设 `Cursor(ui.CursorPointer)`；只为挡住点击才调 `Clicked` 的大容器要设 `Cursor(ui.CursorDefault)`。
