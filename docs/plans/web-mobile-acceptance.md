# 验收：网页标签 ⌘R 刷新和手机预览

改动见 `docs/features/ui.md` 网页标签一条（⌘R、⇧⌘R、手机预览）、`docs/features/agents.md` 的 `posse web device`、`posse web guide`。代码在 worktree `/Users/dengbo/Desktop/AI/Apps/posse/.claude/worktrees/web-mobile`（分支 `feat/web-mobile`），下面的命令都在这个目录跑。

## 环境

- 只用这个 worktree 装出的 `build/dev/PosseDev.app`（包 ID `com.dengbo.posse.dev`，home `~/.posse-dev`）。不碰 `~/.posse`、不碰 `build/darwin-arm64/Posse.app`（worktree 里的 `build/darwin-arm64` 是指向主仓库的符号链接，只读）、不停日常的常驻进程。
- **不能抢焦点、移动鼠标或敲键盘**：用户在用这台电脑。窗口用 `open -g -n build/dev/PosseDev.app` 在后台打开，用 `osascript -e 'tell application id "com.dengbo.posse.dev" to quit'` 退出。界面操作和读取只用 computer-use skill（`/Users/dengbo/.claude/skills/computer-use/SKILL.md`，能在后台给 PosseDev 发按键、点击就用；要激活窗口才能做的不做）。做不到的标"未验证"并写原因。
- 窗口被别的窗口完全挡住时 mygo 不重画，`screencapture -l` 截到的是旧画面，不能当界面证据；页面的结果用 `posse web eval`、`posse web screenshot` 核对（截的是页面本身，不受遮挡影响）。
- 命令行先设好（在 Posse 的终端里跑时环境里带着日常的 `POSSE_*`，必须覆盖）：

  ```sh
  cd /Users/dengbo/Desktop/AI/Apps/posse/.claude/worktrees/web-mobile
  export POSSE_HOME=$HOME/.posse-dev
  export POSSE_SOCKET=$POSSE_HOME/possed.sock
  unset POSSE_TERMINAL_ID POSSE_WORKSPACE_ID
  P=build/dev/PosseDev.app/Contents/MacOS/Posse
  W=w_6668f5f7   # proj/main（/tmp/posse-accept/proj），不在了就看 $P workspace list
  ```

  网页命令都带 `--workspace $W`。窗口侧栏要选中 proj（截图要求标签上过屏），没选中就用 computer-use 点侧栏的 proj。
- 测试页：`/tmp/posse-accept6/site/resp.html`（内容见文末），`python3 -m http.server 8767 --directory /tmp/posse-accept6/site` 后台起，curl 为 200 再开始，验收完停掉。页面显示 `innerWidth`、UA，宽度 ≤600 时底色变黄、写 "mobile layout"。
- 截图、日志放 `/tmp/posse-accept6/`。

## 用例

每条记：步骤、期望、实际（命令输出或 computer-use 读到的内容）、通过/不通过/未验证（附原因）。

| # | 场景 | 步骤 | 期望 |
|---|---|---|---|
| M1 | ⌘R 刷新（网页有焦点） | `$P web open localhost:8767/resp.html`、`wait`；`eval 'window.__mark = 1; return 1'`；computer-use 点进页面让它有焦点，按 ⌘R；`wait` 后 `eval 'return window.__mark === undefined'` | `true`（页面重新载入过，标记没了） |
| M2 | ⌘R 刷新（地址栏有焦点） | 同上打标记，点地址栏让它有焦点再按 ⌘R | 页面重新载入（标记没了），地址栏文字不变 |
| M3 | ⇧⌘R 强制刷新 | 打标记后按 ⇧⌘R；看 http.server 的日志 | 标记没了；服务日志里多一条 `GET /resp.html` 且状态是 200（不是 304） |
| M4 | 终端里 ⌘R 不起作用 | 切到 proj 里的一个终端（没有就 `$P terminal create $W`），焦点在终端里按 ⌘R | 终端没有收到字符（`$P terminal read <id> --lines 3` 没有多出东西），网页标签也没刷新（回去 eval 标记还在） |
| M5 | View 菜单 | computer-use 读菜单栏的 View 菜单 | 有「重新载入网页 ⌘R」「强制重新载入网页 ⇧⌘R」「手机预览 ⇧⌘M」 |
| M6 | 工具栏开手机预览 | 点网页标签工具栏的手机按钮，菜单里选「iPhone 15/16  393×852」；`wait`；`eval 'return innerWidth + " " + navigator.userAgent'` | `393`，UA 含 iPhone；页面显示 mobile layout（`screenshot` 看是黄底窄页面）；工具栏手机按钮是强调色底；网页上面一行写「iPhone 15/16 · 393×852」（窗口矮时后面带缩放百分比） |
| M7 | 机型和横屏 | 菜单里依次选 iPhone SE、Pixel 8、iPad mini，再勾「横屏」；每次 `wait` 后 eval innerWidth 和 UA | 375 / 412 / 768，横屏后 1024；UA 分别是 iPhone、Android（Pixel 8）、iPad |
| M8 | 窗口放不下时缩小 | 选 iPad mini 横屏（1024×768），窗口比它小时 eval `innerWidth` | 还是 1024（排版宽度不变）；`screenshot` 的图比 1024 逻辑像素小（整体缩小了）；上面一行带缩放百分比 |
| M9 | ⇧⌘M 开关 | 手机预览开着时按 ⇧⌘M，eval；再按一次，eval | 第一次关掉：innerWidth 回到电脑宽度、UA 不含 iPhone/Android；第二次用上次选的机型打开；`~/.posse-dev/ui.json` 的 `webDevice` 是上次选的机型 |
| M10 | 选「电脑」关掉 | 手机预览开着时在手机按钮的菜单里选「电脑」 | 回到原来的大小和 UA（页面重新载入一次） |
| M11 | 按标签记 | 一个标签开手机预览，再 `web open` 一个新标签 | 新标签是电脑模式；切回原标签仍是手机预览 |
| M12 | `posse web device` | `$P web device iphone`、`wait`、eval、`screenshot -o /tmp/posse-accept6/m12.png`；`device ipad --landscape`；`device nokia`；`device off`；`--json` 再跑一次 iphone | 打印 `tab N: iPhone 15/16 393x852 (reloading; run posse web wait)`，eval 393 和 iPhone UA，截图是手机尺寸；横屏 1024；nokia 报 `bad_request: unknown device "nokia"; use one of iphone, se, pixel, ipad or off`；off 打印 `desktop`；json 里有 device、width、height |
| M13 | 快捷键能改 | 设置 › 快捷键里搜「网页」 | 「网页」分组有三项，默认 ⌘R、⇧⌘R、⇧⌘M；改一个后 View 菜单上的快捷键跟着变（改完改回默认） |
| M14 | 帮助和说明 | `$P web device -h`；`$P web guide \| grep -A1 device` | 打印用法；说明里有 device 一行和"不模拟触摸事件"的限制 |

## 自动化检查

```sh
unset GIT_TRACE2_EVENT GIT_TRACE2_ENV_VARS
go vet ./... && go test ./... && go tool mygo vet
go test ./internal/app -run 'DeviceZoom|SetWebDevice|WebDeviceRequest' -count=3 -v
```

## 收尾

- 停掉 8767 的服务；关掉测试建的终端（只关 proj 里的）；PosseDev 的窗口和常驻进程留着。
- 报告写到 `/tmp/posse-accept6/report.md`：每条用例的结果表、发现的问题（复现步骤、期望、实际），不改仓库里的代码。

## 测试页 resp.html

```html
<!doctype html>
<html><head><title>Responsive Test</title>
<style>
body { font: 18px -apple-system, sans-serif; margin: 0; padding: 16px; background: #dbeafe; }
.layout::after { content: "desktop layout"; font-weight: 700; }
@media (max-width: 600px) { body { background: #fde68a; } .layout::after { content: "mobile layout"; } }
</style></head>
<body>
<h1>Responsive Test</h1>
<p class="layout"></p>
<p id="w"></p>
<p id="ua" style="font-size:13px;word-break:break-all"></p>
<script>
const show = () => { document.getElementById("w").textContent = "innerWidth=" + innerWidth + " innerHeight=" + innerHeight; };
show(); addEventListener("resize", show);
document.getElementById("ua").textContent = navigator.userAgent;
</script>
</body></html>
```
