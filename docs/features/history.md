# 会话历史

`hive history` 的索引和查询，以及界面的历史页。

- 会话历史（`hive history`）：
  - 只读各 agent 的会话目录，不写回（例外是会话标题，见 `agents.md` 的 `hive session title`；以及：用户在历史页点"移到废纸篓"，由窗口进程把会话文件或会话目录移到 macOS 废纸篓，只认各家会话目录里、路径和会话 ID 对得上的，见 `internal/app/trash.go`）：claude `~/.claude/projects/*/*.jsonl`（不含 subagents）、codex `~/.codex/sessions` 和 `archived_sessions`（标题取 `session_index.jsonl`）、cursor `~/.cursor/projects/*/agent-transcripts/*/*.jsonl`（标题、目录、时间取 `~/.cursor/chats/*/<会话>/meta.json`）、grok `~/.grok/sessions/*/*/chat_history.jsonl`（元数据取同目录 `summary.json`）。
  - 不入库的内容：agent 以用户身份注入的提醒、技能正文、AGENTS.md/环境信息、本地命令输出、压缩摘要、思考过程；不是用户发起的会话（claude/cursor 子 agent、codex 的 guardian/automation、grok 内部的目标规划）。工具调用和输出各截到 3072 个字符。新格式（`history_mode=paginated`）的 codex 会话只读 `event_msg` 的 `item_completed`。用户在 Claude 工作时插进来的话不是 `type: user`，而是 `type: attachment` 的 `queued_command`（`origin.kind` 为 human、`commandMode` 为 prompt），历史和手机会话页都按用户消息收；同样是 queued_command 的后台任务通知、子 agent 回报不算。
  - 索引更新在子进程 `hive history index --progress` 里做（首次约 6 秒、读几 GB，常驻进程内存不受影响），按文件的 size/mtime 增量读新增的完整行；offset 前的字节对不上就整份重读，文件消失就删除。没有后台轮询：查询时索引超过 15 秒没更新才更新一次，最多等 3 秒，没做完先用已有结果。历史页开着时每 15 秒刷新一次。
  - 中文逐字切开再交给 unicode61 分词（`splitCJK`），中文按子串匹配；英文末尾按前缀匹配；各词 AND，全都没有时退回 OR 并告诉调用方。排序：标题命中优先，再按 bm25（用户 4、助手 2、工具 1）和命中消息数。
  - 只改了某一家的解析规则时改 `parserVersions`（index.go）：只重读这一家的文件，会话 ID 不变，跨版本继续运行的旧常驻进程照常能查。改表结构或分词才改 `schemaVersion`：旧索引清空重建，旧常驻进程会报 ErrStale，要重启它（会断掉所有终端）。
  - grok 的 `chat_history.jsonl` 不带时间：用户消息按 `prompt_index`、回复按开头文字、工具按调用 ID，对上同目录 `updates.jsonl` 里带时间戳的记录；对不上的沿用前一条的时间。增量更新时从状态里记的位置（`GrokUpdates`，最后对上的回复分段的行尾）接着读 `updates.jsonl`。
  - 解析前先按各家 parser 的 `wants` 粗筛（只认 `"type"` 等字段的值，冒号后有空格也能匹配），无关的行不做 JSON 解码；改粗筛规则要跑 `TestLineFilterSameResult`。
  - 索引里一个文件一行，查询时按 `groupKey` 合成会话：Codex 新格式的会话会分成几个文件（后一段的 `session_meta` 带 `history_base`，会话 ID 相同），列表、搜索、`history show` 都合成一个，信息取最新一段、消息数相加、无标题时用第一段的提问，详情按段接起来、序号顺延，移到废纸篓时各段一起走（`Session.Parts`）。别家同一个 ID 的不同文件是不同会话（Cursor 出现过同一 ID 在两个项目里的两段无关对话），所以界面按会话文件认会话（`sessionKey`），`history.show` 带 `path` 时按文件找（`FindPath`）。索引只有一个连接，结果集没关之前不能再查。
  - 历史列表按今天、昨天、最近 7 天、最近 30 天、1 个月前分组，默认只展开第一组，点组标题展开收起（不记进 ui.json）；↑/↓ 在看得见的会话之间换选中（`stepSession`），会话列表卡片可聚焦，点过之后方向键不被侧栏拿走。
  - `history show` 的 `--tail`、`--tools` 由常驻进程在 SQL 里筛选，`--json` 输出同样生效。
  - `history resume` 在会话目录所在的工作区里开终端，先 `cd` 回原目录（claude 按目录找会话）；同一会话已在 hive 终端里运行时直接返回那个终端。没有对应工作区时报 not_found，界面会问要不要把目录加成项目。
- 历史页列表顶上是总计（会话数、消息数、各 agent 会话数，取自 `history.list` 带回的 `status`）。详情里的路径点一下在访达中打开；"上下文 N%"是最后一次请求占的上下文，由窗口进程从会话文件末尾往前读（`history.ReadContext`）：Codex 取 `token_count` 的 `last_token_usage` 和 `model_context_window`；Claude 取最后一条助手消息的 usage（输入、缓存、输出相加），文件里没有窗口大小，按模型推断（Claude 5 起的 Opus、Sonnet 和带 `[1m]` 的是 100 万，其余 20 万，用量超过 20 万的算 100 万）；Grok 只有每轮累计、Cursor 没有记录，不显示。
- 历史页顶栏的文件夹按钮（`historyDirFilter`）列出侧栏里的项目，选一个只看在它目录（含子目录）里开的会话，搜索也只在里面找；用的是 `history.list`、`history.search` 已有的 `cwd` 参数，不记进 ui.json。
