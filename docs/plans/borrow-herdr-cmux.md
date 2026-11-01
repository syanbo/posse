# 借鉴 herdr、cmux 的改进

来源：herdr（Rust 写的终端 TUI，专管 agent）和 cmux（基于 libghostty 的 macOS 终端）。只取 Posse 还没有的部分。第 1、2、5 项（Dock 角标和通知开关、`posse web`、终端分屏）已做完，行为见 `docs/features/ui.md` 和 `docs/features/agents.md`；下面是还没做的三项，编号沿用原表。"现状"是写计划时对照代码的结果，动手前再核对一遍。

每项单独提交、单独验收，互不依赖：

| # | 内容 | 主要改动 | 规模 |
|---|---|---|---|
| 3 | 侧栏显示 PR 状态和监听端口 | gitx、daemon、app | 中 |
| 4 | 不注入 hooks 也能识别更多 agent | agent、daemon | 小（主要是逐家核实） |
| 6 | 在普通终端里接入（`posse attach`） | cli、daemon | 小 |

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
  - 显示：分支行尾加 `#123` 和一个状态点（检查通过/失败/进行中、草稿、已合并、已关闭）；点击用网页标签打开 PR（复用 `openWeb`）。颜色、尺寸用 `style.go` 里的名字。
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
  - **识别档**：只识别"这个终端里跑着 X"和工作中/等待/空闲，显示图标和状态、参与通知和 unread；不进新建菜单、不做续接和会话历史。
- 规则表 `internal/agent/watch.go`：`{名字, 进程名匹配, 标题规则, 屏幕规则}`。先看标题；标题不给信号的，退到看屏幕最后几行的固定提示（例如等待确认时的选项框文字），接进常驻进程每 2 秒一次的 `agentLoop`（`internal/daemon/terminal.go`，现在只做 `checkAgentGone`），用已有的 `Terminal.Screen()` 读可见屏幕，不读回滚。
- 每家的规则必须先实测再写，不照别人的文档抄：herdr 也承认这些 CLI 改界面就会失效。规则表里每一项注释写上核实的版本号。
- 图标：没有官方图标的用首字母圆标，不放第三方商标图片进 `resources/`。

### 步骤

1. 逐家装上实测（Gemini CLI、opencode、Copilot CLI 先做，其余按装得上的来）：记录进程名、工作中/等待/空闲时的标题和屏幕底部，写进本文件末尾的表。
2. `watch.go` 规则表 + `TitleOnlyStatus` 改成先查规则表；屏幕规则接到 `agentLoop`。
3. 单测：每家每种状态一条用例（标题和屏幕文本是实测记录下来的样本）。

### 验收

- `go test ./internal/agent ./internal/daemon`。
- PosseDev.app：在普通终端里手动启动每一家，侧栏出现图标，发一个请求时状态是工作中、要确认时是待处理、结束后是已完成并收到通知。

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

## 通用约束

- 每项一个分支或一组提交，`type(scope): 中文描述`，先过 `go vet ./... && env -u GIT_TRACE2_EVENT -u GIT_TRACE2_ENV_VARS go test ./... && go tool mygo vet`。
- 改了常驻进程的（第 3、6 项）只加 RPC 和可选字段，不改交接记录，不动 `handoffVersion`；验收时用 `posse daemon handoff` 换到新常驻进程，确认终端不断。
- 需要起 App 的验收一律用 `scripts/dev-app.sh` 装出的 PosseDev.app，不碰 `build/darwin-arm64/Posse.app` 和 `~/.posse`。
- 改界面的先读 `docs/design.md`，只用 `style.go` 里的名字。

## 第 4 项实测记录

| agent | 版本 | 进程名 | 工作中 | 等待确认 | 空闲 |
|---|---|---|---|---|---|
| （动手时填） | | | | | |
