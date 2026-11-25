# 飞书多机器人网关

Posse 常驻进程原生托管多个本机 `lark-cli` profile 长连接（无需公网服务器或 Webhook），把飞书单聊与同事工作群映射为 Posse 讨论，实现**飞书、桌面 Posse、手机 Posse 三端互通**。

## 架构与存储

- **多机器人配置**：存放在 `$POSSE_HOME/lark-bots.json`（`internal/lark/config.go`），可同时启用多个飞书自建应用 profile，每个机器人独立配置默认工作区、默认 Agent、权限审批模式（`ask` / `skip`）、群聊会话粒度（`chat` 按群共享 / `thread` 按话题隔离）、触发白名单（`self` / `allowlist` / `group`）、群聊近期上下文条数（`groupContextCount`，默认关闭，可选 10/20/30 或 CLI 指定 0~50）与角色说明。
- **引用展开与群上下文**：回复飞书里的某条消息再 `@机器人` 时，网关自动通过 `im +messages-mget --download-resources` 拉取被引用消息的正文与图片/文件附件一并传给 Agent（即使回复里只打了 `@机器人` 也生效）；配置了 `groupContextCount > 0` 时，群聊 `@` 还会自动附带该群最近 N 条未撤回消息作为背景上下文（自动排除当前消息与已展开的父消息）。
- **本机私有表**（`internal/store/lark.go`，不进入局域网协作同步）：
  - `lark_bindings`：`(bot_id, chat_id, thread_key) → discussion_key` 与当前活跃成员、工作区、Agent、权限模式；
  - `lark_cards`：执行、提问、权限审批与飞书卡片消息 ID 的映射；
  - `lark_events`：按 `message_id` / `event_id` 持久化事件去重（自动清理 7 天前记录）。
- **长连接托管**（`internal/lark/cli.go`）：每个已启用机器人由常驻进程并发启动两个 `lark-cli --profile <p> event consume <key> --as bot` 子进程（分别消费 `im.message.receive_v1` 与 `card.action.trigger`），子进程自动拼接 `lark-cli` 所在目录与常见 Homebrew 路径进 `PATH`（避免 `.app` 环境找不到 `node`），等 `[event] ready` 握手，断线指数退避重连，退出时关闭 stdin 优雅退出（严禁 `kill -9`）。群聊按机器人自身的 `openId` 与 `appName` 精确识别 `@` 指向，开通全群消息权限时不会误抢 `@ 其他同事` 的发言。

## 卡片交互与权限审批

- **进度与结果卡（Card 2.0）**：收到 `@机器人` 后立即回复进度卡片（带耗时、实时工具/状态行与 `[停止]` 按钮），每 2.5 秒节流刷新；完成后先把原进度卡收拢为一行灰色完结回执（移除 `[停止]`），再将完整 Markdown 结论作为一条新卡片发送到对话底部（引用回复原提问消息并触发飞书通知；超长自动截断并附 `.md` 文件附件，失败/停止带 `[重试]`）。
- **工具权限审批（优先 Antigravity `agy` + 兼容 Claude Code）**：
  - `internal/daemon/permission.go` 的 `permissionHub` 同时协调飞书与手机审批；
  - 飞书 `permissionMode == "ask"` 的 worker 启动时自动带 `POSSE_LARK_APPROVAL=1`（不改全局设置）。`agy` 挂载独立的飞书专用 hook 目录 `$POSSE_HOME/.posse-antigravity-lark-hooks`（与普通终端的 `.posse-antigravity-hooks` 隔离，普通终端不挂 `PreToolUse`），进程侧跳过内置二次确认弹窗由 `PreToolUse` 独占把关；CLI hook 与常驻进程共用 `internal/agent/readonly.go` 的安全只读判定：
    - **自动放行**：仅将「空白 + 反斜杠 + LF」续行规整为空格（拒绝紧贴字符续行、CRLF、裸换行、未加引号 `#` 注释、非 `$POSSE_NOTES`/`$HOME` 变量展开、非开头 `~`、裸赋值段与非白名单目录可执行文件路径），全部由安全段组成的命令——含本地只读排查命令（`ls`/`grep`/`rg`（禁 `--pre`/`--pre-glob`/`-z`）/`jq`/`git` 受限只读子命令（禁 `-c`、`diff/log/show` 禁 `--output*`/`--ext-diff`/`--textconv`、`branch` 禁无 `--list` 位置参数）/`sed -n '<地址|模式>p'`/`find`（禁 `-exec*`/`-ok*`/`-delete`/`-fprint*`/`-fls`）/`sort`（禁 `-o`/`--compress-program`/`-T`）/`du`/`df`/`tree`（禁 `-o`）/`xxd`（仅 `-l`/`-s`/`-c`/`-g`/`-p` 且至多 1 位置参数）/`od`/`strings`/`shasum`/`md5`/`column`/`plutil -p|-lint`/`unzip -l|-Z [-1]`/`tar -t[vzf]`（禁含 `:` 的归档参数）等，`sqlite3` 与 `awk` 不在自动放行名单）、`lark-cli` 真实帮助（`-h`/`--help`）、`schema`、`--dry-run`、显式白名单及按动词判定的 `+xxx` 只读子命令（会写本地的 `base +record-download-attachment --output`、`drive +export --output-dir` 及其他 `download`/`export` 命令必须指向 `/tmp/...` 或 `$POSSE_NOTES/discussions/...`，`im` 带 `--download-resources` 固定落盘到 `./lark-im-resources/` 一律审批）、Posse 讨论协作与只读查询命令（含 `history show|search|list|brief`、`discussion list|show`、`task list|show|inbox|guide`、`terminal list`、`workspace list`、`project list`、`lark list|profiles|status`、`version` 及 `--` 前任意 `-h`/`--help`）、以及经 Clean 与最深已存在祖先 `EvalSymlinks` 校验（拒软链接目标与 `..` 路径段）落在 `/tmp` 或 `$POSSE_NOTES/discussions/` 内的目录创建/草稿写入/`tee [-a]` 及安全重定向（`>`/`>>`/`1>`/`2>`/`&>` 至上述目录或 `/dev/null`、`2>&1`、`<` 输入重定向）；
    - **人工审批与同类规则键**：其余写文件/改代码/MCP/非只读命令（含 `$()`、反引号、后台 `&`、越界重定向、`lark-cli` 写操作与 `rm`）挂起等待飞书审批；卡片提供 `[允许]`、`[本会话允许同类]`、`[拒绝]` 三个按钮，`deriveCommandRuleKeys` 跳过已证明安全的只读段并仅记录需审批的段（`posse` 与 `lark-cli` 均记两级），若需审批段含任何重定向、前置环境变量、无法可靠解析或携带越界输出目标参数，则整条命令不生成同类规则键（仅单次允许）；点「本会话允许同类」后当前工位内后续同类操作自动放行（不因桌面窗口聚焦/滚轮误取消）；3 分钟超时显式返回 `deny`（绝不自动放行）；Claude 通过 `PermissionRequest` 挂起最多 3 分钟，超时或桌面键入交回终端 TUI。
- **选项问答**：worker 调用 `posse task ask --choice` 时自动发选项按钮 + 自定义输入表单卡片，点击或提交直接调用 `answerDiscussion` 并同步桌面/手机。
- **同事工作群安全边界**：
  - `[允许]` / `[拒绝]` 权限审批、`[停止]`、`[重试]` 与切换 Agent/工作区按钮强制校验 `operator_id == ownerOpenId`；
  - 反向同步规则：**群聊只将 Agent 的最终回复同步回飞书群，不把桌面/手机发给 Agent 的内部追问刷到同事群**；**单聊（`p2p`）双向全同步**。

## 飞书快捷命令与 CLI

- 飞书内支持 `/agent [name]`、`/ws [name]`、`/new`、`/status`、`/stop`、`/help`。
- CLI 管理：`posse lark profiles`、`posse lark list`、`posse lark add --profile <p> ...`、`posse lark enable/disable <id>`、`posse lark rm <id>`。
- 桌面管理：「设置 › 飞书机器人」。
