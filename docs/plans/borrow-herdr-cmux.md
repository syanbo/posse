# 借鉴 herdr、cmux 的六项改进

来源：herdr（Rust 写的终端 TUI，专管 agent）和 cmux（基于 libghostty 的 macOS 终端）。只取 Posse 还没有的部分。下面的"现状"都对照过代码，并经过一轮独立审查。还要实测的（通知授权、各家 agent 的标题和屏幕、snapshot 脚本）写在对应的步骤里。

按价值和改动量排序，每项单独提交、单独验收，互不依赖。第 6 项改动小、先于第 5 项做，所以排在它前面：

| # | 内容 | 主要改动 | 规模 |
|---|---|---|---|
| 1 | Dock 角标、通知开关（系统通知已有） | app | 小 |
| 2 | agent 用 CLI 操作网页标签 | app、cli | 中 |
| 3 | 侧栏显示 PR 状态和监听端口 | gitx、daemon、app | 中 |
| 4 | 不注入 hooks 也能识别更多 agent | agent、daemon | 小（主要是逐家核实） |
| 6 | 在普通终端里接入（`posse attach`） | cli、daemon | 小 |
| 5 | 终端分屏 | app | 大 |

---

## 1. 系统通知和 Dock 角标

### 现状

- 窗口进程早已在发系统通知：`internal/app/app.go` 的 `noticeStatus` 在轮询里比较每个终端前后两次的状态，进入待处理、工作中变空闲时发；`tasks.go` 发任务的通知；`OnNotificationClick` 按 `posse:<工作区>:<终端>`、`posse-task:<任务>` 跳转。第一轮只记基线，正看着的会话不发。
- 缺的：Dock 角标；关掉通知的开关；"已完成"不看常驻进程的未读，手机正看着时完成的也会通知。
- 关掉最后一个窗口时窗口进程就退出（`third_party/mygo/app.go` 的 `lastWindowClosed`，Posse 没注册 `OnWindowAllClosed`）。不为通知留住进程，所以通知只在窗口开着时有。

### 改动

- "已完成"只在常驻进程记成未读（`Unread`）时发，和侧栏的铃铛口径一致。判断抽成纯函数 `noticeTitle`。
- Dock 角标 = 待处理 + 未读的 agent 会话数（还没恢复的不算，同一会话只算一次，`badgeCount`），在 `apply` 里更新，数变了才调 `mygo.App.SetBadgeCount`；没有窗口时（测试、离屏预览）不调。
- 设置 › 通用加「通知」卡片：「系统通知」「Dock 角标」两个开关，默认开，存 ui.json 的 `notifyOff`、`badgeOff`；关掉通知对 agent 和任务都生效，关掉角标马上清掉。

### 验收

- 单测 `internal/app/notify_test.go`：`noticeTitle` 的各种状态变化、`badgeCount` 的计数规则、关掉通知时不碰窗口但照样记状态。
- PosseDev.app 上的手测用例见 `docs/plans/borrow-herdr-cmux-1-acceptance.md`。

---

## 2. agent 用 CLI 操作网页标签

### 现状

- 网页标签在窗口进程里（`internal/app/web.go`，mygo `WebView`），常驻进程看不到它。
- mygo 的 `Page` 有 `EvalContext(ctx, js) (any, error)`、`LoadURL`、`URL`、`IsLoading`；`WebView.CapturePage()` 能截图；`WebViewOptions.Page.PreloadScript`（`PageOptions`，`third_party/mygo/page.go`）在每个页面自己的脚本之前注入（主 world、只主 frame）。够用，不用改 mygo。
- `internal/rpc` 是通用的一请求一连接服务，窗口进程也能起一个。

### 关键决策

- **通道**：CLI 直连窗口进程。窗口启动时用 `internal/rpc` 起一个服务，监听 `$POSSE_HOME/app.sock`；CLI 由 `POSSE_HOME` 推出路径直接连。常驻进程不改，不涉及交接、退出时等请求写完和 `doWait`。
  - 连不上就回"Posse 窗口没开"，不替它拉起窗口。
  - socket 已有人应答时不接管（同一 home 只有一个窗口进程接网页请求）；没人应答的残留文件删掉重建，窗口退出时删掉。
- **目标网页**：按工作区找（取 `POSSE_WORKSPACE_ID`），该工作区有多个网页标签时默认最近激活的，`--tab <n>` 指定；`posse web open <url>` 新开一个并返回它的编号。
- **命令**（都支持 `--json`）：

  ```
  posse web open <url>           在本工作区新开网页标签
  posse web list                 本工作区的网页标签：编号、网址、标题、是否在载入
  posse web goto <url> | reload | back | forward
  posse web wait [--for load|selector <css>|text <s>] [--timeout 30s]
  posse web snapshot             可交互元素的精简树：每个元素带 ref（e1、e2…）、role、名字、值
  posse web click <ref|css>
  posse web fill <ref|css> <text>
  posse web press <key>          Enter、Tab、Escape 等
  posse web eval <js>            返回 JSON 结果
  posse web screenshot [-o path] 默认写到 $TMPDIR，打印路径（agent 用读图片的方式看）
  posse web console              最近的 console 输出和未捕获错误
  ```

- **snapshot 的实现**：一段注入的 JS 遍历 DOM，只收可见的可交互元素和标题、地标，按 ARIA role 和可访问名字（`aria-label`、`<label>`、文字）输出；ref 写进元素的 `data-posse-ref`，click、fill 先按 ref 找。不用系统无障碍 API：WebContent 进程里取不到页面级的树，JS 就够用且跨页面一致。
- **console**：页面载入时注入一段脚本，把 `console.*` 和 `window.onerror` 存进页面里的环形缓冲（最多 200 条），`web console` 读它。经 `PreloadScript` 注入，`openWeb` 现在只传 `URL`（`internal/app/web.go`），要补上 `Page: mygo.PageOptions{PreloadScript: …}`。只注入主 frame，iframe 里的输出收不到，写进文档。
- **安全**：socket 权限 0600，只有同一用户能连，和 `terminal send` 同一信任边界，不另加确认。

### 步骤

1. 窗口：`internal/app/webctl.go` 起 `app.sock` 的服务、接 `web.*` 请求，回到主线程（`a.win.Update`）取 `webDoc`，调 `EvalContext` 或导航；处理函数跟着请求的 ctx 结束。JS 放 `internal/app/webctl.js`（`embed`），`openWeb` 带上 `PreloadScript`。
2. CLI：`internal/cli/web.go`，用法写进 `-h`；`wait` 在 CLI 侧按超时轮询，窗口退出就报错结束。
4. 技能文档：给 agent 的说明（`internal/cli` 里已有 `*_guide.md` 的做法）加 `web_guide.md`，`posse web -h` 指过去。

### 验收

- 单测：窗口没开（socket 不存在、残留文件没人应答）、请求超时、工作区没有网页标签时的回复。离屏的 fake 后端 `Eval` 是空实现、`CapturePage` 返回假数据（`third_party/mygo/internal/fake`），snapshot 的 JS 只能在 PosseDev 里测。
- PosseDev.app：起一个本地页面（带表单和按钮），在终端里让 Claude 用 `posse web` 打开、snapshot、填表、点击、截图，结果和页面一致；关掉窗口后命令回"窗口没开"。
- 内存：打开网页前后窗口进程的 phys_footprint 和现在一样（只多 WebKit 那约 20 MB），常驻进程不变。

### 文档

`docs/features/ui.md` 的网页标签一条补 CLI；`docs/features/agents.md` 加一节"agent 操作网页"。

---

## 3. 侧栏显示 PR 状态和监听端口

### 现状

- 侧栏工作区行显示分支（`internal/app/view.go` 的 `navRow`，分支一行、路径一行）。分支先取常驻进程的 `workspaceInfo`，窗口每秒再用 `gitx.Head` 覆盖（`internal/app/app.go`）。
- Git 界面全在窗口进程调系统 git（`internal/gitx`），常驻进程不参与。
- 常驻进程能拿到终端的前台进程组（`s.Foreground()`）；`internal/procmem` 已经用 purego 接了 libproc，有子孙进程遍历（`Children` 即 `proc_listchildpids`，`TreeFootprint`）。

### 关键决策

- **PR**：窗口进程调 `gh pr view <分支> --json number,state,isDraft,url,statusCheckRollup`，放在 `internal/gitx/pr.go`（和其它 gitx 一样走 `runRaw`）。
  - 没装 `gh`、没登录、不是 GitHub 远端：不显示，不报错。
  - 频率：每个工作区 60 秒一次，切到这个工作区、推送成功后立即刷新；侧栏收起的项目不查。结果缓存在窗口进程内存里，不进 ui.json。
  - 显示：分支行尾加 `#123` 和一个状态点（检查通过/失败/进行中、草稿、已合并、已关闭）；点击用网页标签打开 PR（复用第 2 项之前就有的 `openWeb`）。颜色、尺寸用 `style.go` 里的名字。
- **端口**：常驻进程算，因为只有它知道每个终端的进程树。
  - 新 RPC `workspace.ports`：对工作区里每个活终端，用 `procmem.Children` 从 shell 的 pid 走出子孙进程（脱离出去的后台进程不算，和副本回收的口径一致），对每个进程 `proc_pidinfo(PROC_PIDLISTFDS)` 列描述符、`proc_pidfdinfo(PROC_PIDFDSOCKETINFO)` 取 TCP LISTEN 端口。fd→socket 的部分新开 `internal/procnet`，复用 procmem 注册好的 libproc，不调 lsof（一次要 100ms 以上）。
  - 不进每秒的轮询：窗口每 5 秒、且只对展开的工作区调一次；常驻进程不缓存不常驻 goroutine，低内存优先。
  - 显示：工作区行下面一行 `:3000 :5173`（最多三个，多的 `+N`），点击用网页标签打开 `http://localhost:<端口>`。
- 设置 › 外观加「侧栏显示 PR」「侧栏显示端口」两个开关，和现有的 `workspaceMetaHidden` 放一起。

### 步骤

1. `internal/procnet`：给定 pid 列表返回监听端口，单测起一个 `net.Listen` 验证能找到自己。
2. 常驻进程 `workspace.ports`：每个活终端从 shell pid 走子孙树，交给 procnet。
3. `internal/gitx/pr.go` + 单测（假的 `gh` 脚本放 PATH 前面）。
4. 侧栏 `navRow` 加 PR 和端口；离屏预览加一张"有 PR、有端口"的截图（`preview_test.go`）。

### 验收

- `go test ./internal/procnet ./internal/gitx ./internal/app`，离屏预览深浅两套看排版。
- PosseDev.app：在工作区终端里 `python3 -m http.server 8123`，5 秒内侧栏出现 `:8123`，点开是网页标签；停掉后消失。有 PR 的分支显示编号和检查状态。
- `footprint` 看常驻进程：加了端口查询后稳态不涨。

### 文档

`docs/features/ui.md` 侧栏一条；`docs/features/git.md` 加 PR 状态。

---

## 4. 不注入 hooks 也能识别更多 agent

### 现状

- `agent.Known` 只有 claude、codex、grok、cursor（`internal/agent/agent.go`）。用户自己启动的 agent 靠进程名识别（`agent.FromProcess`）、状态只靠标题（`TitleOnlyStatus`）。
- 其它 agent（Gemini CLI、opencode、Copilot CLI、Amp、Qwen Code 等）现在被当成普通终端。

### 关键决策

- 分两档，不把它们都做成一等公民：
  - **一等**（启动、续接、hooks、编排）：仍是现有四家，本项不动。
  - **识别档**：只识别"这个终端里跑着 X"和工作中/等待/空闲，显示图标和状态、参与通知（第 1 项）和 unread；不进新建菜单、不做续接和会话历史。
- 规则表 `internal/agent/watch.go`：`{名字, 进程名匹配, 标题规则, 屏幕规则}`。先看标题；标题不给信号的，退到看屏幕最后几行的固定提示（例如等待确认时的选项框文字），接进常驻进程每 2 秒一次的 `agentLoop`（`internal/daemon/terminal.go`，现在只做 `checkAgentGone`），用已有的 `Terminal.Screen()` 读可见屏幕，不读回滚。
- 每家的规则必须先实测再写，不照别人的文档抄：herdr 也承认这些 CLI 改界面就会失效。规则表里每一项注释写上核实的版本号。
- 图标：没有官方图标的用首字母圆标，不放第三方商标图片进 `resources/`。

### 步骤

1. 逐家装上实测（Gemini CLI、opencode、Copilot CLI 先做，其余按装得上的来）：记录进程名、工作中/等待/空闲时的标题和屏幕底部，写进本文件末尾的表。
2. `watch.go` 规则表 + `TitleOnlyStatus` 改成先查规则表；屏幕规则接到 `agentLoop`。
3. 单测：每家每种状态一条用例（标题和屏幕文本是实测记录下来的样本）。

### 验收

- `go test ./internal/agent ./internal/daemon`。
- PosseDev.app：在普通终端里手动启动每一家，侧栏出现图标，发一个请求时状态是工作中、要确认时是待处理、结束后是已完成并收到第 1 项的通知。

### 文档

`docs/features/agents.md` 加"识别档"一节和支持列表（带核实版本）。

---

## 6. 在普通终端里接入（`posse attach`）

### 现状

- 常驻进程的 `terminal.attach` 已经是"快照 + 实时输出 + 输入/尺寸帧"（`internal/daemon/attach.go`），快照是能在同尺寸终端里还原状态的 VT 序列（`internal/vt/snapshot.go`）。`internal/client/conn.go` 有现成的 `Conn`。
- `term.Terminal.Attach` 改尺寸并让接入方接管终端查询（DA/DSR）的应答，内核不再应答；窗口用的就是它。手机用 `Watch()`（不改尺寸、不接管应答）加 `HoldSize` 占住尺寸、`View` 记成在看（`internal/mobile/termstream.go`）；窗口的"手机尺寸"提示条看的是 `SizeHeld()`（`PhoneSized`）。
- 流式连接在常驻进程退出（交接、重启）时断开；窗口靠 `OnExit` 重新接入新进程（`internal/app/app.go`）。

### 关键决策

- `posse attach [终端]`：当前终端切到 raw 模式，进备用屏，写快照，然后双向转发；`SIGWINCH` 发尺寸帧。不带参数时列出终端让用户选（和 `terminal list` 同一份数据）。
- **退出键**：`Ctrl-\` 连按两次断开（第一次之后 1 秒内再按才算，单按照常发给程序），可用 `--detach-key` 改。断开只是断接入，终端照跑。
- **走手机那条路，不用 `Attach`**：给 `terminal.attach` 加可选参数 `hold`，带上时常驻进程用 `Watch` + `HoldSize` + `View`。这样不和窗口抢查询应答（否则窗口和外层终端各回一份）；查询由内核或窗口应答，外层终端自己的应答在 CLI 里丢掉（只丢 DA/DSR 这类应答序列，不碰用户输入）。
- **尺寸冲突**：attach 占着尺寸期间窗口显示现有的提示条（`SizeHeld()` 为真就自动出现，只改文案，从"手机"改成"别处"），断开时 `ReleaseSize`。
- **远程**：不做网络监听。远程用法是 `ssh mac -t posse attach <id>`，走 SSH 自己的加密和认证。
- 被 `Kicked`（行数变多时内核重建），或连接断开而终端没退出（交接、重启）时，按当前尺寸重新接入，交接的情况等新 socket 应答再接，和窗口的 `OnExit` 同一个思路。

### 步骤

1. 常驻进程：`terminal.attach` 的可选参数 `hold`（旧 CLI 不带就是原行为，不改交接记录）。
2. `internal/cli/attach.go`：raw 模式（用已有的 `golang.org/x/sys/unix` 的 `IoctlGetTermios`、`IoctlSetTermios`、`IoctlGetWinsize`，不加依赖）、备用屏、快照、转发、过滤外层终端的应答、退出键、SIGWINCH、Kicked 和交接后的重连。
3. 窗口提示条的文案从"手机"改成"别处"。

### 验收

- 单测：退出键的状态机。
- 端到端：`POSSE_HOME=/tmp/posse-e2e/home ./bin/posse` 起常驻进程和一个跑 `vim` 的终端，另一个终端里 `posse attach`，显示一致、能输入、改窗口大小 vim 跟着重排、两次 `Ctrl-\` 断开后终端还在；接着时跑 `posse daemon handoff`，attach 自动接到新进程上；`ssh localhost -t …` 同样能用。

### 文档

`docs/features/ui.md` 或新开 `docs/features/cli.md` 写 attach（终端切到别处尺寸时窗口的表现）。

---

## 5. 终端分屏

### 现状

- 主区域同一时间只画一个终端（`internal/app/view.go` 的 `mainBody`，`a.view` 是单个），只接入当前可见的终端，这是低内存的前提之一。
- 已有分栏线组件 `paneDivider`（`internal/app/logview.go`），拖动、松手写 ui.json。

### 关键决策

- **只做左右两栏**，不做任意嵌套：覆盖"agent + dev server/测试"这个主要场景，状态和键盘焦点都简单。
- 分屏是**工作区级**的视图状态：记"右栏是哪个终端、宽度比例"，存 ui.json（`splits`，按工作区 ID）。两栏都是标签栏里已有的终端，右栏不是新类型；标签栏里右栏的那个标签加一个标记。
- **接入**：两栏可见就接两个，仍守"只接入可见的终端"。`a.view` 改成 `a.views [2]`，焦点在哪栏、快捷键和查找栏就作用于哪栏。
- **尺寸**：两栏各自按自己的宽度接入；拖分栏线时只在松手后发尺寸，避免程序按每一帧的宽度重排。
- 快捷键（登记在 `keymap.go`）：「向右分屏」把当前终端放到右栏、左栏换成上一个标签；「关闭分屏」；「切换栏」。右键标签菜单加"在右侧打开"。
- 手机、任务、恢复终端都不感知分屏。

### 步骤

1. 重构：`a.view` → 按栏的数组，非测试代码里约 31 处引用都要过一遍：`view.go`（查找栏、右键菜单、拖文件、链接卡片）、`unread.go` 的 `markRead`、`fonts.go`、`termkeys.go`、`termmenu.go`，以及 `app.go` 里 `OnExit` 的 `a.view == v`（重连要按栏判断）。改成"焦点栏"或"对应的那一栏"。这一步单独提交，行为不变，先跑全部测试和离屏预览。
2. 分屏布局、分栏线、ui.json 字段、快捷键和菜单。
3. 离屏预览加分屏的深浅两张。

### 验收

- `go test ./internal/app`，离屏预览。
- PosseDev.app：左 Claude、右 `npm run dev`，两边都实时刷新、键盘只进焦点栏；拖分栏线松手后两边重排一次；关掉右栏的终端时分屏自动收起；重开窗口分屏还在。
- 内存：开分屏前后窗口进程 phys_footprint 的增量约等于多接入一个终端，常驻进程不变。

### 文档

`docs/features/ui.md` 的主区域部分；`docs/design.md` 如果分栏线有新的样式规则。

---

## 通用约束

- 每项一个分支或一组提交，`type(scope): 中文描述`，先过 `go vet ./... && env -u GIT_TRACE2_EVENT -u GIT_TRACE2_ENV_VARS go test ./... && go tool mygo vet`。
- 改了常驻进程的（第 3、6 项）只加 RPC 和可选字段，不改交接记录，不动 `handoffVersion`；验收时用 `posse daemon handoff` 换到新常驻进程，确认终端不断。
- 需要起 App 的验收一律用 `scripts/dev-app.sh` 装出的 PosseDev.app，不碰 `build/darwin-arm64/Posse.app` 和 `~/.posse`。
- 改界面的先读 `docs/design.md`，只用 `style.go` 里的名字。

## 第 4 项实测记录

| agent | 版本 | 进程名 | 工作中 | 等待确认 | 空闲 |
|---|---|---|---|---|---|
| （动手时填） | | | | | |
