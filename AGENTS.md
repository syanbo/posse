# posse

多 agent 终端管理器，替代 Orca。目标是低内存：一个 Go 常驻进程托管所有终端，界面可以随时关闭。

## 构建与测试

```sh
export GOPROXY=https://goproxy.cn,direct   # 本机直连 proxy.golang.org / storage.googleapis.com 会超时
go build -o bin/posse ./cmd/posse
go vet ./... && go test ./... && go tool mygo vet
go tool mygo build -platform darwin/arm64 -skip-dmg   # 打包 build/darwin-arm64/Posse.app
```

- go.mod 要求 Go 1.27.1（MyGo 的要求），本机 Go 1.26 会通过 GOTOOLCHAIN=auto 自动下载。
- 终端内核 libghostty-vt 首次运行时由 MyGo 下载到 `~/Library/Caches/mygo/natives/`。
- 端到端测试不要碰 `~/.posse`。只测 CLI 和常驻进程、不涉及系统授权时用 `POSSE_HOME=/tmp/posse-e2e/home ./bin/posse ...`，编得最快；单元测试和离屏预览不起 App。
- 改界面后用离屏预览自查（屏幕锁定时也能用）：`POSSE_PREVIEW_DIR=/tmp/posse-preview go test ./internal/app -run TestPreview`，生成主要界面深浅两套的 PNG（清单见 `preview_test.go`）。
- gofmt 用 `$(go env GOROOT)/bin/gofmt`：Homebrew 的 gofmt 版本较旧，对齐规则不同。
- 写界面前读 `docs/design.md`（取向和字号、颜色、间距等规则，只用 `style.go` 里的名字，不写数字），各界面的行为见 `docs/features/ui.md`。侧栏是自绘的行（`navRow`），不用 mygo 的 Sidebar：它的选中样式改不了。mygo 会把字重取整到固定档位，别指望细调字重。
- `mygo.json` 用 Developer ID Application 证书签名（团队 QKSM9Z4U85；`macos.signingIdentity` 写证书的 SHA-1：钥匙串里有一张同名的过期证书，写名字 codesign 会报 unknown exception）。临时签名（adhoc）时系统按每次构建的 cdhash 认 app，每打一次包访问桌面、文稿都要重新授权；用证书后按包 ID 加证书认，授权一直有效。正式打包会带强化运行时和时间戳（要联网），libghostty 在包里、同一证书签，照常加载。换证书改这里的 SHA-1（`security find-identity -v -p codesigning`）。
- 应用图标的原图是 `design/icon-art.png`（满版、不带圆角），改图标改它和下面几处。桌面端用 Icon Composer 文档 `design/Posse.icon`（`mygo.json` 的 `macos.icon`，里面一层就是这张图，关了玻璃、高光和阴影），由 actool 编成 Assets.car、圆角由系统画：自带留白和圆角的图在新版 macOS 上会被缩小放进一块底板。`icon` 指向的 `design/icon.png` 是同一张图，只在编不了 .icon 时用；手机端的 AppIcon、安卓 mipmap 和 `mobile/assets/icon.png` 由原图缩放。旧版图标留在 `design/old-icon/`。不要放进 `resources/`：那里的文件会原样打进 app。
- 要把 App 跑起来的测试（窗口、Computer Use、看桌面、屏幕录制和辅助功能这类按签名认的授权、访问文稿和桌面目录）一律用 `scripts/dev-app.sh` 装出的 `build/dev/PosseDev.app`：包 ID `com.dengbo.posse.dev`、同一张证书，授权一次一直有效，可反复重装。它没设 `POSSE_HOME` 时用 `~/.posse-dev`、没设 `POSSE_MOBILE_PORT` 时手机端口用 6790，`POSSE_HOME` 指向 `~/.posse` 也换成 `~/.posse-dev`（`daemon.ResolveHome`）：双击、系统重新拉起、Computer Use 按包 ID 激活都不带环境变量，在 Posse 的终端里直接运行又会继承日常的 `POSSE_HOME`，连上日常的常驻进程后窗口会把它自动交接到更新的开发包上。命令行调用 `build/dev/PosseDev.app/Contents/MacOS/Posse …`，窗口用 `open -n build/dev/PosseDev.app`。手机配对一次长期有效，用完 `daemon stop` 停掉它的常驻进程（先 `daemon status` 看 home 是 `~/.posse-dev`）。
- `build/darwin-arm64/Posse.app` 是日常在用的包（窗口、常驻进程、hooks 都从它运行），只由正式打包写入，不往里复制、不原地改写：正在运行的程序文件被改写，系统按签名失效直接杀掉所有用它的进程，终端全部丢失。不拿它做测试，用上一条的 PosseDev.app；测试用的包如果和日常的包 ID 相同，按包 ID 激活起来的实例会用 `~/.posse`，还会把日常的常驻进程交接过去（发生了就用 build 里的包 `posse daemon handoff` 换回来）。
- 在 Posse 的终端里跑测试时，环境里带着日常常驻进程的 `GIT_TRACE2_EVENT`，`internal/daemon` 的 `TestGitTrace` 会失败：用 `env -u GIT_TRACE2_EVENT -u GIT_TRACE2_ENV_VARS go test ./...`。
- 发布 Mac 版：先在 `CHANGELOG.md` 加 `## <版本>` 一节（更新时显示的更新内容，可以不提交），再跑 `scripts/release.sh <版本>`：它把版本号写进 `mygo.json` 和 `internal/daemon/daemon.go`（`TestVersionMatchesPackage` 保证两处一致），连同 CHANGELOG 提交成 `chore(release): 发布 <版本>`、打标签 `v<版本>`（不推送），再打包、公证到 `build/release/<版本>`，用 wrangler（本机登录的 Cloudflare 账号，过期了 `npx wrangler login`）把更新包、增量包、dmg 和清单传到 R2 的 `posse-updates`，经 `https://posse.shaoyan.uk` 下载（`Posse.dmg` 是最新版）。打包或上传失败后不带参数重跑，发布 `mygo.json` 里现在的版本。更新包的签名私钥在 `~/Library/Application Support/mygo/update-keys/`，丢了已装的 Posse 就收不到更新。装在「应用程序」里的 Posse 会自己更新，`build/` 里的不会；方案和换包时机见 `docs/plans/mac-update.md`。
- 发布 iOS 的 Ad Hoc 安装包：先在 `mobile/CHANGELOG.md` 加 `## <版本>` 一节（App 提示更新时显示的更新内容，可以不提交），再跑 `scripts/ios-adhoc.sh <版本>`：它把版本号写进 `mobile/pubspec.yaml`，连同 CHANGELOG 提交成 `chore(release): 发布 iOS <版本>`（不推送；`mobile`、`internal/mquic` 有别的未提交改动时拒绝），用 Apple Distribution 证书打 ipa（导出设置 `mobile/ios/ExportOptions-adhoc.plist`，`--dart-define` 写进版本号和构建号），连同 itms-services 清单、安装页和 `latest.json` 传到 R2 的 `ios/`，iPhone 用 Safari 打开 `https://posse.shaoyan.uk/ios/index.html` 安装，装过的 App 读 `latest.json` 在首页提示更新。不带参数是按现在的版本重新打包上传（登记新设备、上传失败后）。只有开发者账号里登记过 UDID 的设备装得上；构建号是提交数，覆盖安装不清配对。
- 内存验收看 `footprint -p <pid>` 的 phys_footprint，不看 RSS：libghostty-vt 压缩回滚后 RSS 不会立即下降。

## 结构

```
cmd/posse            单一二进制：窗口（posse app，或从 Posse.app 无参数启动）、常驻进程（posse daemon run）和 CLI 共用
internal/rpc        unix socket 协议：每连接一次请求，请求/响应各一行 JSON
internal/daemon     常驻进程：项目、工作区、终端的 RPC 处理；task.go 是多 agent 编排；desk.go 是手机看桌面的会话（同一时间一台，拉起采集子进程、收发 UDP）
internal/host       常驻进程和手机操作终端的边界（Host、Terminal 接口），包着 internal/term 的进程内实现；升级交接（Pause 导出、Adopt 重建）也在这层，方案见 docs/plans/terminal-host.md
internal/term       PTY + libghostty-vt 维护屏幕状态；接入时给界面快照和输出流
internal/vt         libghostty-vt 绑定，复制自 MyGo 后只留常驻进程用得到的部分（键鼠编码、选区已删，RenderState 只在测试里）；snapshot.go 逐格生成快照内容
internal/client     CLI 与界面共用的客户端：拉起常驻进程、接入终端（Conn）
internal/store      SQLite（modernc，纯 Go）：项目、工作区、终端记录；task.go 是任务和消息
internal/history    会话历史：读 claude/codex/cursor/grok 的会话文件，增量索引进 $POSSE_HOME/history.db（FTS5），搜索和摘要；也给技能与工具面板统计用量、扫描会话里的图片和视频
internal/gitx       调用系统 git：管理 worktree（常驻进程），以及界面的状态、diff、分支、推送、日志、冲突
internal/agent      agent 启动命令与 hooks 事件到状态的映射
internal/namer      会话标题（posse session title）：规整 `MMDD｜类型｜摘要`，写 Codex、Grok 自己存标题的地方
internal/cli        子命令解析与输出（--json 给 agent 用）
internal/app        MyGo 原生窗口：侧栏项目/工作区，标签页终端，任务、历史、文件、Git 等界面，状态栏用量和内存；只接入当前可见的终端；各界面见 docs/features/
internal/procmem    读取进程 phys_footprint（proc_pid_rusage）
internal/spawn      拉起常驻进程和桌面采集子进程：新开会话，让它自己对系统授权负责
internal/desk       手机看 Mac 桌面的采集侧：子进程（`posse desk helper`）用 ScreenCaptureKit 采屏（可选屏，也可只看一个窗口，window_darwin.go）、VideoToolbox 硬编码，帧经管道交给父进程，手机的键鼠输入由父进程排序去重（input.go）、子进程发 CGEvent 注入（inject_darwin.go）；父进程拆 RTP、AES-GCM 加密发 UDP、按接收报告调码率（`posse desk cast` 是还没接进常驻进程时的推流入口）
internal/usage      各 agent 套餐用量：解析 Claude statusLine 输入和缓存、Codex 会话文件和 app-server、Grok 日志、Cursor 接口，合并后存 $POSSE_HOME/usage.json
internal/files      文件标签的纯逻辑：读写文件（原子保存、磁盘戳）、目录树、文件清单、模糊匹配、全文检索、跳到定义、识别行内链接和路径
internal/media      会话里的图片和视频（粘贴的、agent 生成的），桌面的图片面板和手机共用
internal/mobile     让 Orca 手机 App 连到 posse：配对、E2EE v2、它用到的那部分 RPC 和终端二进制流；quic.go 给 Posse 手机客户端在同一端口号的 UDP 上开 QUIC（局域网和中转）
internal/mquic      手机和 Mac 之间的 QUIC 传输，两端共用：一条流上的消息分帧、关闭帧、向中转登记的 socket、手机拨号
internal/deskrelay  UDP 中转协议和服务器（看桌面和手机 QUIC 共用：按令牌把手机和 Mac 的包原样互转，只见密文；探测包回来源地址，给打洞用；开了鉴权时只有带签名的 Mac 能开会话，邀请码、管理员码和 desk.json 的读写也在这里），`tools/deskrelay` 是服务器程序，改了协议要重新部署
tools               desk 的验收工具：`deskrecv`（Swift 收包解码，不补发）、`desksession`（和插件的 `DeskSession.swift` 一起编，跑手机上那份收包、补发、解码）、`deskshape`（本机 UDP 限速、延迟、丢包）、`deskrelay`（部署到公网服务器上的中转）
scripts             `dev-app.sh`：装开发用的 PosseDev.app；`release.sh`：发布 Mac 版；`ios-adhoc.sh`：发布 iOS 的 Ad Hoc 安装包
mobile              Posse 手机客户端（Flutter，用 fvm 固定版本，命令见其中的 README；`third_party/flterm` 是带改动的终端组件，改动见其中的 POSSE.md；`packages/posse_quic` 是编进 App 的 Go QUIC 客户端，构建钩子调 `go build`，Dart 经 FFI 调用）
third_party/mygo    带改动的 mygo v0.6.3（go.mod 用 replace 指向它），改动和升级方法见其中的 POSSE.md
```

## 关键约定

- 常驻进程持有 `$POSSE_HOME/possed.lock` 的文件锁直到完全退出；socket 和 pid 文件只在持锁时创建和删除。例外是交接接过来的新进程：先写 pid 文件、接着用旧进程交来的 socket 服务，锁在后台等（见下一条）。
- 升级交接（`daemon.handoff`，`internal/daemon/handoff.go`；流程、描述符怎么交、带不过去的终端状态见 `docs/plans/terminal-host.md` 和代码注释）：窗口的包比常驻进程新时自动触发（`internal/app/upgrade.go` 的 `autoUpgrade`，每个旧进程只自动试一次），设置页的"更新常驻进程"和 `posse daemon handoff` 手动触发。改代码时要守的：协议版本是 `handoffVersion`，改了交接记录或帧格式要加一；新建终端和交接开始经 `handoffState.startMu` 互斥；交接的终端和以它们为 worker 的任务不记为 lost；旧进程只把 PTY 已交出去的终端的退出留给新进程，交接窗口里退出的新进程收不到退出码，记 -1；新进程回话后马上开始服务（hook 只等 2 秒，不能等旧进程退出），在后台一直等锁，拿到才开手机服务（端口在旧进程手里），等不到也不退出；普通启动拿到锁后 socket 还有人应答就退出，免得交接空档里被拉起的进程删掉新进程的 socket；交接期间 `terminal wait --for idle` 不算空闲；手机看着时终端是手机的尺寸，记录里带上界面的尺寸（`DeskCols`、`DeskRows`），新进程换回去。
- 常驻进程退出（交接或重启）时 `rpc.Server` 先等正在处理的请求写完回复（最多 3 秒），处理函数因请求的 ctx 结束返回 `context.Canceled` 的回成 `restarting`。新增长等待的 RPC 要跟着 ctx 结束，CLI 侧走 `doWait`，到新进程上按剩下的时间接着等；`task ask` 带回问题 ID 改成 `--resume`、Claude 权限挂起到新进程上再挂，见 `doWait` 和 `hook.permission.hold` 调用处的注释。
- PTY 主端是非阻塞、交给 Go 轮询器的描述符（`term.pollable`），交接时靠读期限停下读协程。不要对它调 `Fd()` 或 `pty.Setsize`（经 `Fd()` 改回阻塞模式，读期限就失效了，而且阻塞标志和交出去的描述符共用），改尺寸用 `setPTYSize`、取描述符用 `SyscallConn`。
- 拉起常驻进程的一方（`client.EnsureDaemon`、交接时的旧进程）都经 `internal/spawn`：新开会话，并用私有接口 `responsibility_spawnattrs_setdisclaim` 让它自己对系统授权负责（否则授权记在拉起方身上：终端里的 CLI 拉起时是那个终端 App，一路交接下去是最初那份 Posse.app，包被换掉后每次访问桌面都弹授权）。`daemon.log` 也由拉起方经 `daemon.OpenLog` 打开后交给它当 stdout/stderr；拉起前超过 5 MB 就改名成 `daemon.log.1`（只留一份），运行期间不轮转。`EnsureDaemon` 拉起后在后台 `Wait`，窗口开着时常驻进程退出不留僵尸进程。
- 程序关掉鼠标跟踪（9、1000、1002、1003 都关）时，如果最后写进 PTY 的是鼠标报告，常驻进程清掉 PTY 里还没读的输入（`dropStaleMouse`，主端 `TIOCFLUSH`）：Codex 退出时先不读输入、最后才关鼠标跟踪，这期间鼠标移动的报告会被接着运行的 shell 读成一串 `35;12;8M`。
- 终端内核对程序的终端查询（DA/DSR 等）的应答写回 PTY，由 `Terminal.answer` 控制；界面连接后改由界面应答，避免重复回复。
- `terminal send` 直接写 PTY，不经过终端内核；带 `--enter` 时回车与文本分开发送，否则 TUI 会把回车当成粘贴内容。
- 新终端的环境会去掉 `ORCA_*`、`CLAUDE_CODE_*`、`CLAUDECODE` 等变量，以及 agent 给工具调用设的非交互变量（`NO_COLOR`、`FORCE_COLOR`、`PAGER`、`GIT_EDITOR`、`GROK_*` 等，会经 `open` 拉起的 Posse.app 漏进终端，见 `term.Environ`）；注入 `POSSE_HOME`、`POSSE_SOCKET`、`POSSE_TERMINAL_ID`、`POSSE_WORKSPACE_ID`，以及给 Git 操作记录用的 `GIT_TRACE2_EVENT`、`GIT_TRACE2_ENV_VARS`；PATH 最前面是 posse 所在目录。
- 工作区三种：`main`（项目目录本身）、`folder`（同一目录上的额外工作区）、`worktree`（git worktree，放在 `$POSSE_HOME/worktrees/<项目>/<名称>`）。别的工具建的 worktree 用 `attach` 模式加进来时记成 `folder`（路径是那个 worktree），删工作区不碰它的目录和分支。
- agent 状态靠启动参数注入的 hooks，不改用户的全局配置：Claude 用 `--settings $POSSE_HOME/claude-hooks.json` 和 `--session-id`；Codex 用 `-c hooks.<事件>=…` 加 `-c hooks.state=…` 预置信任 hash（算法见 `codexHookHash`，Codex 升级后用 `codex app-server` 的 `hooks/list` 核对是否仍为 trusted）；Cursor 用 `--plugin-dir $POSSE_HOME/cursor-plugin`，hooks 放在 posse 生成的插件里（`internal/agent/cursor.go`），不写 `~/.cursor/hooks.json`。
- `posse hook` 被 agent 同步调用：不得向 stdout 输出（Claude 会把它注入模型上下文）。例外有三处：Cursor 要从 stdout 读一个 JSON 回应，posse 输出 `{}` 表示不干预；Claude 的 PermissionRequest 在手机给出 allow 或 deny 时向 stdout 写决策 JSON，没有决定就不输出；Claude 的 UserPromptSubmit 在有待交的会话标题时写 `hookSpecificOutput.sessionTitle`。始终 exit 0，不拉起常驻进程。
- 没有 hook 的情况靠终端标题兜底（`agent.TitleStatus`）：Claude 按 Esc 中断后标题回到 `✳` 前缀；Codex 等待操作时标题含 `Action Required`；grok 不注入 hooks，状态全靠标题（盲文转圈字符是工作中，含 `Action Required` 是等待）。用户自己启动的 agent 也只靠标题，见 `docs/features/agents.md`。
- 界面接入终端：`terminal.attach` 流式连接，常驻进程先发快照再推实时输出，两者在 `smu` 下衔接，不丢不重（快照怎么生成、为什么接上 `tail`，见 `internal/vt/snapshot.go` 和 `Terminal.subscribe` 的注释）。行数变多时常驻进程会重建内核（`term.pullBack`）并断开所有接入者（`Subscriber.Kicked`），它们按自己当下的尺寸重新接入拿新快照；不在输出流里广播快照。
- 所有子命令都认 `-h` / `--help`（在 `--` 之前），打印这条命令的用法：agent 常先这样试。
- `workspace merge` 在项目目录以 `--no-ff` 合入 worktree 分支：要求两边已跟踪文件干净、分支有新提交、项目目录在基准分支上；失败时 `merge --abort`。未跟踪文件不挡合并，但 `workspace rm` 仍会因它拒绝删除。
- Posse.app 里的可执行文件叫 `Posse`，agent 在终端里调用 `posse` 能找到它，依赖 APFS 默认的大小写不敏感。
- 菜单由 `installMenu` 自己装：macOS 先把按键交给菜单，mygo 默认菜单的"关闭窗口"占着 ⌘W（界面收不到，关最后一个标签会连 app 一起关掉），View 菜单占着 ⌘R、⌘=、⌘-、⌘0。⌘W 是"关闭标签"（`closeTab`），关窗口是 ⇧⌘W。
- 只有界面关心的状态（`internal/app/uistate.go`）存在 `$POSSE_HOME/ui.json`，由窗口进程读写，不进常驻进程：改常驻进程的协议要换掉常驻进程。
- 重新打包、覆盖安装后，窗口进程失去 TCC 授权（读桌面、文稿等目录报 EPERM，不弹授权），要重开窗口（`internal/app/relaunch.go`，提示和重开流程见 `docs/features/ui.md`）。
- 界面每秒轮询常驻进程，数据和上次相同就不重建界面；只有连不上 socket 时才去拉起常驻进程。靠时间推进的内容（右侧面板的定时刷新、"几分钟前"）要在视图里用 `c.After` 约下一帧，不能指望每秒重建。操作失败的报错留在错误条上，等用户关掉。
- 功能说明按模块放在 `docs/features/`：`agents.md`（agent 识别、手动启动、编排、会话标题、权限）、`history.md`、`files.md`、`git.md`、`mobile.md`、`usage.md`（套餐用量、防休眠）、`ui.md`（界面各部分）。改了功能同步更新对应的文档。
- `$POSSE_HOME/bin` 的转发脚本和 shell 集成文件每个常驻进程只写一次，注入 agent 的参数（hooks、技能插件、`developer_instructions`、`--rules`）在常驻进程的代码里：改了这些要交接（`posse daemon handoff`，不断终端）或重启常驻进程，之后新开的终端才生效。Codex 的 `-c developer_instructions` 会整个替换用户 `config.toml` 里的同名设置，所以用户设了它时不注入。
- 会话历史只读各 agent 的会话目录，不写回（例外：会话标题、用户在历史页移到废纸篓）。只改了某一家的解析规则改 `parserVersions`（index.go），只重读这一家；改表结构或分词才改 `schemaVersion`（旧索引重建，跨版本的旧常驻进程报 ErrStale，要重启）。改粗筛规则（`wants`）要跑 `TestLineFilterSameResult`。索引只有一个连接，结果集没关之前不能再查。
- 文件栏不用 fsnotify（macOS 上基于 kqueue，监听一个目录要为其中每个文件开一个描述符），看得见时每秒 stat 一次。保存前磁盘戳变了先问，不默默覆盖 agent 的修改。
- Git 界面全部在窗口进程里调系统 git，常驻进程不参与：路径都相对仓库根目录，仓库根目录换成工作区路径的写法（`repoRoot`，git 给的是解析过符号链接的）；界面调用的 gitx 函数用 `runRaw`；gitx 自己调 git 时设 `GIT_TRACE2_EVENT=0`。强制推送一律 `--force-with-lease --force-if-includes`。
- `internal/desk` 经 purego 调 Objective-C：自动释放池按线程记，在 goroutine 上 push/pop 之间要 `runtime.LockOSThread`（`withPool`），否则 goroutine 换线程后 pop 会崩。用 iOS 模拟器测手机看桌面时推流加 `--exclude-app com.apple.iphonesimulator`（经常驻进程订阅时给常驻进程设 `POSSE_DESK_EXCLUDE=com.apple.iphonesimulator`），否则模拟器窗口被录进画面形成回环。
- 看桌面和手机 QUIC 的 UDP 中转写在 `$POSSE_HOME/desk.json`（`{"relay": "host:port"}`，被邀请的再带 `name`、`key`，管理员带 `master`），由手机页「外网中转」或 `posse mobile relay set <码>` 写入，每次观看、每次手机调 `posse.quic` 时读，不用重启。中转用的 socket 经 `IP_BOUND_IF` 绑在物理网卡（en*）上，绕开 Surge 这类接管默认路由的代理（走代理时 UDP 出不去）；直连局域网和 Tailscale 的 socket 不绑，照系统路由走。
- 手机遥控的端到端测试用 `POSSE_MOBILE_PORT` 换端口，免得和日常的常驻进程冲突；主机密钥在 `$POSSE_HOME/mobile.json`，换密钥等于所有手机重新配对。
- 菜单都是 Posse 自绘（`menu.go`）：按钮弹出用 `menuButton`，右键用 `contextMenu`（mygo 的 `ContextMenuAsked` 沿用它的右键路由，只报位置），终端内容区由插件的 `Options.ContextMenu` 交来位置；不用 mygo 的 `Menu`、`ContextMenu`（系统 NSMenu，悬停高亮改不了）。build 每帧先跑一遍收集菜单项，点了某项后同一帧再跑一遍报告 `Chosen`，两遍加的项要一样。"在访达中显示/打开"一律用 `iconFolder`。mygo 自己的菜单（文字输入框、可选文字、编辑器的编辑菜单）经 `Context.PresentMenus` 交给 Posse（`presentMenu`、`sysMenuView`），菜单项的英文在 `menuLabels` 里换成中文，mygo 加了新的菜单项要补上。能点的元素主题已给手形指针，新控件不用单独设 `Cursor(ui.CursorPointer)`；只为挡住点击才调 `Clicked` 的大容器要设 `Cursor(ui.CursorDefault)`。
