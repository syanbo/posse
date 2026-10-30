# 第 5 项验收：终端左右分屏

改动见 `docs/plans/borrow-herdr-cmux.md` 第 5 项和 `docs/features/ui.md` 的"终端分屏"一条。代码在 worktree `/Users/dengbo/Desktop/AI/Apps/posse/.claude/worktrees/split`（分支 `feat/split`），下面的命令都在这个目录跑。

## 环境

- 只用这个 worktree 装出的 `build/dev/PosseDev.app`（包 ID `com.dengbo.posse.dev`，home `~/.posse-dev`）。不碰 `~/.posse`、不碰 `build/darwin-arm64/Posse.app`（worktree 里的 `build/darwin-arm64` 是指向主仓库的符号链接，只读）、不停日常的常驻进程。
- **不能抢焦点、移动鼠标或敲键盘**：用户在用这台电脑。窗口用 `open -g -n build/dev/PosseDev.app` 在后台打开，用 `osascript -e 'tell application id "com.dengbo.posse.dev" to quit'` 退出。界面操作和读取只用 computer-use skill（`/Users/dengbo/.claude/skills/computer-use/SKILL.md`，codex-cu MCP：按应用操作，能在后台给 PosseDev 发点击和按键就用；它要激活窗口才能操作的，不做）。做不到的标"未验证"并写原因。
- 注意：窗口被别的窗口完全挡住时 mygo 不重画，`screencapture -l` 截到的是旧画面，不能拿来判断界面变化。界面上的结果以 computer-use 读到的为准；能从命令行确认的（ui.json、终端尺寸、输出）用命令行。
- 命令行先设好（在 Posse 的终端里跑时环境里带着日常的 `POSSE_*`，必须覆盖）：

  ```sh
  cd /Users/dengbo/Desktop/AI/Apps/posse/.claude/worktrees/split
  export GOPROXY=https://goproxy.cn,direct
  export POSSE_HOME=$HOME/.posse-dev
  export POSSE_SOCKET=$POSSE_HOME/possed.sock
  unset POSSE_TERMINAL_ID POSSE_WORKSPACE_ID
  P=build/dev/PosseDev.app/Contents/MacOS/Posse
  W=w_6668f5f7   # proj/main（/tmp/posse-accept/proj），不在了就看 $P workspace list
  ```

- 包已经装好。改过代码才需要重装：`sh scripts/dev-app.sh`，再退出、重开窗口。
- 只在 `proj/main` 里建终端、改分屏，别动别的工作区。建两个会持续输出的终端：

  ```sh
  T1=$($P terminal create $W --json -- sh -c 'i=0; while :; do i=$((i+1)); echo "left $i"; sleep 1; done' | python3 -c 'import json,sys;print(json.load(sys.stdin)["result"]["id"])')
  T2=$($P terminal create $W --json -- sh -c 'i=0; while :; do i=$((i+1)); echo "right $i"; sleep 1; done' | python3 -c 'import json,sys;print(json.load(sys.stdin)["result"]["id"])')
  ```

  已有的测试终端（`$P terminal list --workspace $W`）可以先关掉。
- 看分屏状态：`python3 -c 'import json,os;print(json.load(open(os.path.expanduser("~/.posse-dev/ui.json"))).get("splits"))'`。看两个终端的尺寸：`$P terminal list --workspace $W --json` 里的 `cols`、`rows`（分屏时两边按各自宽度接入，列数不同；没分屏时只有选中的那个按整宽）。
- 截图、日志放 `/tmp/posse-accept5/`。

## 用例

每条记：步骤、期望、实际（命令输出或 computer-use 读到的内容）、通过/不通过/未验证（附原因）。

| # | 场景 | 步骤 | 期望 |
|---|---|---|---|
| S1 | 向右分屏 | 侧栏选 proj，标签栏选 T2，焦点在终端里按 ⌘D | 左栏 T1、右栏 T2，两边都在刷新；ui.json 的 splits 有 `{left: T1, right: T2, focus: 1}`；两个终端的 cols 不同、加起来约等于原来整宽的列数；两个标签上各有一个左栏/右栏的小图标 |
| S2 | 只有一个终端时分屏 | 关掉 T2（`$P terminal close $T2`）；只剩 T1 时焦点在 T1 里按 ⌘D | 新开一个 shell 放在右栏（`terminal list` 多一个），左栏是 T1 |
| S3 | 切换栏 | 分屏时按 ⌘]，再按 ⌘[ | 焦点在两栏间切换：标签栏选中的标签跟着换，ui.json 的 focus 跟着变；没有焦点的那栏光标是空心的 |
| S4 | 键盘只进焦点栏 | 把两个终端换成 shell（或用 S2 的 shell），焦点在右栏时打 `echo RIGHT` 回车 | 只有右栏的终端出现 RIGHT（`$P terminal read <id> --lines 5` 核对左栏没有） |
| S5 | 点进另一栏 | 点左栏的终端区域 | 焦点和选中的标签换成左栏的终端 |
| S6 | 选不在分屏里的终端 | 再建一个终端 T3；分屏时焦点在右栏，在标签栏点 T3 | 右栏换成 T3，左栏不变；ui.json 的 right 是 T3 |
| S7 | 拖分栏线 | 拖中间的分栏线往左一段，拖的过程中看两边，松手 | 拖的时候两栏不变，只有一条强调色的线跟着指针；松手后两栏宽度变了、ui.json 有 percent；`terminal list` 的 cols 只在松手后变一次（拖的过程中隔 0.5 秒连读几次 cols 不变） |
| S8 | 拖的范围 | 把分栏线拖到最左、最右 | 左栏不小于约 20%、不大于约 80% |
| S9 | 关闭分屏 | 焦点在左栏时按 ⇧⌘D | 只剩左栏的终端，两个终端都还在（`terminal list`）；ui.json 没有这个工作区的 splits |
| S10 | 标签右键 | 右键一个不在分屏里的标签选「在右侧打开」；再右键分屏里的标签 | 它到了右栏；分屏里标签的右键菜单是「关闭分屏」，选了收起 |
| S11 | 关掉分屏里的终端 | 分屏时 `$P terminal close <右栏的终端>`；再开一次分屏，这次在窗口里点右栏标签上的关闭按钮关掉它 | 两次都自动收起，左栏的终端占满；CLI 关掉的那次 ui.json 里 splits 记录还在（不再显示），窗口里关掉的那次记录被删 |
| S12 | 重开窗口 | 分屏开着（比例拖过）时退出窗口再 `open -g -n` | 分屏、比例、焦点栏都还在 |
| S13 | 切到文件标签再回来 | 分屏时切到文件标签（⌘P 打开一个文件，或点文件栏），再点回终端标签 | 文件标签占满主区域；回来后分屏还在 |
| S14 | 文件里的 ⌘D、⌘] 不被抢 | 文件标签里的编辑器有焦点时按 ⌘] | 是编辑器的缩进，不是切换栏 |
| S15 | 查找栏、会话按钮 | 分屏时焦点栏按 ⌘F 搜一个词 | 查找栏只出现在焦点栏，搜的是那一栏的终端 |
| S16 | 内存 | 不分屏时 `footprint -p <窗口 pid>` 记 phys_footprint；分屏后再记；常驻进程（`$P daemon status`）前后各记一次 | 窗口进程的增量约等于多接入一个终端（几 MB 到十几 MB）；常驻进程不变 |
| S17 | 离屏预览 | `POSSE_PREVIEW_DIR=/tmp/posse-accept5/preview go test ./internal/app -run TestPreview`，看 `terminal-split-dark.png`、`terminal-split-light.png` | 左右两栏、中间分栏线，标签上有左栏/右栏图标，文字不截断、不重叠 |

## 自动化检查

```sh
unset GIT_TRACE2_EVENT GIT_TRACE2_ENV_VARS
go vet ./... && go test ./... && go tool mygo vet
go test ./internal/app -run 'PaneIDs|SplitRight|SwitchAndClose|EnsurePanes' -count=3 -v
```

`TestGitConsolePerProject` 偶发失败（临时目录清理时后台写文件的协程还没退出，和分屏无关），失败了重跑一次并在报告里记下。

## 收尾

- 关掉测试建的终端（只关 proj 里的）；PosseDev 的窗口和常驻进程留着。
- 报告写到 `/tmp/posse-accept5/report.md`：每条用例的结果表、发现的问题（复现步骤、期望、实际），不改仓库里的代码。
