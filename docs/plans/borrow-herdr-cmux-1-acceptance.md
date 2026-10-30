# 第 1 项验收：Dock 角标、通知开关、已完成通知跟着未读

改动见 `docs/plans/borrow-herdr-cmux.md` 第 1 项和 `docs/features/ui.md` 的"系统通知和 Dock 角标"一条。

## 环境

- 只用 PosseDev.app（包 ID `com.dengbo.posse.dev`，home `~/.posse-dev`）。不碰 `~/.posse`、不碰 `build/darwin-arm64/Posse.app`、不停日常的常驻进程。
- 下面的命令都先设好（在 Posse 的终端里跑时环境里带着日常的 `POSSE_*`，必须覆盖）：

  ```sh
  cd /Users/dengbo/Desktop/AI/Apps/posse
  export GOPROXY=https://goproxy.cn,direct
  export POSSE_HOME=$HOME/.posse-dev
  export POSSE_SOCKET=$POSSE_HOME/possed.sock
  unset POSSE_TERMINAL_ID POSSE_WORKSPACE_ID
  P=build/dev/PosseDev.app/Contents/MacOS/Posse
  ```

- 装包并换上新常驻进程：`scripts/dev-app.sh`；窗口开着就先 ⌘Q 退出 PosseDev，再 `open -n build/dev/PosseDev.app`。`$P daemon status` 看 home 是 `~/.posse-dev`、构建是刚装的（不是就 `$P daemon handoff`）。
- 角标读法（不用看图）：`lsappinfo info -only StatusLabel "$(lsappinfo find bundleid=com.dengbo.posse.dev)"`，输出里的 `"label"="3"` 是数字，没有 label 或为空是没有角标。
- 通知看法：`screencapture -x /tmp/posse-accept/<用例>.png` 截屏看右上角横幅（横幅约 5 秒消失，触发后 1–2 秒内截）；或打开通知中心截图。截图都放 `/tmp/posse-accept/`。
- 模拟 agent 状态（确定、免费）：在 PosseDev 里建一个普通终端，用 hook 把它标成 Claude 并改状态：

  ```sh
  W=$($P workspace list --json | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["id"])')
  T=$($P terminal create "$W" --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
  hook() { if [ -n "${2:-}" ]; then p=$2; else p='{}'; fi; printf '%s' "$p" | POSSE_TERMINAL_ID=$T $P hook claude "$1"; }
  # 工作中：hook UserPromptSubmit   等待：hook Notification '{"notification_type":"permission_prompt"}'
  # 空闲（本轮完成）：hook Stop
  ```

  `$P terminal list --json` 能看到这个终端的 `agentState.status` 和 `unread`，每一步先确认常驻进程那边的状态对，再看窗口的反应（窗口每秒轮询，等 2 秒）。
  若 `workspace list` 为空，先 `$P project add <某个临时目录>`（用 `/tmp/posse-accept/proj`，别加真仓库）。

## 用例

每条记：步骤、期望、实际、截图或命令输出、通过/不通过/未验证（附原因）。

| # | 场景 | 步骤 | 期望 |
|---|---|---|---|
| A1 | 等待时发通知、角标 +1 | 窗口里切到别的工作区（或切到别的 App，让窗口失去焦点）；`hook UserPromptSubmit`，再 `hook Notification '{"notification_type":"permission_prompt"}'` | 出现「Claude 等待你处理」横幅，正文是工作区名；角标 1 |
| A2 | 看过后等待仍计数 | 在窗口里切到这个终端看它 | 角标仍是 1（等待没处理完，不因看过而清） |
| A3 | 完成且未读：通知 + 铃铛 + 角标 | 切走（看别的工作区）；`hook UserPromptSubmit`，再 `hook Stop` | 出现「Claude 已完成」横幅；`terminal list` 里 `unread: true`；侧栏铃铛；角标 1 |
| A4 | 看过后角标清零 | 窗口获得焦点，点到这个会话 | 2 秒内 `unread` 变 false，角标消失 |
| A5 | 正看着时不打扰 | 窗口有焦点并停在这个终端；`hook UserPromptSubmit`、`hook Stop` | 不出横幅；角标不变（看着时窗口下一帧就报看过） |
| A6 | 关掉系统通知 | 设置 › 通用 › 通知，关「系统通知」；重复 A1 的步骤 | 不出横幅；角标照常变化；`~/.posse-dev/ui.json` 有 `"notifyOff": true` |
| A7 | 关掉 Dock 角标 | 在有角标时关「Dock 角标」 | 角标马上消失；之后状态变化也不出现；ui.json 有 `"badgeOff": true`；再打开后角标回到正确的数 |
| A8 | 开关重开窗口后保留 | A6/A7 关着时 ⌘Q 退出窗口再 `open -n` | 设置页两个开关仍是关；重新打开后恢复默认 |
| A9 | 多个会话计数 | 再建一个终端 T2，T 设成等待，T2 走完一轮（UserPromptSubmit→Stop）且不看 | 角标 2；看了 T2 后变 1；T 回到工作中（`hook UserPromptSubmit`）后 0 |
| A10 | 普通终端、关掉的终端不计 | 不经 hook 的普通终端随便跑命令；关掉 T（`$P terminal close $T`） | 普通终端不影响角标；关掉 T 后它的计数去掉 |
| A11 | 点通知跳转 | A1 或 A3 的横幅出现时点它（能操作鼠标的话；不能就标"未验证"） | 窗口到前面并切到那个终端 |
| A12 | 真实 Claude（可选，花用量） | `$P terminal create "$W" --agent claude --prompt "运行 ls 然后结束"`，切走等它跑完 | 收到「Claude 已完成」，角标 +1，看过后清 |
| A13 | 设置页排版 | `POSSE_PREVIEW_DIR=/tmp/posse-preview go test ./internal/app -run TestPreview`，看设置通用页深浅两张（文件名含 settings） | 「通知」卡片和其它卡片对齐，文字不截断、不重叠 |

## 自动化检查

```sh
go vet ./... && env -u GIT_TRACE2_EVENT -u GIT_TRACE2_ENV_VARS go test ./... && go tool mygo vet
go test ./internal/app -run 'TestNoticeTitle|TestBadgeCount|TestNoticeStatus' -v
```

## 收尾

- 关掉建的测试终端；PosseDev 的常驻进程留着即可（`daemon status` 确认是 `~/.posse-dev` 再决定要不要 `daemon stop`）。
- 报告写到 `/tmp/posse-accept/report.md`：每条用例的结果表、发现的问题（复现步骤、期望、实际），不改仓库里的代码。
