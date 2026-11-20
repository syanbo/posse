# Antigravity 接入

核实基线：2026-11-18，本机 agy 1.5.11、内置 agy-customizations 文档、真实会话落盘记录与 PTY 探针。内部标识用 `antigravity`，可执行文件用 `agy`，界面展示 Antigravity。

## 启动与状态

- `agy -i <prompt>` 是交互初始提示，`-p` 是非交互单轮；位置参数属于子命令。恢复用 `--conversation <UUID>`，`--model`、`--effort` 原样透传。默认跳过审批用 `--dangerously-skip-permissions`；用户显式给权限参数时不补。
- PTY 探针收到 `OSC 7501;?` 后应答，实测报告 `app=antigravity`、working、blocked（包括 auth）、idle、done。状态经现有 OSC 通道处理，子 agent 是否仍工作由 agy 的聚合报告决定。
- `--settings` 拒绝 hooks、plugins、pluginPaths、pluginsPaths、hooksPath、defaultHooksPath、customizations，报 unknown setting。没有已核实的会话级挂载途径，因此不注入 hooks，不改用户 hooks.json；任务提醒空闲时输入，无 hooks 答题/审批卡和写文件痕迹。
- `--conversation` 指向不存在的 UUID 会另建不同 ID 的会话，不能用于预分配 ID。agy 运行期间一直持有 `presence/<UUID>.lock`（也开着 `conversations/<UUID>.db`），常驻进程每 2 秒的 agent 检查读 agy 进程打开的文件（`procmem.OpenFiles`，`proc_pidfdinfo`）认出当前会话并绑定，`/new`、切换会话后跟着换；同时持有多个锁时不绑。恢复参数和 agent 执行 `posse session title` 带来的 `ANTIGRAVITY_CONVERSATION_ID` 也能绑定。不按最近修改时间或 cwd 猜归属。
- agy 不写终端标题（OSC 0/2），标签和侧栏的会话名来自 agy 自己起的标题：首轮回复后约 3 秒写进 `conversation_summaries.db` 的 `title`（`cache/conversation_metadata.json` 更晚才有），绑定后只读查询，记成会话标题（`daemon/antigravity.go`）；已有 `posse session title` 起的标题时不覆盖。`posse session title` 仅改 Posse 展示；未找到可安全独立写回原生标题的稳定接口。环境隔离剥离 `ANTIGRAVITY_*` 和 `AGY_CLI_*`。

## 会话数据

```text
~/.gemini/antigravity-cli/
├── brain/<UUID>/
│   ├── .system_generated/logs/transcript.jsonl
│   ├── .system_generated/logs/transcript_full.jsonl
│   ├── .user_uploaded/
│   └── 生成产物
├── cache/conversation_metadata.json
├── conversation_summaries.db
└── conversations/<UUID>.db
```

日志提供 USER_INPUT、PLANNER_RESPONSE、GENERIC。摘要缓存的 summary 提供 ID、Title、WorkspaceURIs、UpdatedAt、Internal；is_internal 也用于过滤。SQLite 中 conversation_summaries 的 parent_conversation_id / nesting_depth、conversation_parents 的 parent_id 补充子会话过滤，查询失败回退缓存标记。conversations/*.db 含原生 protobuf，不改写。

truncated_fields 触发按 step_index、type、created_at 核验的完整日志回读。上下文仅能核实 input_tokens + cache_read_tokens，不猜模型窗口容量。图片与视频来自 media[].uri 和会话目录的实际产物。移到废纸篓只移动 brain/<UUID>；保留原生数据库，原生恢复可能重新生成日志。

已观察的工具包括 read_file、run_command、edit_file、write_file、search_web、manage_task、swarm、call_mcp_tool，args 的字段值还包含一层 JSON（2026-11-20 用临时 stdio MCP 服务实测）。技能统计识别 SKILL.md 读取，命令与网页搜索复用现有统计器。MCP 调用是 `call_mcp_tool`，参数 `ServerName`、`ToolName`、`Arguments`（不是 `mcp__服务__工具`）；调用前 agy 先 `read_file` 读 `~/.gemini/antigravity-cli/mcp/<服务>/<工具>.json` 查工具说明（界面上的 Explored），不算调用。工具结果是紧跟调用的 GENERIC 记录，一次多个调用时按顺序各占一步、没有调用 id，按步号对应；出错时 status 为 ERROR，这次 MCP 调用不计次数、时间线标失败。CLI 没有自带的网页抓取工具（让它读网址时用 read_file 报错，再改用 search_web），网址只来自 search_web、命令和 MCP 参数。

## 技能与 MCP

实现标准项目目录（含别名）、用户目录、自带目录，以及 skills.json 的 additive entries/inherits、直接技能路径、嵌套 include_only、精确 exclude；继承设深度与循环保护。CLI 专属两个技能目录优先且不受 exclude 影响。MCP 按配置推算启用状态，未做在线连接探测。

本机内置文档与线上发现规则有差异（路径展开、排除匹配等），发现实现采用 [官方 customization discovery](https://antigravity.google/docs/customization-discovery/) 的精确名字/相对路径匹配、不展开 `~`。未实现个人仓库目录 `personal_customization_dir`、额外 `--add-dir` 与 Antigravity 插件携带的技能扫描，不能把技能页当作 agy `/skills` 的完整替代。

安装 Posse 自带技能时，在 `~/.gemini/config/skills` 建立相对链接至 `~/.agents/skills`；用户已有同名对象保留。补链接由安装终端执行，关闭窗口后仍继续，失败以命令退出码呈现。内部技能内容版本递增，已有安装可按更新流程补齐链接。

## 套餐与验收

未找到现成的本地套餐配额缓存，不接套餐用量卡片，不调用私有 LanguageServer 凭据接口；会话上下文 token 与套餐额度独立。

回归覆盖启动/恢复/手动转发与权限、OSC 状态、环境隔离、历史粗筛等价性、增量聊天与截断回读、子会话过滤、媒体 URI/产物扫描、废纸篓路径边界、技能继承/优先级/链接与 MCP 配置。桌面/手机共用的 agent 列表、图标、文案与上下文展示一并接入。CLI 参数参照 [官方 CLI reference](https://antigravity.google/docs/cli/reference)。

验收结果：Go 全量测试、go vet、MyGo vet/i18n 检查通过；Flutter analyze、全量测试（563 通过，7 跳过）及新增上下文/设计规则测试通过。离屏检查浅色/深色的新建菜单、技能页和历史 token 展示。Posse Dev（`~/.posse-dev`）实测 agy 启动、信任提示的 waiting、结束 idle、会话标题绑定 UUID、`/exit` 清除 agent 身份，以及 history resume 回到同一 UUID。新建的临时目录会话在原生缓存与 SQLite 都没有 workspace URI，恢复须显式指定工作区；不猜 cwd。
