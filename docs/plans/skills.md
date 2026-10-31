# 技能管理

## 目标

在桌面端一处看清四家 agent（Claude、Codex、Grok、Cursor）各有哪些技能、谁真的用得上、用了多少，能按家开关，装、删、更新交给 `npx skills`。

## 各家怎么读技能、怎么关（2026-10-31 核实）

| agent | 读的目录 | 单个技能的开关 | 依据 |
|---|---|---|---|
| Claude 2.1.314 | `~/.claude/skills`、项目 `.claude/skills`、插件 | `settings.json` 的 `skillOverrides`：按名字设 `on`、`name-only`、`user-invocable-only`、`off`，不写等于 `on`；另有 `disableBundledSkills` | 程序里的设置说明 |
| Codex 0.166 | `~/.agents/skills`、`~/.codex/skills/.system`、插件缓存 | app-server 的 `skills/list`（带 `enabled`、`scope`、`path`、`pluginId`）、`skills/config/write {enabled, name 或 path}` | 会话里的技能清单、`codex app-server generate-json-schema` |
| Grok 1.0.73 | `~/.grok`、`~/.agents`、`~/.claude`、`~/.cursor` 的 skills 及各级项目目录，同名按优先级去重 | `~/.grok/config.toml` 的 `[skills] disabled`（列着但不生效）、`ignore`（藏起来）；`[compat.claude] skills=false` 整个不读 Claude 的目录 | `~/.grok/docs/user-guide/08-skills.md` |
| Cursor 2026.10.22 | 程序里出现 `.agents`、`.claude`、`.codex`、`.cursor` 的 skills 目录；`~/.cursor/skills-cursor` 是自带的 | 没找到 | 只见字符串，规则是推断 |

本机的技能由 `npx skills`（vercel-labs/skills，1.7.4）管：正本在 `~/.agents/skills`，锁文件 `~/.agents/.skill-lock.json`，`~/.claude/skills`、`~/.grok/skills` 里是它建的相对链接。

## 和开源方案的对比

xingkongliang/skills-manager、jiweiyeah/Skills-Manager、Shpigford/chops、runkids/skillshare 这类工具的"对某家关掉"都是删这家目录里的链接。Codex 直接读正本、Grok 读四家的目录，删链接关不掉，它们显示的"装给了谁"也不等于"谁用得上"。借鉴的：按真实路径去重、每家一个状态（chops），体检和收编（`skm doctor`、`adopt`），装之前看脚本（skillshare `audit`）。不做：自建中心目录、市场、云备份、预设组。

## 决策

- 窗口进程实现，常驻进程不参与，不改协议。纯逻辑在 `internal/skills`（扫描、解析 frontmatter、算各家状态、读写各家配置），界面在 `internal/app/skills.go`。
- 一个技能一行，按解开链接后的路径去重。每行：名字、描述、来源（用户、项目、插件、系统；`npx skills` 装的带锁文件里的来源）、各家状态（启用 / 关了 / 读不到）。各家状态按上表的读取规则和开关算，Codex 用 `skills/list` 的结果。
- 开关只写各家自己的配置：Claude 写 `skillOverrides`，Codex 调 `skills/config/write`，Grok 写 `[skills] disabled`；Cursor 只显示，不给开关。读改写保留原有内容和格式，原子保存，写之前比对磁盘戳，变了重读。
- 改全局配置前一律弹框确认：写明要改的文件和改动（diff），确认后才写；装、删、更新弹框写明要跑的完整命令。
- 装、删、更新交给 `npx -y skills@<固定版本> … -g`，带 `DISABLE_TELEMETRY=1`，在当前工作区新开的 shell 标签里跑（窗口进程的 PATH 里没有 nvm 的 node，交互提示和报错也看得见），标签里的程序退出后重新扫描。
- 两个入口，各管一层：
  - 侧栏顶上和任务、历史、手机并列的「技能」页管全局：用户级、插件、系统技能，体检，装、删、更新。左边列表（搜索、按 agent 和来源筛），右边详情（frontmatter、正文预览、各家状态、在访达中显示、在文件标签里编辑）。开关写各家的全局配置（如 `~/.claude/settings.json`）。
  - 右侧文件栏加「技能」页签（和历史、文件并列；ui.json 的 `filesPanelHistory` 换成 `filesPanelTab`，旧的 true 读成"历史"）只管当前工作区的项目级技能（`.claude/skills`、`.agents/skills`、`.grok/skills`、`.cursor/skills`，Grok 从工作区往上到仓库根的各级都算）：一个技能一行，名字、描述，行尾四家状态，点了切换；点名字在文件标签里打开 SKILL.md。开关写项目里的配置（如 `.claude/settings.local.json`），新建项目技能也在这里。worktree 按自己的检出算。
  - 「技能与工具」面板里的技能名：项目级的跳到文件栏的技能页签，其余跳到侧栏的技能页。

## 体检

- 断掉的链接、同名冲突（哪一份生效）。
- 多余的链接：这家本来就读正本，还链了一份（如 `~/.grok/skills` 那批）。
- 锁文件和目录对不上：目录有、锁文件没有（`browser-skill`、`iphone-use`），锁文件有、目录没有（`computer-use`、`herdr`、`minim-cli`）。
- 没进正本的真目录（`~/.claude/skills` 里的 `browser-skill` 等）。
- 处理都经确认弹框，删除移到废纸篓。

## 阶段

1. 只读总览和体检：扫描、去重、各家状态、详情页。先出离屏预览（深浅两套）再接真数据。
2. 开关（已做）：Claude、Codex、Grok，确认弹框。2026-10-31 核实：Codex 0.166 在临时 CODEX_HOME 里按名字和按路径（SKILL.md 的真实路径）写的 `[[skills.config]]` 都被 `skills/list` 认成 enabled = false；Grok 1.0.73 在临时 HOME 里 `grok inspect` 把 `[skills] disabled` 的标成 [disabled]，`ignore` 的项目技能不再列出（项目要先信任）。Claude 的 skillOverrides 只照程序里的设置说明，没用真实会话核实。用临时 HOME 和真实 CLI 核实写法生效，不碰日常配置。项目级的开关各家能不能写在项目里还没核实（Claude 有 `.claude/settings.local.json`；Codex 的 `skills/config/write`、Grok 的 `[skills]` 可能只有全局一份），只能写全局的那家在项目页签里置灰并说明。
3. 装、删、更新：界面收参数，拼 `npx skills` 命令到 shell 标签里跑。
4. 跨会话用量：历史索引里记技能使用，给每个技能的次数和最后使用时间（改 `parserVersions`，各家会话重读一遍）。
