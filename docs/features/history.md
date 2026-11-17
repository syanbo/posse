# 会话历史

`posse history` 的索引和查询，以及界面的历史页。

- 会话历史（`posse history`）：
  - 只读各 agent 的会话目录，不写回（例外：会话标题，见 `agents.md` 的 `posse session title`；用户在历史页点"移到废纸篓"时，窗口确认后由目标常驻进程把会话文件或目录移到 macOS 废纸篓，只认目标用户各家会话目录里、路径和会话 ID 对得上的，见 `internal/daemon/history_trash.go`、`internal/history/trash.go` 和无界面适配 `internal/trash`）：claude `~/.claude/projects/*/*.jsonl`（不含 subagents）、codex `~/.codex/sessions` 和 `archived_sessions`（标题取 `session_index.jsonl`）、cursor `~/.cursor/projects/*/agent-transcripts/*/*.jsonl`（标题、目录、时间取 `~/.cursor/chats/*/<会话>/meta.json`）、grok `~/.grok/sessions/*/*/chat_history.jsonl`（元数据取同目录 `summary.json`）。
  - 不入库的内容：agent 以用户身份注入的提醒、技能正文、AGENTS.md/环境信息、本地命令输出、压缩摘要、思考过程；不是用户发起的会话（claude/cursor 子 agent，codex 的子 agent 和 `thread_source` 不是 user 的自动审批、定时任务等线程，grok 内部的目标规划）。工具调用和输出各截到 3072 个字符。新格式（`history_mode=paginated`）的 codex 会话只读 `event_msg` 的 `item_completed`。用户在 Claude 工作时插进来的话不是 `type: user`，而是 `type: attachment` 的 `queued_command`（`origin.kind` 为 human、`commandMode` 为 prompt），历史和手机会话页都按用户消息收；同样是 queued_command 的后台任务通知、子 agent 回报不算。
  - 索引更新在子进程 `posse history index --progress` 里做（首次较慢，常驻进程内存不受影响），按文件的 size/mtime 增量读新增的完整行；offset 前的字节对不上就整份重读，文件消失就删除。没有后台轮询：查询时索引超过 15 秒没更新才更新一次，最多等 3 秒，没做完先用已有结果。历史页开着时每 15 秒刷新一次。桌面列表、搜索和详情允许查看身份调用：索引是目标端维护的派生数据，查询可触发增量更新，但不改写会话文件、不恢复终端、不取得控制权；主动强制索引不作为桌面查看接口开放。窗口在安排防抖和定时刷新时固定主机请求，成功和失败都在主线程应用前重新核对主机身份与查询序号；旧结果不能盖掉新搜索，重复打开同一会话也只采用最后一次详情请求。
  - 中文按子串匹配；英文末尾按前缀匹配；各词 AND，全都没有时退回 OR 并告诉调用方。排序：标题命中优先，再按 bm25（用户 4、助手 2、工具 1）和命中消息数。
  - grok 的 `chat_history.jsonl` 不带时间：用户消息按 `prompt_index`、回复按开头文字、工具按调用 ID，对上同目录 `updates.jsonl` 里带时间戳的记录；对不上的沿用前一条的时间。
  - 解析前先按各家 parser 的 `wants` 粗筛（只认 `"type"` 等字段的值，冒号后有空格也能匹配），无关的行不做 JSON 解码。
  - Codex 新格式的会话会分成几个文件（会话 ID 相同），列表、搜索、`history show` 都合成一个，信息取最新一段、消息数相加、无标题时用第一段的提问，详情按段接起来、序号顺延，移到废纸篓时各段一起走。别家同一个 ID 的不同文件是不同会话，按会话文件区分。
  - 历史列表默认按今天、昨天、最近 7 天、最近 30 天、1 个月前分组；顶栏的分段按钮可以换成按文件夹分组：会话归到目录所在的项目（项目和它的工作区、worktree 目录取最长匹配），不在任何项目里的用目录名，组按其中最近的会话排先后，选择记在 ui.json 的 `historyByFolder`。默认只展开第一组，点组标题展开收起（不记进 ui.json）；↑/↓ 在看得见的会话之间换选中，会话列表卡片可聚焦，点过之后方向键不被侧栏拿走。
  - `history show` 的 `--tail`、`--tools` 对 `--json` 输出同样生效。
  - `history resume` 在会话目录所在的工作区（或 `--workspace` 指定的）里开终端，先 `cd` 回原目录（claude 按目录找会话）；同一会话已在 posse 终端里运行时直接返回那个终端，原目录已经不在时报错，设置里关掉的 agent 不恢复。没有对应工作区时报 not_found，界面会问要不要把目录加成项目。桌面恢复同时传会话文件路径，避免重复 ID 选到其他文件，并核对路径对应的会话 ID。恢复从点击时固定主机与控制身份，添加项目确认、添加后的再次恢复和打开终端都沿用它；切主机或控制身份变化后，旧确认和迟到结果失效，查看状态不启动恢复。
- 历史页列表顶上是总计（会话数、消息数、各 agent 会话数，取自 `history.list` 带回的 `status`）。本机详情里的路径点一下在访达中打开，远端路径只展示，不交给本机访达；移到废纸篓通过 `history.trash` 在目标执行，服务端接受时检查桌面控制身份；确认后先刷新索引取得当前分段，移动前直接读取源文件核对会话身份；若会话已恢复运行则拒绝。运行状态检查和移动与 Posse 的历史/终端恢复串行，索引等待在锁外。删除后等旧索引轮次结束，再刷新索引；多文件会话部分移动失败也刷新并报告错误，不永久删除、不自动重做。"上下文 N%"是最后一次请求占的上下文，详情请求带 `context: true` 时由目标 daemon 从会话文件末尾往前读并返回（不需要用量的调用不读）；旧本机 daemon 缺字段时由窗口兼容读取，远端缺字段不读取本机文件：Codex 取 `token_count` 的 `last_token_usage` 和 `model_context_window`；Claude 取最后一条助手消息的 usage（输入和缓存相加，不含输出），文件里没有窗口大小，按模型推断（兼容提供商前缀和小数点版本号；Claude 5 起的 Opus、Sonnet 和带 `[1m]` 的是 100 万，其余已知系列 20 万，用量超过 20 万的算 100 万；认不出的模型不显示百分比）；Grok 只有每轮累计、Cursor 没有记录，不显示。

历史详情里的助手 Markdown 图片会按会话根目录加载：Cwd 是绝对路径时用 Cwd，否则用会话文件父目录，相对路径和 `/images/x.png` 都按该根解释；工具消息仍是纯文本。远端图片经详情请求捕获的目标资源读取，不读本机同路径，换会话、详情请求换代或换主机时关闭旧加载器并丢弃迟到结果。
