# Antigravity 接入

核实基线：2026-11-18，本机 agy 1.5.11、内置 agy-customizations 文档、真实会话落盘记录与 PTY 探针。内部标识用 `antigravity`，可执行文件用 `agy`，界面展示 Antigravity。

## 启动与状态

- `agy -i <prompt>` 是交互初始提示，`-p` 是非交互单轮；位置参数属于子命令。恢复用 `--conversation <UUID>`，`--model`、`--effort` 原样透传。默认跳过审批用 `--dangerously-skip-permissions`；逐项确认经 `--settings` 设置 `toolPermission: strict`，覆盖 Auto 的自动批准。手动传入的权限选项、设置文件和内联权限设置由 agy 处理。普通内联设置与 Posse 的权限档位合并，重复 `--settings` 时以最后一个为准。 [官方权限说明](https://antigravity.google/docs/agent-settings#antigravity-cli)
- PTY 探针收到 `OSC 7501;?` 后应答，实测报告 `app=antigravity`、working、blocked（包括 auth）、idle、done。状态经现有 OSC 通道处理，子 agent 是否仍工作由 agy 的聚合报告决定。
- 1.5.15 实测 `--add-dir` 会从附加目录加载工作区 hooks。Posse 把自己的 `.agents/hooks.json` 放在 `$POSSE_HOME/.posse-antigravity-hooks`，交互启动和恢复时加入这个目录，用户和项目的 hooks 照常加载。任务提醒仍在空闲时输入，手机答题与审批仍在终端处理。
- `--conversation` 指向不存在的 UUID 会另建不同 ID 的会话，不能用于预分配 ID。agy 运行期间一直持有 `presence/<UUID>.lock`（也开着 `conversations/<UUID>.db`），常驻进程每 2 秒的 agent 检查读 agy 进程打开的文件（`procmem.OpenFiles`，`proc_pidfdinfo`）认出当前会话并绑定，`/new`、切换会话后跟着换；同时持有多个锁时不绑。恢复参数和 agent 执行 `posse session title` 带来的 `ANTIGRAVITY_CONVERSATION_ID` 也能绑定。不按最近修改时间或 cwd 猜归属。
- 官方 `statusLine` 通过会话 `--settings` 接入，已有用户命令仍接收原始输入并输出原状态栏，只有 Posse 采集时保留原生状态栏。设置文件、禁用的自定义状态栏和无法解析的配置走进程锁和原生记录。报告只用于当前前台会话，旧报告和会话 ID 不符的报告丢弃。 [官方状态栏接口](https://antigravity.google/docs/cli/statusline/)
- 手机对话写入、读取流式草稿时重查前台 agy 的会话锁。缺锁或有多个会话锁时拒绝写入并撤下草稿，界面暂时保留上次识别到的会话。正式正文写入日志后替换草稿。
- agy 不写终端标题（OSC 0/2），标签和侧栏的会话名来自 agy 自己起的标题：首轮回复后约 3 秒写进 `conversation_summaries.db` 的 `title`（`cache/conversation_metadata.json` 更晚才有），状态栏未上报时绑定后只读查询，记成会话标题（`daemon/antigravity.go`）；已经显示标题后，原生标题变动时同步到 Posse；摘要仍是旧标题时保留 `posse session title` 的结果。`posse session title` 使用 agy 内置 Conversations API 的 `UpdateConversation`，按工具命令环境中的 `ANTIGRAVITY_CONVERSATION_ID` 写 `annotations.title`，确认回包的会话 ID 和标题后更新 Posse 标签。地址和令牌来自 `ANTIGRAVITY_LS_ADDRESS` 和 `ANTIGRAVITY_CSRF_TOKEN`，只请求本机回环地址；缺少这些环境变量时，命令提示在 agy 输入 `/rename <标题>`。agy 的 `-p` 模式拒绝执行 `/rename`。新终端环境隔离剥离 `ANTIGRAVITY_*` 和 `AGY_CLI_*`。

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

truncated_fields 触发按 step_index、type、created_at 核验的完整日志回读。上下文优先用状态栏上报的窗口大小、已用百分比和当次输入；没有有效上报时从日志取 input_tokens + cache_read_tokens，显示已用 token。图片与视频来自 media[].uri 和会话目录的实际产物。移到废纸篓只移动 brain/<UUID>；保留原生数据库，原生恢复可能重新生成日志。

已观察的工具包括 read_file、run_command、edit_file、write_file、search_web、manage_task、swarm、call_mcp_tool，args 的字段值还包含一层 JSON（2026-11-20 用临时 stdio MCP 服务实测）。技能统计识别 SKILL.md 读取，命令与网页搜索复用现有统计器。MCP 调用是 `call_mcp_tool`，参数 `ServerName`、`ToolName`、`Arguments`（不是 `mcp__服务__工具`）；调用前 agy 先 `read_file` 读 `~/.gemini/antigravity-cli/mcp/<服务>/<工具>.json` 查工具说明（界面上的 Explored），不算调用。工具结果是紧跟调用的 GENERIC 记录，一次多个调用时按顺序各占一步、没有调用 id，按步号对应；出错时 status 为 ERROR，这次 MCP 调用不计次数、时间线标失败。CLI 没有自带的网页抓取工具（让它读网址时用 read_file 报错，再改用 search_web），网址只来自 search_web、命令和 MCP 参数。

## 技能与 MCP

技能扫描覆盖标准项目目录（含别名）、仓库 `.gemini/config.json` 的 `personal_customization_dir`、全局与自带目录。个人目录中的相对清单路径从仓库根解析，技能优先于项目同名项。本机命令速查和项目技能页签会按当前 agy 进程的启动目录和 `--add-dir` 补扫；远端命令速查由目标常驻进程扫描。会话运行中输入 `/add-dir` 新增的目录以原生 `/skills` 为准。

`skills.json` 支持 entries/inherits、直接技能路径、嵌套 include_only 和精确 exclude，继承设深度与循环保护。发现采用[官方规则](https://antigravity.google/docs/customization-discovery/)的精确名称或相对路径匹配。CLI 专属两个技能目录优先且不受 exclude 影响。标准全局或 `.agents/skills` 下的技能可按目录名改对应 `skills.json` 的 exclude，保存前显示差异。由其他清单排除的、CLI 专属、插件和自带技能保持只读。

全局、项目和 `plugins.json` 注册的插件按插件目录 ID 显示 `<插件>:<技能>` 和 `<插件>_<MCP 服务>`。插件技能只列真实子目录，与 agy 1.5.14 的插件列表一致。`config.json` 的显式开关优先于插件清单的默认状态。MCP 详情展示配置推算的启用状态，可手动对本地 stdio 或远程 Streamable HTTP 服务发起一次 `initialize` 握手。测试前重读有效配置和插件开关，按 stdio 的 `cwd` 启动。结果描述该次连接，OAuth 登录和当前 agy 会话的状态以 `/mcp` 为准。手机的 Antigravity 终端「更多」可复制 `/skills` 和 `/mcp`。

安装 Posse 自带技能时，在 `~/.gemini/config/skills` 建立相对链接至 `~/.agents/skills`；用户已有同名对象保留。补链接由安装终端执行，关闭窗口后仍继续，失败以命令退出码呈现。内部技能内容版本递增，已有安装可按更新流程补齐链接。

## 任务交接

Posse 里的新会话（包括任务 worker）在 `SessionStart` 首轮读取当前目录及子目录最近三条已索引会话的标题、最后一句和查看命令，通过 agy 的 `injectSteps` 注入同一轮。当前会话及恢复启动不重复带交接。首轮仅在 agy 已挂载主工作区时带交接。历史索引略过专用 hooks 目录。如果首次启动时原生摘要只写了该目录，交接和恢复用 Posse 从 agy 进程确认过的工作目录。也可主动运行 `posse history brief --cwd <目录>`。

写文件工具和 shell 命令完成后，`PostToolUse` 把成功的写入交给 Posse 痕迹记录。Posse 只收前台 agy 唯一会话锁对应的事件，按既有规则记录工作区外仍存在的路径。

## 套餐与验收

桌面和手机的套餐用量来自 `agy -p /usage --output-format json`。在 agy 1.5.14 验证返回各池的剩余比例及重置时间，查询不产生模型轮次。状态栏也会按额度 ID 更新已确认的额度池，保留 `/usage` 给出的名称和窗口长度。自动及手动刷新仍可获取完整额度列表。会话上下文 token 与套餐额度独立。额外 AI 积分余额来自 `agy -p /credits --output-format json`，只采用原生零模型轮次返回的读数。该输出未提供逐笔消费记录，桌面与手机可从详情打开 Google One 积分活动页查看。

回归覆盖启动/恢复/手动转发与权限、OSC 状态、环境隔离、历史粗筛等价性、增量聊天与截断回读、子会话过滤、媒体 URI/产物扫描、废纸篓路径边界、技能继承/优先级/链接与 MCP 配置。桌面/手机共用的 agent 列表、图标、文案与上下文展示一并接入。CLI 参数参照 [官方 CLI reference](https://antigravity.google/docs/cli/reference)。

验收结果：Go 全量测试、go vet、MyGo vet/i18n 检查通过；Flutter analyze、全量测试（563 通过，7 跳过）及新增上下文/设计规则测试通过。离屏检查浅色/深色的新建菜单、技能页和历史 token 展示。Posse Dev（`~/.posse-dev`）实测 agy 启动、信任提示的 waiting、结束 idle、会话标题绑定 UUID、`/exit` 清除 agent 身份，以及 history resume 回到同一 UUID。新建的临时目录会话在原生缓存与 SQLite 都可能缺少 workspace URI。历史恢复优先用原生目录，缺少时用 Posse 已确认的工作区目录。无可用目录时可选工作区，CLI 可用 `--workspace`。
