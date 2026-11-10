# 更新内容

每个版本一节，标题是 `## <版本>`：发布时这一节写进更新清单，Posse 在设置「关于」里显示它。
一节里先写中文条目，再在 `### English` 小节里写对应的英文条目：界面是中文时只显示中文条目，其余语言只显示英文小节（没写英文小节的旧版本整节照旧显示）。

## 0.1.33

- 终端里图片的悬浮预览按窗口大小放大（最宽是终端宽度的一半），竖的手机截图、长截图整张显示在终端里，不再超出窗口看不到下半截；点击图片链接弹出的卡片里，太高的图会缩小，下面的按钮不再被挤出窗口。
- Claude 输入框里粘贴了、还没发送的图片（`[Image #N]`）也能悬停预览、点击打开。
- 修复：链接卡片开着时按 Esc 关不掉，Esc 还会发给终端里的 Claude、Codex，打断它们这一轮。
- 「技能与工具」面板能认出 Claude 在后台运行的内置技能（如 `/code-review`）。

### English

- Image previews on hover in the terminal now scale with the window (up to half the terminal's width), and tall phone screenshots and long screenshots show whole inside the terminal instead of running off the window. In the card that opens when you click an image link, a tall image is scaled down so the buttons below it stay on screen.
- Images pasted into Claude's prompt but not sent yet (`[Image #N]`) can also be previewed on hover and opened with a click.
- Fix: Esc did not close an open link card, and it went on to the Claude or Codex in the terminal, interrupting its turn.
- The Skills & Tools panel now recognizes Claude's built-in skills that run in the background (such as `/code-review`).

## 0.1.32

- 修复：Codex 还在运行时，终端偶尔被认作已退出，图标变回普通终端、侧栏里不见了（Codex 结束旧会话时也会发出退出通知）。现在收到通知后先确认 Codex 进程是否真的退出了。

### English

- Fix: a terminal running Codex was sometimes taken as exited, its icon turned back into a plain terminal and it vanished from the sidebar (Codex also sends an end notice when it closes an old session). Posse now checks that the Codex process really exited first.

## 0.1.31

- 终端里的 Agent 被认作已退出时，`~/.posse/daemon.log` 会记下原因，方便排查「Agent 还在跑，图标却变回普通终端」这类问题。

### English

- When an agent in a terminal is taken as exited, `~/.posse/daemon.log` records why, to help track down cases where the agent is still running but its icon turns back into a plain terminal.

## 0.1.30

- 侧栏在工作区下面也列出运行中的普通终端，点一下切过去；已退出的终端和还没恢复的普通终端仍只在标签栏。⌘⇧] / ⌘⇧[ 照侧栏的顺序切换。
- 讨论：Agent 提问时可以给出选项，点一下就回答；成员可以把一部分工作 @ 给另一个成员（最多转 3 层、一条请求最多 8 轮）；可以让成员写一份讨论摘要，之后加入的成员从摘要读起，右栏显示摘要；连着发给同一成员的几条请求并成一轮回答；在局域网分享时可以邀请只读访客，只能看、不能发言。新功能要房主和成员都更新到这个版本。
- 修复：Codex 用子 Agent，或 Agent 跑的命令里又启动了 Agent 时，终端被误认成已退出，图标变回普通终端、侧栏里不见了。

### English

- The sidebar now also lists running plain terminals under their workspace, and clicking one switches to it. Exited terminals and plain terminals not yet restored stay in the tab bar only. ⌘⇧] / ⌘⇧[ follow the sidebar order.
- Discussions: agents can offer choices with a question, and clicking one answers it; members can hand part of their work to another member with @ (up to 3 hops and 8 rounds per request); a member can write a discussion summary that later members read first, shown in the side panel; several requests sent in a row to the same member are answered in one round; when sharing on the local network you can invite read-only guests who can read but not post. The new features need this version on both the host and the members.
- Fix: when Codex used subagents, or a command an agent ran started another agent, the terminal was taken as exited, its icon turned back into a plain terminal and it vanished from the sidebar.

## 0.1.29

- 个人资料：在设置里填名字、选头像，讨论里显示你的名字和头像；也可以在讨论人员表里右键自己那行或用 `posse profile set` 修改。加入别人的讨论时，资料会同步给房主（双方都需要更新到这个版本）。
- 讨论里的图片加了细边框，白底、黑底的截图也看得出边界；点开在窗口里看大图，大图下方和右键菜单都能复制、存储为、在访达中显示、用预览打开。
- 分支按钮和 Git 日志开关移到底部状态栏左端，长分支名不再挤占标签栏；在设置「外观 → 界面」里可以改回标签栏。
- Git 日志的提交图照 WebStorm 重画：跨很多行的长线只在两头各画一小段，断开处是箭头，悬停提示要跳到的提交，点击直接选中它（还没读到会接着往下读）；一次合并几十个分支时提交图不再被撑宽，合并提交的标题变淡。
- 修复：Codex 讨论成员按回答结束派下一轮，后台命令不再卡住队列；开着详情切换讨论时窗口崩溃；头像改为从图片中间裁成正方形；窄窗口下设置页不被遮住，讨论页会收起列表。

### English

- Profile: set your name and avatar in Settings, and discussions show them. You can also change them by right-clicking your own row in a discussion's member list or with `posse profile set`. In discussions you joined, your profile syncs to the host (both sides need this version).
- Images in discussions get a thin border, so white or black screenshots stand out. Clicking one opens it large in the window; the buttons below it and the context menu can copy, save as, reveal in Finder or open in Preview.
- The branch button and the Git log toggle move to the left end of the status bar, so long branch names no longer crowd the tab bar. Settings > Appearance > Interface can put them back in the tab bar.
- The Git log commit graph is redrawn the WebStorm way: long lines show only a short piece at each end with arrows where they are cut, hovering an arrow names the commit it leads to, and clicking selects that commit (loading more history if needed). Merging dozens of branches at once no longer widens the graph, and merge commit titles are dimmed.
- Fixes: Codex members in discussions start their next request when the answer finishes, so background commands no longer block the queue; the window no longer crashes when switching discussions with details open; avatars are cropped from the center of the image; the settings page is no longer covered in narrow windows, and the discussion page collapses its list.

## 0.1.28

- 讨论里的图片附件直接显示缩略图，点开用默认程序查看；超大图片仍显示为文件。
- 在输入框里打 @ 弹出候选，可直接选 Agent 或人，选中后自动设好接收者。
- 有人 @ 你时，讨论列表标出「有人 @ 了你」，消息带「@你」标记，并发送系统通知，不再因同时到达的消息而漏掉。
- 协作讨论中，Agent 可设置「谁能 @ 它」：只有我、所有人或指定的人。被允许的人可以直接请它执行，请求仍在拥有者的电脑上运行，停止、重试和回答只有拥有者能做；拥有者的电脑会按本机设置再核对一次。双方都需要更新到这个版本。
- 讨论页更简洁：自己的消息靠右显示，不再加底色；已完成的请求收成一行；去掉重复的说明文字。
- 已配对的手机可以查看并参与这台电脑上的讨论和协作讨论（需要手机端 0.1.18）。

### English

- Image attachments in discussions now show as thumbnails and open in the default app; very large images still appear as files.
- Typing @ in the message box suggests agents and people, and picking one sets the recipient.
- When someone mentions you, the discussion list shows "Someone mentioned you", the message carries an "@You" tag, and a notification appears, even when other messages arrive at the same time.
- In shared discussions, each agent has a "Who can @ it" setting: only me, everyone or specific people. Allowed people can ask it to run directly; requests still run on the owner's Mac, only the owner can stop, retry or answer them, and the owner's Mac checks its own setting again. Both sides need this version.
- A cleaner discussion page: your messages sit on the right without a background, finished requests collapse to one line, and repeated labels are gone.
- Paired phones can read and join this Mac's discussions and shared discussions (requires the phone app 0.1.18).

## 0.1.27

- 新增讨论：多个 Agent 在同一讨论中共享上下文，可指定接收者、引用消息、标记关键结论、搜索历史并添加附件。
- 支持通过邀请加入另一台 Mac 分享的讨论，由房主批准；各自的 Agent 在自己的电脑上执行，断线后保留已同步内容和草稿。
- 添加成员遇到应答丢失时可原样重试，不重复创建；失败保留表单，提交时锁定字段，旧回复不会覆盖新表单。
- 改善长讨论的分页、滚动和跳到最新，释放关闭页面后遗留的界面对象引用。
- 局域网协作已完成本机隔离验收；真实双 Mac 的网络权限、防火墙、休眠和网络切换仍待验证。重试去重需要双方都更新。

### English

- New discussions let multiple agents share context, with recipients, message quotes, key conclusions, history search and attachments.
- Join discussions shared by another Mac through invitations and host approval. Each participant's agents run on their own computer, with synced content and drafts retained while offline.
- Retry adding a member after a lost response without creating duplicates. Failed submissions keep the form, pending submissions lock its fields, and old responses leave new forms untouched.
- Improved pagination, scrolling and jumping to the latest messages in long discussions, and released retained UI object references after closing a page.
- LAN collaboration has passed isolated local acceptance tests. Network permissions, firewalls, sleep and network changes across two physical Macs remain unverified. Retry deduplication requires both sides to update.

## 0.1.26

- 支持 Intel 芯片的 Mac：下载 Posse-Intel.dmg 安装，之后同样自动更新。
- 新增随处搜索：一个弹框分"所有""文件""符号""操作""文本"五页，⇥ 换页；⇧⌘P 打开"所有"，⌘P 打开"文件"，⇧⌘A 打开"操作"。设置「快捷键」里可以打开"双击 ⇧ 打开随处搜索"。
- 文件栏的检索并入"文件"页：顶上是检索框，有字时显示结果、清空回到文件树；大小写、正则和筛选常驻在框里。选中目录按 ⇧⌘F，或在目录右键菜单点"在此目录中查找…"，只搜这个目录。
- 终端标签的关闭按钮在标签变暗时不再凹出暗块，右边距加宽。

### English

- Posse now runs on Intel Macs: install from Posse-Intel.dmg, and it updates itself like the Apple silicon version.
- New Search Everywhere: one dialog with All, Files, Symbols, Actions and Text pages, switched with ⇥. ⇧⌘P opens All, ⌘P opens Files and ⇧⌘A opens Actions. "Double-tap ⇧ for Search Everywhere" can be turned on in Settings › Shortcuts.
- Search in the files panel moves into the Files page: a search box sits on top, showing results while it has text and the file tree once cleared, with case, regex and filter toggles inside the box. Select a folder and press ⇧⌘F, or choose "Find in Folder…" from its context menu, to search only that folder.
- The close button on terminal tabs no longer leaves a dark patch over the fade, and has more room on the right.

## 0.1.25

- 图片面板收录 Codex 查看图片、截图工具返回的图片，保留当时的快照；生成目录中的相同副本只显示一次，多次查看的记录仍保留。
- 修正会话记录重写后可能读错图片或沿用旧缩略图的问题。
- 多仓库工作区的 Git 日志在分支筛选旁显示所属仓库，悬停可看完整路径。
- 菜单的勾选标记与普通图标对齐，去掉多余缩进。
- 支持手机端 0.1.16 切换 Git 分支，操作结果记入桌面 Git 操作记录。

### English

- The image panel now includes images returned by Codex image-viewing and screenshot tools, preserving the snapshot seen at the time. Matching copies in the generated-images directory are hidden while repeated views remain separate.
- Fixed incorrect images or stale thumbnails after a session transcript is rewritten.
- In workspaces with multiple repositories, the Git log shows its repository beside the branch filter, with the full path on hover.
- Menu checkmarks align with other icons without an extra indentation column.
- Supports Git branch switching from mobile 0.1.16, with results recorded in the desktop Git operation log.

## 0.1.24

- 一个工作区目录下有多个 Git 仓库时都能识别（嵌套的、子模块、软链过去的也算），当前仓库跟着打开的文件走，也可以手动换。
- 改动页按仓库分组，一次提交可以勾选多个仓库的文件。
- 多仓库目录也能建 worktree 工作区：新建时勾选要带的仓库，每个仓库各建一个 worktree，改动、合并和删除都按仓库来；合并前有仓库会冲突时一个都不合。
- 改动列表去掉状态字母，文件名按状态上色，悬停显示状态。
- 文件树右键菜单可以删除文件，确认后移到废纸篓。
- 用量时间线每项悬停马上出提示，用户发的网址整条显示。
- 修正手机看桌面时 Mac 解锁后手机一直显示已锁屏。
- 配合手机端 0.1.15 的按仓库分组改动页。

### English

- Workspaces with several Git repositories are recognized, including nested repositories, submodules and symlinked ones; the current repository follows the open file and can also be switched by hand.
- The Changes page groups files by repository, and one commit can include files from several repositories.
- Folders with several repositories can get worktree workspaces: pick the repositories when creating one, and each gets its own worktree; changes, merging and removal work per repository, and nothing is merged if any repository would conflict.
- The change list drops status letters; file names are colored by status, and hovering shows it.
- The file tree's context menu can delete a file, moving it to the Trash after confirmation.
- Usage timeline items show their tooltip immediately on hover, and URLs sent by the user are shown in full.
- Fixed the phone still showing the Mac as locked after it was unlocked while viewing the desktop.
- Works with the repository-grouped Changes page in mobile 0.1.15.

## 0.1.23

- agent 状态更准：支持程序状态协议（OSC 7501），Claude、Codex 按 Esc 中断后马上回到空闲，Grok 也有准确的工作中和等待，提问时带着问题。更新后已经开着的 Claude、Grok 重开一次生效。
- worker 终端的返回按钮切到派发方的会话。
- 派发方空闲时，worker 的连线顶端拐过来接到它的图标。
- 切换标签按侧栏顺序跨项目切换，侧栏跟着滚动。
- 从访达把文件夹拖到侧栏，确认后加为项目。
- Git 日志打开时默认选中当前分支。
- 确认框点遮罩不再关掉，可以用键盘切换按钮、回车确认。
- 修正侧栏空闲会话行的底色范围。

### English

- More accurate agent status: Posse now supports the program status protocol (OSC 7501). Claude and Codex return to idle right after you press Esc, and Grok shows accurate working and waiting states, including the question it asks. Reopen Claude and Grok sessions that were already open after updating.
- The back button in a worker terminal switches to the dispatcher's session.
- When the dispatcher is idle, the top of a worker's connecting line bends over to its icon.
- Switching tabs follows the sidebar order across projects, and the sidebar scrolls along.
- Drag a folder from Finder to the sidebar and confirm to add it as a project.
- The Git log selects the current branch when it opens.
- Clicking outside a confirmation dialog no longer closes it; use the keyboard to switch buttons and Return to confirm.
- Fixed the background extent of idle session rows in the sidebar.

## 0.1.22

- 手机看桌面解锁更安全：密码只在 Mac 锁着时投给登录窗口，逐字检查，中途解开就停，不会打进解锁后最前面的 App（比如终端）；连点解锁不会把密码发几遍。
- 侧栏里派到同一项目别的工作区的任务直接用连线连起来，不再显示影子行。
- 设置里关掉的 agent，状态栏和手机都不再显示它的套餐用量，也不再去读取。
- 会话标题跟着界面语言写，英文界面用英文类型。
- 侧栏和手机不再显示看过的本轮完成。
- 配合手机端 0.1.13 的解锁改动。

### English

- Safer unlocking from the phone: the password is sent only to the login window while the Mac is locked, checked key by key and stopped as soon as the Mac unlocks, so it never lands in the app in front after unlocking (such as a terminal); tapping Unlock repeatedly no longer sends the password several times.
- Tasks sent to other workspaces of the same project are now linked directly in the sidebar instead of showing as shadow rows.
- Agents turned off in Settings no longer show plan usage in the status bar or on the phone, and their usage is no longer read.
- Session titles follow the interface language; an English interface uses English types.
- The sidebar and the phone no longer show finished turns you’ve already seen.
- Works with the unlock changes in Posse Mobile 0.1.13.

## 0.1.21

- 界面支持英文：设置「通用」的「语言」里选跟随系统、简体中文或 English，马上生效；报错、日期、命令速查和更新说明都跟着界面语言。
- 钟点跟系统的 12/24 小时设置。
- 设置里加主题：跟随系统、浅色、深色。
- Markdown 预览照 GitHub 的样式排版，正文里的 HTML 和图片也照 GitHub 显示；Markdown 字号并入编辑器字号。
- 本地网页在网页标签里打开，文件标签也能预览；`posse web open` 可以直接给文件路径。
- 侧栏用连线把派发方和它派出的任务连起来，派到别的工作区的任务显示成影子行。
- 侧栏恢复显示看过的本轮完成。
- 终端和侧栏的右键菜单加了图标和快捷键，终端菜单新增分栏、重命名、复制终端 ID。
- 终端标签的关闭按钮只在悬停时出现，标题按宽度截断。
- 设置里加「关闭前确认」开关，关掉后关闭运行中的会话和终端不再确认。
- Posse 自带的技能改为装进用户技能目录。
- 修正 10 个没对齐的图标。
- 看完 Claude 的 /usage 后，Fable 用量能及时跟上。
- 只有正式发布的包才检查更新，从源码打的包不会被官方版本换掉。
- 配合手机端 0.1.12：手机的消息改走加密二进制帧，iOS 支持推送通知。

### English

- English interface: choose System, 简体中文 or English under Settings › General › Language; it applies right away, and errors, dates, the Cheat Sheet and release notes follow the interface language.
- Times follow the system’s 12/24-hour setting.
- New Theme setting: System, Light or Dark.
- Markdown previews are styled like GitHub, including HTML and images in the text; the Markdown font size now follows the editor font size.
- Local web pages open in a web tab and can be previewed in file tabs; `posse web open` accepts a file path.
- The sidebar draws lines from a dispatcher to the tasks it started; tasks sent to other workspaces show as shadow rows.
- The sidebar shows finished turns you’ve already seen again.
- Terminal and sidebar context menus now have icons and shortcuts; the terminal menu adds split panes, Rename and Copy Terminal ID.
- A terminal tab’s close button only appears on hover, and titles are truncated by width.
- New “Confirm before closing” setting; turn it off to close running sessions and terminals without confirming.
- Posse’s own skills are now installed into the user skills directory.
- Fixed 10 misaligned icons.
- Fable usage now catches up right after viewing Claude’s /usage.
- Only official release builds check for updates, so builds from source are never replaced by the official version.
- Works with Posse Mobile 0.1.12: phone messages now use encrypted binary frames, and iOS supports push notifications.

## 0.1.20

- 分屏改成分栏：主区域可以左右、上下任意分成几格（最多四格），每格有自己的标签栏，终端和打开的文件都能放进去。
- 把标签拖到某一格的上下左右边缘就在那一边拆出新的一格，拖到格子中间或它的标签上就移进那一格；拖动时会标出落点。
- ⌘D 向右分栏、⇧⌘D 向下分栏，⌃⌘D 合并所有分栏，⌘]、⌘[ 在各格之间切换；标签右键也能移到右边或下方的新分栏。
- 拖分隔线调整大小，双击分隔线平分；分栏随工作区记住，重开窗口还在。
- 技能页：来源里找不到的技能不再提示有新版本；体检和读不到的那一家给出修复入口。
- 命令速查面板和图片、技能面板宽度统一。

### English

- Split view is now split panes: divide the main area left/right or top/bottom into up to four panes, each with its own tab bar, holding terminals and open files alike.
- Drag a tab to the edge of a pane to split off a new pane on that side, or onto the middle of a pane or its tabs to move it there; the drop target is highlighted while dragging.
- ⌘D splits right, ⇧⌘D splits down, ⌃⌘D merges all panes, and ⌘] / ⌘[ move between panes; a tab’s context menu can also move it to a new pane on the right or below.
- Drag a divider to resize and double-click it to split evenly; panes are remembered per workspace and come back when the window reopens.
- Skills: skills that are gone from their source no longer show an update, and health check issues and agents that can’t read a skill now come with a fix.
- The Cheat Sheet panel now matches the width of the Images and Skills panels.

## 0.1.19

- 侧栏顶部改为可拖动排序的磁贴：任务、历史、技能、手机、聊天之外，也能把常用的工作区拖上来固定。
- 新增「聊天」：侧栏顶上的磁贴开临时对话，每个会话在 ~/Posse 下单独建文件夹，之后可以转成项目。
- 侧栏项目列表上方新增「项目 | 运行中」切换：「运行中」把所有项目的 agent 会话平铺成一列，按状态排好，第二行标出项目和工作区；状态变了只换图形、不挪位置。
- 侧栏会话行首固定显示 agent 图标，状态挪到左边的状态槽。
- 会话按钮新增命令速查：分内置技能、技能、内置命令、快捷键四个页签，列出各家 agent 的斜杠命令、快捷键和输入前缀，点一下贴进输入框。
- 任务页可以用 ↑↓ 切换任务，任务说明按 Markdown 显示。
- Git 日志、比较、文件历史的提交和文件列表支持方向键；改动、回滚、提交与比较的文件列表显示按类型的文件图标。
- 历史页的对话装进卡片，铺满宽度。
- 图片面板的卡片能拖进终端贴路径，右键可复制图片；粘贴访达里复制的文件时贴完整路径，视频等任意文件都能粘贴。
- 终端里指向别的工作区文件的链接，改在当前工作区的文件标签里打开。
- 删除工作区和项目时可以一并清理 agent 在工作区外留下的痕迹。
- 界面上的报错按界面语言显示，不再露出英文原文和错误码。
- 配合手机端 0.1.10。

## 0.1.17

- 技能页新增「检查更新」：查出 npx skills 装的技能哪些有新版本，列表里标出来，可以只看它们，详情里的更新按钮跟着变醒目。
- 安装技能先读出来源里有哪些技能再勾选，已经装过的会标出来；没装 Node.js 时提示安装。
- 安装、更新、删除技能改在对话框里的终端运行，跑完当场显示结果并刷新列表，不用切走。
- 技能详情标出触发方式（自动或手动）和经软链读到的路径，能直接打开来源网页；列表可以用上下键切换。
- 新增 MCP 分页：集中查看 Claude、Codex、Grok、Cursor 配置的 MCP 服务，每家是否用上、从哪个文件读到、怎么启动、用过几次，环境变量只显示名字。

## 0.1.16

- 新增技能管理：侧栏「技能」页集中查看 Claude、Codex、Grok、Cursor 装的技能，每家是否生效一目了然，可以搜索、按 agent 筛选、按用量排序；详情里有跨会话的使用次数和最近一次使用时间。
- 技能可以按 agent 单独开关，改配置文件前弹框列出要改的文件和 diff，确认后才写。
- 用 `npx skills` 安装、更新、删除技能；文件栏新增「技能」页签，看项目里的技能，也能新建项目技能。
- 技能体检：找出断掉的链接、重复的副本、多余的链接和锁文件里缺的条目，链接可以一键移到废纸篓。
- 派出去的 Cursor worker 不再卡在工作区信任框上。
- Markdown 预览里的行内代码改用强调色文字和代码块底色，和手机端一致。

## 0.1.15

- 任务汇报后可以接着给同一个 worker 追加任务：任务页「追加任务」，agent 用 `posse task send`。派发任务的 agent 派完不再干等结果，用户随时能插话。
- Claude、Codex、Cursor 忙着的时候也能收到任务消息，在两次工具调用之间读到，不用等它停下来。
- 文件栏按文件类型显示图标，认得 Flutter、iOS、Android、Vue 工程和 Xcode 的包目录；侧栏项目下的主工作区改用房子图标。
- Markdown 预览重新排版：文件开头的 YAML 画成属性列表，`[[名字]]` 可以点，代码块不折行、左右滚动；切走再回来保留滚动位置；预览和历史页的对话可以单独调字号。
- 网页标签 ⌘R 刷新、⇧⌘R 强制刷新，可以切到手机预览。
- 技能与工具面板新增网址一组，列出访问过的网址和网页搜索，可以只看网址；出错的 MCP 调用不算次数、标成失败；技能识别更准，搜一下技能文件不再算用了一次。
- Codex 自己设了 developer_instructions 时，编排说明接在后面，不再把它顶掉。
- 同一份数据只开一个窗口；空工作区页窗口窄时启动格分行；标签栏的「+」跟着标签走。

## 0.1.14

- 终端可以左右分屏：终端里按 ⌘D 把当前终端放到右栏，⌘] 或 ⌘[ 换到另一栏，⇧⌘D 关闭分屏；标签右键「在右侧打开」。拖中间的线调宽，松手后两边只重排一次；重开窗口分屏还在。
- agent 能用 `posse web` 打开、查看和操作 Posse 里的网页标签：列出页面上的按钮和输入框、填表、点击、截图、看控制台输出（给 agent 的说明在 `posse web guide`）。
- 改动页可以按目录分组查看，目录能收起、整个勾选和回滚。
- Markdown 预览里的 mermaid 代码块画成图。
- 文件预览和图片 diff 支持动图，更多格式的图片能打开。
- 看过的本轮完成不再显示状态图标。
- Codex 里自己发的消息不再是深灰底。
- 续接的 Codex、Cursor 会话在第一轮对话之前，技能与工具面板也能找到会话。
- 连着改界面设置时，设置文件不会再写坏。

## 0.1.13

- 终端里被换行的长路径和网址按整条识别：终端自动折行的、Codex 在 `/` 后自己换行的都算，悬停、点击哪一行都打开完整的链接。
- 终端里的文件路径 ⌘ 点击直接在文件标签里打开并跳到行号，目录在文件栏中显示；⇧⌘ 点击交给系统，文件用默认程序打开，目录在访达中打开。
- 设置「通用」新增「终端链接」：可以改成单击直接打开、⌘ 点击弹卡片，也可以让 ⌘ 点击和 ⇧⌘ 点击互换。
- 设置「通用」新增「选中即复制」：在终端里选中文字、松手就复制。
- 终端标签右键可以复制引用（贴给 agent 的一行说明），以及终端、任务、会话的 ID。
- Dock 图标的角标显示待处理和未读会话数；设置里可以关掉通知和角标。

## 0.1.12

- Codex 忙着的时候在终端里滚动鼠标滚轮，输入框里不再冒出一串 `[<64;53;8M`。

## 0.1.11

- 终端里可以查找：⌘F 打开查找栏，⌘G、⇧⌘G 在命中之间跳转，命中会高亮；agent 的终端搜不到时，可以一键跳到历史页按同一个词搜会话。
- 文件栏顶部加「定位当前文件」（⌥F1）和「全部折叠」；Markdown 的源码、预览切换改成图标。
- 工作区目录在打开文件栏之后才创建的，文件树也能读出来。
- 打开几 MB 大文件的 diff 不再让 Posse 占用上 GB 内存，关掉后内存也会降回来。
- 终端持续输出、辅助功能类工具读过窗口之后，Posse 窗口更省内存；窗口隐藏或最小化时不再统计终端的内存。

## 0.1.10

- 终端里的网址可以在 Posse 里打开：单击弹出卡片，可以在 Posse 中打开、在浏览器中打开或复制；⌘ 点击直接在 Posse 里打开，⇧⌘ 点击交给默认浏览器。
- 网页在文件标签里显示，用系统的 WebKit：有后退、前进、重新载入和地址栏，本机和局域网地址自动补 http://，页面里新开窗口的链接在旁边再开一个标签；打不开时显示原因，可以重新载入。
- 文件路径的卡片也把「复制路径」换成路径后面的复制图标。
- 侧栏项目的图标可以自定义：右键选「更换图标…」，填 emoji、一两个字或选一张图片，手机首页同步显示。
- 窗口自动更新常驻进程时，如果正好已有一次更新在进行，不再报错。

## 0.1.9

- Git 分支比较：每栏有自己的搜索框，按提交信息、作者或哈希筛选；⌘ 点、⇧ 点可以多选提交，一次 Cherry-pick 多个，按提交先后应用。Cherry-pick、交换、刷新挪到上一栏标题旁，交换换成图标，中间的分隔改成细线。
- 文件树、改动、提交和比较里的文件照 WebStorm 按 Git 状态上色：修改蓝、新增绿、删除灰、未加入 Git 和冲突红，含改动的目录也标蓝。
- 会话的图片面板顶部分成「生成」「粘贴」两页切换。
- JSON 文件的「格式化」换成花括号图标。

## 0.1.8

- 设置里新加「快捷键」：Posse、终端和编辑器的快捷键都列在这里，可以按名称或按键搜索，点一下重新录、加一组或恢复默认。
- Markdown 预览和 Annotate 里 ⌘F 可以查找，回车、⌘G 跳到下一个，命中会高亮。
- JSON 文件的顶栏有「格式化」（⇧⌥F），按原来的缩进重排，能撤销。
- 文件树里的符号链接换成带箭头的图标，悬停显示指向哪里。
- 分支弹窗里更新项目、更新分支、删除分支后弹窗不再关掉，结果就地刷新。

## 0.1.7

- 重新打包或更新 Posse 后，已经开着的终端访问桌面、文稿等目录不再被系统拒绝：常驻进程改从 Posse 的一份副本运行，换包不影响它和它开的终端。
- 重启后未恢复的终端保留原来的名字，「未恢复」改成状态图标。
- 完成未读的提醒改由常驻进程记，关掉窗口再打开、在手机上看过都会同步。
- Git 分支比较的顶栏加了 Cherry-pick，界面里的「拣选」统一叫 Cherry-pick。
- Git 操作记录按操作分行显示结果图标，重复的自动 Fetch 合成一条。
- 技能与工具的时间线里，技能、MCP、命令行各留最近 300 条，命令行用得多时不再挤掉技能。
- 配合手机端新版：对话页显示 agent 正在输出的内容，首页同步等待任务、启动中、完成未读和未恢复状态，会话页能看技能与工具。

## 0.1.6

- 重启电脑或常驻进程后，上次的终端还在：侧栏和标签栏照常列出，标着「未恢复」，切到它时才拉起，agent 接着原来的会话，普通终端在原来的目录重开（之前的输出不保留）。设置 › 通用 › 恢复终端 可以改成启动时全部恢复。
- 在 Posse 外面挪走或删掉了目录的工作区会标出来，可以重新指定目录，或从 Posse 移除。
- 从别处加进来的 worktree 在侧栏和标题显示它当前的分支。
- 设置里可以把右侧文件栏的开关改成所有项目共用。
- 文件列表读取失败后隔一会儿再试，不再反复报错。
- Markdown 预览的列表圆点更大，和首行对齐。
- Git 日志的选中和悬停底色改成内缩圆角，三栏列表行距统一；分支菜单的重要操作加了图标。
- 窄窗口里空状态的说明卡片跟着变窄，不再盖住左边的列表。

## 0.1.5

- 设置 › Agent 可以单独关掉某家 agent：关掉后新建会话、派发任务、新建工作区和手机里都不再列出它，也不能从历史里恢复它的会话；在终端里自己敲命令照样能用。
- 设置 › Agent 可以整个关掉套餐用量：关掉后不再读取、查询，底栏和手机首页都不显示。
- 设置 › 外观可以隐藏侧栏项目行的分支和路径、工作区行的分支，只留名字。
- 侧栏顶上的任务、历史、手机固定不动，只有项目列表跟着滚动。
- 侧栏底部的"已更新""已就绪"和报错提示换成带图标的淡底卡片，和项目列表分得开；"已更新"的提示可以先收起，齿轮旁留一个圆点提醒还没重开。

## 0.1.4

- 侧栏会话行和顶部标签排法一致：最左是状态，接着 agent 图标和名字，字和图标都小一档；名字有空位时完整显示，不再提前截断。
- 侧栏、标签栏和状态栏两头的间距统一，和左上角的红绿灯对齐。
- 切换标签时文字不再跳动：选中的标签只换底色和文字颜色，不再加粗。

## 0.1.3

- 手机看桌面更清楚：直连时起步码率提高到原来的两三倍（照 Moonlight 的默认值）；画面停下后自动把最后一帧补清楚，滚动、打字停下来文字不再糊着。
- 画面一变时的一个大帧不再被当成网络拥堵，没丢包的直连不会被降码率。

## 0.1.2

- Git 更新项目时，本地改动取回有冲突不再报"更新失败"：照 WebStorm 打开改动页，横幅说明冲突文件、怎么解决，解决完可以确认删掉留下的储藏；没能取回时提示去储藏里手动应用。

## 0.1.1

- 打开新版本时，如果还在运行的常驻进程是旧版本、读不到项目，会自动把它换成新版本，终端不断；换不了时提示到设置 › 关于 重启常驻进程。

## 0.1.0

- 第一个经公证的版本，装在「应用程序」里之后会自动更新。
