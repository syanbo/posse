# posse

多 agent 终端管理器，替代 Orca。目标是低内存：一个 Go 常驻进程托管所有终端，界面可以随时关闭。

## 构建与测试

```sh
export GOPROXY=https://goproxy.cn,direct   # 本机直连 proxy.golang.org / storage.googleapis.com 会超时
go build -o bin/posse ./cmd/posse
go vet ./... && go test ./... && go tool mygo vet && go tool mygo i18n check
go tool mygo build -platform darwin/arm64 -skip-dmg   # 打包 build/darwin-arm64/Posse.app（Intel 机器用 darwin/amd64）
```

- go.mod 要求 Go 1.27.3（MyGo 的要求），本机 Go 1.26 会通过 `GOTOOLCHAIN=auto` 自动下载；终端内核 `libghostty-vt` 首次运行时由 MyGo 下载到 `~/Library/Caches/mygo/natives/`。
- `gofmt` 用 `$(go env GOROOT)/bin/gofmt`：Homebrew 的 gofmt 版本较旧，对齐规则不同。
- 在 Posse 的终端里跑测试时环境带着日常常驻进程的 `GIT_TRACE2_EVENT`（`internal/daemon` 的 `TestGitTrace` 会失败），用 `env -u GIT_TRACE2_EVENT -u GIT_TRACE2_ENV_VARS go test ./...`。
- 端到端测试不要碰 `~/.posse`。只测 CLI 和常驻进程、不涉及系统授权时用 `POSSE_HOME=/tmp/posse-e2e/home ./bin/posse ...`；单元测试和离屏预览不起 App。
- 改界面后用离屏预览自查（锁屏也能跑）：`POSSE_PREVIEW_DIR=/tmp/posse-preview go test ./internal/app -run TestPreview`（清单见 `preview_test.go` 和其余 `*preview*_test.go`）。
- 写桌面界面前读 `docs/design.md`（字号、颜色、间距只用 `style.go` 里的名字，不写数字；自绘侧栏 `navRowAt`、快捷键表 `keymap.go`、自绘菜单 `menu.go` 与 `installMenu` 规则见该文档），各界面行为见 `docs/features/ui.md`。
- 桌面多语言经 mygo 框架（方案见 `docs/plans/i18n.md`）：译文表是 `locales/en.json`（源语言，带 `description`）和 `zh-Hans.json`，键小驼峰、界面在前，带数字或名字的整句用 ICU 占位符和复数，不拼句子。视图里写 `L(c).SettingsTitle()`，无 Context 的菜单才用平面函数 `locales.SettingsTitle()`；界面文字一律经 `locales`，禁写中文字面量和全角标点（`TestNoChineseLiterals`）。改 JSON 后跑 `go tool mygo generate` 重写 `locales/catalog_gen.go` 并提交（`TestCatalogGenerated` 核对）；mygo 控件叫法在 `zh-Hans.json` 用 `mygo.<键>` 覆盖；测试用 `newTester`（固定中文和 24 小时制）。钟点写 `::jm`、`::jms`（不写 `::Hm` 或 `Format("15:04")`），用 `appTime` 等键经 `L(c)`/`appL()` 取。`internal/cheatsheet/cheatsheet.json` 新增条目同步补 `titleEn`/`descEn`/`argsEn`。
- 写手机端界面前读 `docs/mobile-design.md`：样式只用 `mobile/lib/ui/theme.dart` 里的名字（`design_rules_test.dart` 检查）；文字经 Flutter `gen-l10n`（`app_en.arb` / `app_zh.arb`，改后跑 `fvm flutter gen-l10n` 一并提交，钟点用 `dates.dart` + `use24Hour(context)`），原生插件和 iOS `InfoPlist.strings` 禁写中文（`l10n_test.dart` 检查），细则见 `docs/mobile-design.md`。
- 要把 App 跑起来的测试（窗口、Computer Use、看桌面、TCC/录屏/辅助功能授权）一律用 `scripts/dev-app.sh` 装出的 `build/dev/PosseDev.app`（包 ID `com.dengbo.posse.dev`，同证书签名，默认 `~/.posse-dev` 和手机端口 `6790`；`POSSE_HOME` 指向 `~/.posse` 也会经 `daemon.ResolveHome` 换到 `~/.posse-dev`）。worktree 里跑脚本也装到主仓库的 `build/dev`，只有一份，换包前先退出正在运行的 Posse Dev，不并行验收。CLI 调 `build/dev/PosseDev.app/Contents/MacOS/Posse …`，窗口用 `scripts/dev-app.sh --open`（后台打开，靠 `ui.DrawWhenOccluded` 挡住也照常画，截图和无头键鼠不用前置；只有悬停、拖动等真实鼠标操作才前置）；不要 `open -n`、不要直接跑 `posse app`。用完先 `daemon status` 核对 home 是 `~/.posse-dev` 再 `daemon stop`。
- `build/darwin-arm64/Posse.app` 是日常在用的包（窗口、常驻进程、hooks 都从它运行），只由正式打包写入，严禁往里复制或原地改写（签名失效会被系统直接杀掉所有相关进程，终端全部丢失），也不拿同包 ID 的测试包运行（误把日常常驻进程交接过去时，用 `build/darwin-arm64` 里的包执行 `posse daemon handoff` 换回）。
- 签名证书（`mygo.json` 写团队 `QKSM9Z4U85` 证书 SHA-1）、应用图标（`design/icon-art.png` 与 `Posse.icon`，禁放 `resources/`）及三端发布步骤见 `docs/release.md`。`CHANGELOG.md`、`mobile/CHANGELOG.md` 每节先写中文条目，再写 `### English` 小节。
- 内存验收看 `footprint -p <pid>` 的 `phys_footprint`，不看 RSS（`libghostty-vt` 压缩回滚后 RSS 不会立即下降）。
- `main` 保持一条直线，不留合并提交：分支或 worktree（含 `posse task` 的 worker）先 `git rebase main`、重跑测试，再到主仓库 `git merge --ff-only <分支>`；不要用按 `--no-ff` 合入的 `posse workspace merge`。

## 结构

```
cmd/posse              单一二进制：窗口（posse app / 无参启动 .app）、常驻进程（daemon run）和 CLI 共用
internal/rpc           unix socket 协议：每连接一次请求，请求/响应各一行 JSON
internal/daemon        常驻进程：项目、工作区、终端、远端 Git/文件 RPC；task.go 编排，desk.go 看桌面，discussion.go / collab*.go 讨论与协作
internal/host          终端宿主边界（Host、Terminal 接口）与升级交接（Pause 导出、Adopt 重建，见 docs/plans/terminal-host.md）
internal/term          PTY + libghostty-vt 屏幕状态，接入快照与输出流
internal/vt            精简版 libghostty-vt 绑定（删键鼠编码/选区/视口滚动，禁写剪贴板）；snapshot.go 逐格导出快照
internal/client        CLI 与窗口共用的客户端：拉起常驻进程、流式接入终端与目标 RPC
internal/remote        Mac SSH 远程工作区：主机配置、私有 askpass、远端发现、socket 转发与能力握手（docs/features/remote.md）
internal/store         SQLite（modernc，纯 Go）：项目、工作区、终端、任务（task.go）、讨论与协作（discussion.go、collab.go）
internal/history       claude/codex/cursor/grok 会话增量索引（$POSSE_HOME/history.db，FTS5）、搜索、工具用量与媒体扫描
internal/gitx          调用系统 git：管理 worktree，以及状态、diff、分支、推送、日志、追溯、比较与冲突预测
internal/agent         各家 agent 启动参数、手动启动转发与 hooks/状态映射
internal/namer         会话标题（posse session title）：规整 MMDD｜类型｜摘要 并写回 Codex/Grok 存储
internal/cli           子命令解析与输出（--json 供 agent 调用）
internal/app           MyGo 原生窗口：侧栏、终端标签、任务、讨论、历史、文件、Git、技能、手机与设置页（只接入当前可见终端）
internal/procmem       读取进程 phys_footprint（proc_pid_rusage）
internal/cheatsheet    各家 agent 命令/按键速查表（cheatsheet.json）与本机版本识别
internal/projicon      项目自定义图标（$POSSE_HOME/project-icons）
internal/collaboration 讨论身份（$POSSE_HOME/collab.json，Ed25519）与局域网协作传输（TLS 1.3 WebSocket、邀请、凭据）
locales                桌面多语言译文表（en.json、zh-Hans.json）与 mygo 生成代码（catalog_gen.go）
internal/errtext       英文报错按界面语言查表翻译（如 zh.go），供桌面窗口和手机服务显示
internal/spawn         拉起常驻进程与采集子进程（新会话 + disclaim 授权归属）；runtime.go 管理 APFS 克隆运行副本
internal/desk          手机看桌面采集侧（desk helper：ScreenCaptureKit + VideoToolbox + CGEvent 注入；父进程 RTP/AES-GCM UDP）
internal/usage         各家套餐用量采集与合并（$POSSE_HOME/usage.json）
internal/files         文件读写（原子保存、磁盘戳、分段读）、目录树、清单、全文检索、定义跳转与行内链接识别
internal/writecoord    同 daemon 多资源写协调：路径父子互斥、仓库独占键与项目共享/独占键，一次申请、等待可取消
internal/trash         无界面 macOS 废纸篓适配（Foundation/purego），失败不回退永久删除
internal/skills        扫描四家 agent 技能目录、开关配置、npx skills 锁文件与断链/冲突体检
internal/media         会话内粘贴与生成的图片/视频扫描及缩略图
internal/mermaid       Mermaid 排版（Pele + modernc QuickJS，与 sqlite 共用 modernc libc，升级须保持 libc 版本一致；diagram/ 用 mygo 画）
internal/mobile        手机服务端：配对、E2EE v2、RPC/二进制流与同端口 UDP QUIC（quic.go）
internal/mquic         手机与 Mac 共用的 QUIC 消息分帧、关闭帧、中转登记与拨号
internal/deskrelay     看桌面与手机 QUIC 共用的 UDP 中转协议、鉴权邀请、APNs 转发与 desk.json 读写
tools                  desk 验收与部署工具：desksession、deskshape、deskrelay（改中转协议须重部署）
scripts                dev-app.sh、release.sh、github-release.sh、ios-adhoc.sh、android-release.sh、pele.sh、memprobe.sh、discmem.sh 等
mobile                 Flutter 手机客户端（fvm 固定版本；third_party/flterm 终端组件；packages/posse_quic Go FFI 库）
third_party/mygo       带改动的 mygo v0.7.4（改动与升级方法见其中的 POSSE.md）
```

## 模块文档路由

改对应模块前先读相应文档，改完功能同步更新文档：

- `docs/features/agents.md`：agent 识别、手动启动 hooks、编排（`posse task`）、会话标题、自带技能、权限、恢复终端、`posse web`、文件去处与痕迹
- `docs/features/git.md`：多仓库发现、改动/提交/储藏/回滚、diff、日志、比较、分支、worktree 同步合并及目标 Git 操作规则
- `docs/features/remote.md`：SSH 远程工作区、桌面查看/接管租约（`desktop.*`）、跨入口写协调及目标文件/媒体/Git RPC 边界与状态机
- `docs/features/files.md`、`history.md`、`discussions.md`、`usage.md`、`mobile.md`、`ui.md`：文件栏与编辑器、会话历史、讨论与局域网协作、套餐用量与防休眠、手机遥控与看桌面、桌面各界面行为
- `docs/plans/terminal-host.md`：常驻进程无损交接（`daemon.handoff`）与 APFS 运行副本回收

## 关键约定

- **锁与升级交接**（细则见 `docs/plans/terminal-host.md`）：常驻进程持有 `$POSSE_HOME/possed.lock` 直到完全退出，普通启动只在持锁时增删 socket/pid（拿到锁后 socket 仍有人应答则退出）。交接新进程先写 pid、立即接过旧 socket 服务，后台等锁拿到后再开手机服务；新建终端与交接经 `handoffState.startMu` 互斥；双向不兼容变更才递增 `handoffVersion`，兼容可选字段不加；交接终端及关联任务不记 `lost`，交接空档退出的终端记 `-1`，交接中 `terminal wait --for idle` 不算空闲，手机占尺寸时随 `DeskCols`/`DeskRows` 带回桌面尺寸。退出时 `rpc.Server` 最多等 3 秒写完在途回复，因请求 ctx 取消的回 `restarting`；长等待 RPC 必须响应 ctx，CLI 走 `doWait` 续等（`task ask` 改 `--resume`，`hook.permission.hold` 重挂）。
- **拉起与运行副本**：拉起常驻进程和采集子进程一律经 `internal/spawn`（新会话 + `responsibility_spawnattrs_setdisclaim` 让其自身承担 TCC 授权，`daemon.OpenLog` 启动前超 5 MB 轮转为 `daemon.log.1`，`EnsureDaemon` 后台 `Wait` 防僵尸）。打包版常驻进程从 `~/Library/Application Support/Posse/runtime/` 下的 APFS 克隆副本运行（`spawn.Runtime`，终端记 `session.runtime`，启动时和终端退出后按引用回收，防换包后 TCC 拒访；详见 `docs/plans/terminal-host.md`）。重新打包覆盖后窗口进程会丢 TCC 授权，需重开窗口（`internal/app/relaunch.go`）。
- **PTY 与终端输入输出**：PTY 主端是非阻塞、交给 Go 轮询器的描述符（`term.pollable`），严禁调 `Fd()` 或 `pty.Setsize`（会改回阻塞模式使交接读期限失效），改尺寸用 `setPTYSize`、取描述符用 `SyscallConn`。程序关掉全部鼠标跟踪（9/1000/1002/1003）且最后写入是鼠标报告时，`dropStaleMouse` 调 `TIOCFLUSH` 清未读输入；PTY 未读超 256 B 或写完超 960 B 时丢弃滚轮与移动报告（`Inject`/`lossyMouse`，按下松开不丢），防截断产生孤立 ESC。终端查询（DA/DSR、OSC 10/11 等）未接界面时由内核应答（`Terminal.answer`），界面接入后改由界面应答；终端默认配色跟窗口每秒 `daemon.status` 上报的 `statusParams`（5 秒未收视作窗口关闭，`daemon/window.go`）。`terminal send` 直写 PTY，`--enter` 时文本与回车分开发送。
- **界面接入终端**：`terminal.attach` 流式连接在 `smu` 下先发快照再接实时输出，不丢不重（见 `internal/vt/snapshot.go`）；行数变多时重建内核（`term.pullBack`）并踢开所有接入者（`Subscriber.Kicked`）重连拿新快照，不在输出流广播快照。
- **终端环境与固定路径**：新终端剥离 `ORCA_*`、`CLAUDE_CODE_*`、`CLAUDECODE` 及非交互工具变量（`NO_COLOR`、`FORCE_COLOR`、`PAGER`、`GIT_EDITOR`、`GROK_*` 等，见 `term.Environ`），注入 `POSSE_HOME`、`POSSE_SOCKET`、`POSSE_TERMINAL_ID`、`POSSE_WORKSPACE_ID`、`GIT_TRACE2_EVENT`、`GIT_TRACE2_ENV_VARS`，PATH 最前放当前常驻进程副本目录。长期留存的 posse 路径（hooks、Claude `statusLine`、`$POSSE_HOME/bin` 转发脚本、`posse agent exec`）一律写 `$POSSE_HOME/bin/posse`（`daemon.PosseLink` 启动时原子更新），不写副本路径。`Posse.app` 内可执行文件名为 `Posse`，终端敲 `posse` 靠 APFS 默认大小写不敏感。所有子命令在 `--` 前都认 `-h` / `--help`。
- **工作区**：三种模式 `main`（项目目录）、`folder`（同目录额外工作区）、`worktree`（`$POSSE_HOME/worktrees/<项目>/<名称>`）；外部建的 worktree 以 `attach` 加入时记为 `folder`，删除不碰其目录和分支。`workspace merge` 以 `--no-ff` 合入（要求两边已跟踪文件干净、有新提交、在基准分支上，失败 `merge --abort`）；未跟踪文件不挡合并，但挡 `workspace rm`。
- **Agent hooks 与状态**（细则见 `docs/features/agents.md`）：状态靠启动参数注入 hooks，不改用户全局配置——Claude 用 `--settings $POSSE_HOME/claude-hooks.json` 和 `--session-id`；Codex 用 `-c hooks.<事件>=…` 加 `-c hooks.state=…` 预置信任 hash（`codexHookHash`，升级后用 `codex app-server` 的 `hooks/list` 核对）；Cursor 用 `--plugin-dir $POSSE_HOME/cursor-plugin`（`internal/agent/cursor.go`）。启动只注入 hooks 和 Claude 的 `--append-system-prompt` 文件去处指路，不按会话注入技能、不碰 Codex `developer_instructions` 或 Grok `--rules`；改转发脚本或注入参数后需 `posse daemon handoff` 或重启常驻进程对新终端生效。`posse hook` 同步执行，始终 `exit 0` 且不拉起常驻进程，**严禁向 stdout 输出**，仅四处例外：Cursor 固定回 `{}`；Claude `PermissionRequest` 有手机决策时写决策 JSON；Claude `UserPromptSubmit` 有待交标题时写 `hookSpecificOutput.sessionTitle`；有未提醒任务消息时在 `agent.DeliversOn` 允许的钩子写提醒输出（`agent.DeliveryOutput`）。报过 OSC 7501 后工作中/空闲以它为准，hooks 管等待、退出与附加字段，标题只看 `Action Required`；未报 7501 且无 hook 时靠标题兜底（`agent.TitleStatus`）。
- **报错翻译与禁写中文**：内部包、常驻进程和 CLI 的报错源文本一律写英文（`errtext` 的 `TestNoChineseOutsideApp` 守卫 `internal/` 除 `app` 外及 `cmd/`）。桌面窗口显示报错一律经 `errtext.Text(err)`（用 `errtext.Current`），手机服务经 `conn.fail` / `errtext.TextIn`（按连接上报的 `locale`，缺省中文，不用全局 `Current`），常驻进程自显文字按 `errtext.SystemLang()`（5 秒缓存），手机端 Dart 经 `friendlyError`；新增界面可见报错须在 `internal/errtext/zh.go` 补精确格式串译文（`TestFormatsStillInSource` 核对）。
- **窗口状态与轮询**：仅界面关心的状态存 `$POSSE_HOME/ui.json`（`internal/app/uistate.go`），窗口直接读写，不进常驻进程。窗口每秒轮询常驻进程，数据未变不重建界面，连不上 socket 才拉起常驻进程；随时间推进的显示（定时刷新、"几分钟前"）在视图里用 `c.After` 约下一帧；操作报错留在错误条等用户关闭。文件栏不用 `fsnotify`（kqueue 每文件占一个 fd），可见时每秒 `stat`；保存前磁盘戳变化先询问。
- **会话历史**：只读各 agent 会话目录，不写回（会话标题和历史页移到废纸篓除外）。单家解析变更只改 `parserVersions`，表结构或分词变更才改 `schemaVersion`（旧常驻进程报 `ErrStale`）；改粗筛 `wants` 必跑 `TestLineFilterSameResult`；单连接查询，结果集未关前不得再查。
- **Git 与远程工作区**（各接口与状态机细则必读 `docs/features/git.md` 和 `docs/features/remote.md`）：有桌面身份的本机和远端控制窗口统一经目标 daemon 的 `git.*` 与已接受的 `desktop.call` 读写（无身份旧测试窗口保留本机 `gitx` 调用）；查看端隐藏写入口并静默拒绝，不发请求、不报门禁错。目标读取经 `gitx.WithReadLimit` 共用输出与扫描预算，前后核对 `Root`/`GitDir`/`CommonDir` 身份，禁执行目标过滤器脚本、不改全局 Trace、绝不回退本机同名路径。目标写操作一次取齐仓库路径、`common-dir`、项目共享键与相关文件锁（与 `files.save` 共用 `writecoord`），锁内复查工作区登记、仓库身份及点击时基线（HEAD OID、`IndexID`、分支/跟踪 OID、文件版本），不符返回 `conflict`（`Started=false`）且不改现场；开始后失败、冲突或取消保留现场，不自动 reset/abort。断线、关弹窗或控制转移不取消已接受操作；回复丢失只按原 `ref` 查 `desktop.operation`，不自动重发，另开须确认并先续跑本机旧队列；取消用当前控制身份与原 `ref`，回执不等于完成或回滚。桌面 Git 写操作、文件夹批量上传下载（`files.mkdir`）与远端终端复制/拖入文件上传（`files.pasteFile`）均已迁到目标服务，`daemon.info` 宣布完整能力集；任何误调的本机 Git 闭包仍由 `git.write` 门禁拦截。界面路径相对仓库根并换算为工作区写法（`gitx.ShownRoot`）；界面调 `gitx` 用 `runRaw`；`gitx` 自调 git 设 `GIT_TRACE2_EVENT=0`；强制推送只用 `--force-with-lease --force-if-includes`。
- **看桌面与手机遥控**（细则见 `docs/features/mobile.md`）：`internal/desk` 经 purego 调 Objective-C 时 goroutine 内 push/pop 自动释放池必须 `runtime.LockOSThread`（`withPool`）；本机用 iOS 模拟器测看桌面时加 `--exclude-app com.apple.iphonesimulator`（或 `POSSE_DESK_EXCLUDE`）防画面回环。外网中转存 `$POSSE_HOME/desk.json`（每次用时现读），中转 UDP socket 经 `IP_BOUND_IF` 绑物理网卡 `en*` 绕开代理，局域网和 Tailscale 不绑。手机端到端测试用 `POSSE_MOBILE_PORT` 换端口；换 `$POSSE_HOME/mobile.json` 主机密钥等于所有手机重新配对。
