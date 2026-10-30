# 第 2 项验收：agent 用 `posse web` 操作网页标签

改动见 `docs/plans/borrow-herdr-cmux.md` 第 2 项、`docs/features/agents.md` 的"agent 操作网页"一条、`posse web guide`（源文件 `internal/cli/web_guide.md`）。代码在 worktree `/Users/dengbo/Desktop/AI/Apps/posse/.claude/worktrees/web-cli`（分支 `feat/web-cli`），下面的命令都在这个目录跑。

## 环境

- 只用这个 worktree 装出的 `build/dev/PosseDev.app`（包 ID `com.dengbo.posse.dev`，home `~/.posse-dev`）。不碰 `~/.posse`、不碰 `build/darwin-arm64/Posse.app`（worktree 里的 `build/darwin-arm64` 是指向主仓库的符号链接，只读）、不停日常的常驻进程。
- **不能抢焦点、移动鼠标或敲键盘**：用户在用这台电脑。窗口用 `open -g -n build/dev/PosseDev.app` 在后台打开，用 `osascript -e 'tell application id "com.dengbo.posse.dev" to quit'` 退出；要看窗口界面时用 computer-use skill（`/Users/dengbo/.claude/skills/computer-use/SKILL.md`）读界面，不点、不激活窗口。做不到的标"未验证"并写原因。
- 命令行先设好（在 Posse 的终端里跑时环境里带着日常的 `POSSE_*`，必须覆盖）：

  ```sh
  cd /Users/dengbo/Desktop/AI/Apps/posse/.claude/worktrees/web-cli
  export GOPROXY=https://goproxy.cn,direct
  export POSSE_HOME=$HOME/.posse-dev
  export POSSE_SOCKET=$POSSE_HOME/possed.sock
  unset POSSE_TERMINAL_ID POSSE_WORKSPACE_ID
  P=build/dev/PosseDev.app/Contents/MacOS/Posse
  ```

- 包已经装好。改过代码才需要重装：`sh scripts/dev-app.sh`，再退出、重开窗口。
- 工作区：`$P workspace list`。用 `proj/main`（目录 `/tmp/posse-accept/proj`）做测试，别往别的工作区开网页；没有就 `$P project add /tmp/posse-accept/proj`。下面 `W` 是它的 ID，命令都带 `--workspace $W`（不在 PosseDev 的终端里时，不带就用窗口侧栏选中的工作区）。
- 测试页：在 `/tmp/posse-accept2/site/` 放 `index.html`（内容见文末）和 `other.html`（`<!doctype html><title>Other</title><h2>Other page</h2>`），`python3 -m http.server 8766 --directory /tmp/posse-accept2/site` 起服务（后台跑，验收完停掉）。
- 截图、输出都放 `/tmp/posse-accept2/`。

## 用例

每条记：步骤、期望、实际（贴命令输出）、通过/不通过/未验证（附原因）。

| # | 场景 | 步骤 | 期望 |
|---|---|---|---|
| B1 | 窗口没开 | 退出 PosseDev 窗口；`ls ~/.posse-dev/app.sock`；`$P web list --workspace $W`、加 `--json` 再跑一次 | socket 文件已删；报 `no_window`，提示让用户打开 Posse；`--json` 是 `{"ok":false,"error":{"code":"no_window",…}}`，退出码 1；没有拉起窗口 |
| B2 | 残留 socket | 窗口开着时 `kill -9` 窗口进程（`pgrep -f 'web-cli/build/dev/PosseDev.app/Contents/MacOS/Posse$'`，只杀窗口，不杀 `daemon run`）；确认 app.sock 还在；`$P web list --workspace $W`；再 `open -g -n` | 杀掉后命令报 `no_window`（不是卡住）；重开后 `web list` 正常，socket 权限 `srw-------` |
| B3 | 打开和列出 | `$P web open localhost:8766 --workspace $W`；`$P web wait --workspace $W`；`$P web list --workspace $W` | 打印 `tab N  http://localhost:8766`；list 有这一项、标 `*`、标题 `Posse Web Test`、不再 loading。用 computer-use 读窗口：若 proj 是选中的工作区，文件标签里出现这个网页标签 |
| B4 | snapshot | `$P web snapshot --workspace $W` | 列出 heading "Sign up"、navigation 下的 link "Other page" -> /other.html、form 下的 textbox "Email"、combobox "Plan" value="Free"、checkbox "I agree"、button "Create account"，可交互的带 `[eN]`；隐藏的按钮 "Hidden" 不出现；地标后面没有一长串文字 |
| B5 | 填表提交 | 用 snapshot 给的 ref：`fill <email> alice@example.com`、`fill <plan> Pro`、`click <agree>`、`click <button>`；`wait --for text Welcome`；再 snapshot | 每步打印 `ok <网址>`；wait 成功；`eval "return document.getElementById('out').textContent"` 是 `"Welcome alice@example.com plan=pro agree=true"`；snapshot 里 value、checked 都更新，ref 不变 |
| B6 | 按键 | 刷新（`reload`、`wait`）后 `fill <email> bob@example.com`、`press Enter --on <email>`；`wait --for text Welcome`；`press Tab`；snapshot 看 `focused` 在哪 | Enter 提交了表单（出现 Welcome bob…）；Tab 后焦点移到下一个可聚焦元素 |
| B7 | 导航 | `click <Other page 的 ref>`、`wait`、`list`；`back`、`wait --for selector '#email'`；`forward`；`goto localhost:8766`；`goto 'not a url'`；在没有后退历史的新标签上 `back` | 点链接后网址是 /other.html、标题 Other；back、forward 生效；坏网址报 `bad_request`；没有历史时报 `conflict: no page to go back to` |
| B8 | wait 超时 | `wait --for text NeverThere --timeout 1s`；`echo $?` | 约 1 秒后报 `timeout`，退出码 3 |
| B9 | eval | `eval 'document.title'`；`eval 'const x = 1; return x + 1'`；`eval 'throw new Error("nope")'`；`echo 'return location.pathname' \| $P web eval - --workspace $W`；`eval '({a:[1,2]})' --json` | `"Posse Web Test"`；`2`；报 `js_error: nope`；`"/"`；JSON 里 result 是 `{"a":[1,2]}` |
| B10 | console | 新开页面后 `console` | 有 `[warn] page loaded`、`[error] Error: boom later (…:14)`，提交后有 `[log] submitted …`；没有 `Script error.` 这样没内容的条目（B9 跑过语句形式的 eval 之后也没有） |
| B11 | 截图 | `screenshot -o /tmp/posse-accept2/b11.png`；不带 `-o` 再来一次；打开图片看 | 打印路径，PNG 能打开，内容是页面当前状态；不带 `-o` 时路径在 `$TMPDIR` 下 |
| B12 | 多个标签 | 再 `open localhost:8766/other.html`；`list`；`snapshot`（不带 --tab）；`snapshot --tab <第一个>`；`screenshot --tab <第一个>`；`snapshot --tab 99` | list 两项，新开的标 `*`；不带 --tab 操作新开的；--tab 能操作第一个（被遮住的标签也能截图）；99 报 `not_found … see posse web list` |
| B13 | 从没显示过的标签 | 窗口侧栏选中的不是 proj 时（用 computer-use 读侧栏确认；是 proj 就把测试换到另一个工作区：再 `project add` 一个临时目录，别加真仓库）在 proj 里 `open`、`wait`、`snapshot`、`screenshot` | snapshot 正常；screenshot 报 `conflict: tab N has never been on screen…`，不写空文件 |
| B14 | 终端里按所在工作区 | `$P terminal create $W -- sh -c 'posse web list > /tmp/posse-accept2/b14.txt 2>&1; posse web snapshot >> /tmp/posse-accept2/b14.txt 2>&1'`，等 2 秒看文件；关掉这个终端 | 不带 --workspace 也列出 proj 的网页标签（用的是终端的 `POSSE_WORKSPACE_ID`） |
| B15 | 外部网站 | `open https://example.com`、`wait`、`snapshot`、`screenshot` | snapshot 有 heading 和 link（"More information…" 或 "Learn more"），截图正常 |
| B16 | 帮助 | `$P web`、`$P web -h`、`$P web guide`、`$P web fill -h`、`$P -h \| grep web` | `posse web` 和 `web guide` 打印说明；`-h` 打印用法；总帮助里列出 web 命令并提到 `posse web guide` |
| B17 | 内存 | 只开 PosseDev 窗口、没开网页时 `footprint -p <窗口 pid>` 记 phys_footprint；开两个网页标签后再记；常驻进程（`$P daemon status` 的 pid）前后各记一次 | 窗口进程只多 WebKit 约 20 MB 上下；常驻进程不变（差几 MB 内） |
| B18 | 真实 agent（花用量，用 haiku） | `$P terminal create $W --agent claude --model haiku --prompt "Run 'posse web guide' and follow it. Open http://localhost:8766 in a web tab, sign up with email test@example.com, plan Pro, agree checked, then take a screenshot and tell me the message shown on the page."`；等它做完（`$P terminal wait <终端> --for idle --timeout 5m`），`$P terminal read <终端> --lines 60` | 它用 posse web 完成了填表和截图，报出 `Welcome test@example.com plan=pro agree=true`；看完关掉终端 |

## 自动化检查

```sh
unset GIT_TRACE2_EVENT GIT_TRACE2_ENV_VARS
go vet ./... && go test ./... && go tool mygo vet
go test ./internal/app ./internal/cli -run 'Web|Ctl|FindWeb|CurrentWeb|OnMain' -v
```

## 收尾

- 关掉测试开的网页标签不必（窗口退出就没了）；关掉测试建的终端；停掉 8766 的服务；PosseDev 的常驻进程留着（`daemon status` 确认是 `~/.posse-dev` 再决定要不要 `daemon stop`）。
- 报告写到 `/tmp/posse-accept2/report.md`：每条用例的结果表、发现的问题（复现步骤、期望、实际），不改仓库里的代码。

## 测试页 index.html

```html
<!doctype html>
<html><head><title>Posse Web Test</title></head>
<body>
<h1>Sign up</h1>
<nav><a href="/other.html">Other page</a></nav>
<form id="f" onsubmit="event.preventDefault(); document.getElementById('out').textContent='Welcome ' + document.getElementById('email').value + ' plan=' + document.getElementById('plan').value + ' agree=' + document.getElementById('agree').checked; console.log('submitted', document.getElementById('email').value);">
  <label for="email">Email</label> <input id="email" type="email" placeholder="you@example.com">
  <label>Plan <select id="plan"><option value="free">Free</option><option value="pro">Pro</option></select></label>
  <label><input id="agree" type="checkbox"> I agree</label>
  <button type="submit">Create account</button>
</form>
<button style="display:none">Hidden</button>
<p id="out"></p>
<script>console.warn('page loaded'); setTimeout(() => { throw new Error('boom later') }, 100);</script>
</body></html>
```

## 复测：code review 修正

环境同上（同一个 worktree、PosseDev、`P`、`W`、8766 的测试页服务）。包已经按修正后的代码重装、窗口已重开。测试页目录再放一个 `shadow.html`（内容见本节末尾）。

| # | 场景 | 步骤 | 期望 |
|---|---|---|---|
| R1 | shadow root 里的元素 | `open localhost:8766/shadow.html`、`wait`、`snapshot`；`click <Inside 的 ref>`；`wait --for text "shadow clicked" --timeout 3s` | snapshot 有 `[eN] button "Inside"`；click 成功（不报 no element matches）；wait 成功 |
| R2 | role=heading 的级别 | 同一页的 snapshot | `heading level=2 "Settings"`；`heading "No level"`（没有 level，不出现 `level=pan` 这类） |
| R3 | 选择器写错 | `wait --for selector 'button:contains(Save)' --timeout 10s`，记用时和 `$?` | 马上（远小于 10 秒）报 `js_error: … is not a valid selector`，退出码 1，不是 timeout |
| R4 | 跳转后马上 wait | 回到 `localhost:8766`，循环 10 次：`reload`、`wait`、`snapshot`、`fill <email> u$i@example.com`、`click <提交>`、`wait --for text "Welcome u$i" --timeout 5s`、`snapshot`、`click <Other page>`、`wait --for text "Other page" --timeout 5s`、`back`、`wait --for selector '#email' --timeout 5s`；数失败次数 | 0 次失败（每次 reload、跳转后 ref 会重编，所以每轮先 snapshot） |
| R5 | 等待中标签被销毁 | 后台起 `$P web wait --workspace $W --for text NeverThere --timeout 30s; echo "exit $?"`（输出到文件）；1 秒后 `$P project rm <proj 的项目 ID>`（folder 项目，文件留着；ID 见 `$P project list`）；等 wait 结束；看窗口进程还在不在（`pgrep -f 'web-cli/build/dev/PosseDev.app/Contents/MacOS/Posse$'`）、`$P web list --workspace $W`；最后 `$P project add /tmp/posse-accept/proj` 加回来 | wait 在几秒内结束，报 `not_found`（工作区不在了或 `web tab N was closed`），不是一直等到 30 秒；窗口进程还活着（没有崩溃）；加回来后是新的工作区 ID，后面的用例用新 ID |
| R6 | 回归 | 在新工作区里重跑 B3、B4、B5、B7、B9、B10 的命令 | 结果和第一轮一致 |
| R7 | 单测 | `go test ./internal/app ./internal/cli -run 'Web|Ctl|FindWeb|CurrentWeb|OnMain' -count=3 -v` | 全过，含 `TestOnMainCanceled`（超时后主线程不再执行）和 `TestWebTimeout` 里拨号时就到期报 timeout 的一段 |

地址栏回车跳转（和 `web goto` 共用了 `loadWeb`）要在窗口里打字，按约束不做，标"未验证"即可。

报告写到 `/tmp/posse-accept2/report-r.md`。

### shadow.html

```html
<!doctype html>
<html><head><title>Shadow Test</title></head>
<body>
<div role="heading" aria-level="2">Settings</div>
<span role="heading">No level</span>
<x-box></x-box>
<p id="out"></p>
<script>
customElements.define("x-box", class extends HTMLElement {
  connectedCallback() {
    const root = this.attachShadow({ mode: "open" });
    root.innerHTML = '<button>Inside</button>';
    root.querySelector("button").onclick = () => { document.getElementById("out").textContent = "shadow clicked"; };
  }
});
</script>
</body></html>
```
