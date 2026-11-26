# 手机端更新内容

每个版本一节，标题是 `## <版本>`：发布 iOS、安卓安装包时这一节写进 latest.json 和安装页，App 首页提示更新时显示它。
一节里先写中文条目，再在 `### English` 小节里写对应的英文条目：中文条目写进 latest.json 的 `notes` 和安装页，英文条目写进 `notes_en`。

## 0.1.39

- 语音识别改为输入框内联交互（覆盖终端草稿栏、对话页与讨论页）：移除上方独立预览框，识别文字直接流式显示在输入框内，右侧显示柔和的小声波动画与取消/完成键，输入栏单行高度保持不变、零跳动。
- 新增按住说话手势：输入框为空时长按输入框即可直接说话（松手完成转写、上滑取消，参考微信输入法），有文字时长按仍完整保留系统选字与移动光标；麦克风按钮同时支持轻点免手按与长按说话。

### English

- Redesigned voice recognition as an inline in-field experience across the Terminal draft bar, Chat, and Discussions: removed the floating preview box above the composer, streams recognized text directly inside the input box, and shows a subtle mini sound-wave animation with cancel/finish controls on the right at the exact same row height.
- Added hold-to-talk gestures: long-press an empty input box to speak (release to transcribe, slide up to cancel), while non-empty fields keep native cursor and text selection untouched; the mic button also supports both tap (hands-free) and long-press hold-to-talk.

## 0.1.38

- 修复初次进入终端时常驻语音与草稿输入栏会短暂遮挡底部会话内容的问题：常驻模式下直接预留单行基准高度并关闭入场滑动动画，多行输入与键盘弹出仍保持不改变终端尺寸。

### English

- Fixed the persistent voice & draft bar briefly overlapping the bottom of a terminal session on initial entry: reserves the single-line bar height directly in the terminal viewport and skips the entry slide animation, while keeping multi-line expansion and keyboard lift free of PTY resizes.

## 0.1.37

- 终端「常驻语音与草稿输入栏」（设置 › 终端）改为默认开启：开启后草稿与语音栏常驻在终端快捷键栏上方，并自动隐藏工具栏中的语音按钮与收起键；关闭后恢复从工具栏随时展开或收起。
- 修复 iOS 系统语音识别持续报错「语音转写失败」的问题：改为 `.record + .measurement` 音频会话、调整识别任务与音频引擎启停顺序、清理旧识别任务避免连续报错，并放开强锁离线限制（优先本机离线，离线包未就绪时自动回退 Apple 系统语音服务）且开启自动标点。
- 修复 iOS 应用内点击更新下载后需手动杀掉 App 才能完成安装的问题：触发更新前自动暂停后台静音保活与后台任务，切回主屏幕后系统可直接覆盖安装。

### English

- Enabled the Persistent Voice & Draft Bar (Settings › Terminal) by default: keeps the voice and multi-line draft bar pinned above the terminal key bar while hiding the toolbar voice button and collapse arrow; turn it off to toggle the bar from the key bar.
- Fixed iOS system speech recognition repeatedly failing: switched to `.record` + `.measurement` audio session, fixed task/engine start order, cancelled stale recognition tasks on teardown, added automatic punctuation, and allowed fallback to Apple speech services when on-device assets are not ready.
- Fixed iOS in-app updates waiting until the app is manually force-quit: pauses background silent audio keep-alive and background tasks before launching OTA installation so iOS can replace the app cleanly once you return to the Home Screen.

## 0.1.36

- 新增「语音输入转可编辑草稿」（设置 › 语音输入），覆盖终端草稿栏、对话页与讨论页：支持系统本机离线识别及云端转写（OpenAI 兼容 / Groq / 硅基流动 / 火山豆包极速版 / 自定义），支持项目、分支、Agent 与自定义编程术语提示词，API Key 存于系统钥匙串 / Keystore，录音中断或转写失败自动保留音频供原地重试。
- 终端页新增折叠式草稿输入栏（Staged Input）：快捷键栏新增「草稿 / 语音」入口，支持多行草稿编辑、单行「仅填入」与「发送」，收起后保留草稿并显示指示点，展开不触发终端尺寸重排，并可在设置中设为默认展开。

### English

- Added Voice-to-Editable-Draft input (Settings › Voice Input) across the Terminal draft bar, Chat, and Discussions: supports on-device offline speech recognition and cloud transcription (OpenAI-compatible / Groq / SiliconFlow / Volcengine Doubao Flash / Custom), project/branch/agent and custom developer term prompts, Keychain/Keystore API key storage, and in-place retry with retained audio on interruption or failure.
- Added a collapsible Terminal Draft Bar (Staged Input): adds a Draft / Voice key to the terminal keybar with multi-line editing, single-line Paste-only and Send actions, draft persistence with an indicator dot when collapsed, zero PTY resize when toggling, and an option to expand by default.

## 0.1.35

- 远程桌面的返回、连接状态/FPS 和工具条三个按钮均支持自由拖动，拖动范围可延伸至屏幕安全区，横竖屏位置分别记住并自动避开重叠。
- 连接状态/FPS 按钮优化为与返回按钮同尺寸的紧凑布局，常驻展示当前 FPS 数值（或「静止」「—」）。
- 修复设置页向上滑动时偶尔发生画面跳动的问题。

### English

- All three Remote Desktop floating controls (Back, Status/FPS, and Toolbar) can now be dragged freely—including into the safe area—with per-orientation position memory and automatic overlap avoidance.
- Compacted the Status/FPS button to match the Back button size while showing the live FPS value (or Still / —).
- Fixed occasional scroll jumps when scrolling the Settings screen.

## 0.1.34

- 新增「内置 Tailscale」（设置 › 内置 Tailscale）：无需安装官方 Tailscale App，也不占用系统 VPN 槽位，可与 Surge、Shadowrocket、Clash 等代理工具同时使用；支持浏览器交互登录、一次性 AuthKey、自定义 Headscale 控制服务器、「跟随系统代理或 VPN 路由」开关与重置节点身份。
- 开启并登录后，连接电脑的 Tailscale 地址自动经内置节点以 QUIC 通信（兼容电脑端官方 Tailscale 或内置 Tailscale）；看桌面支持显式 Tailscale 线路，并在配了自建中转且对端走 DERP 时自动优先自建中转。
- 会话页状态行与看桌面调试面板区分「Tailscale 直连」与「Tailscale 中继 (DERP)」。电脑端内置 Tailscale 需要 Posse for Mac 0.1.49 或更新版本。

### English

- Added Built-in Tailscale (Settings › Built-in Tailscale): runs a standalone node inside Posse without taking the system VPN slot, so it works alongside proxy apps like Surge, Shadowrocket, or Clash. Supports browser sign-in, one-time Auth Keys, custom Headscale control servers, a “Follow system proxy / VPN routing” toggle, and identity reset.
- Once connected, Posse automatically reaches your Mac’s Tailscale address over QUIC through the built-in node (compatible with both official Tailscale and Built-in Tailscale on Mac). Remote Desktop adds an explicit Tailscale path and prefers your self-hosted relay when the peer is on DERP.
- Session status lines and the Remote Desktop debug panel now distinguish Tailscale Direct from Tailscale Relay (DERP). Mac-side Built-in Tailscale requires Posse for Mac 0.1.49 or later.

## 0.1.33

- 讨论支持单条消息同时 `@` 多位可用 Agent（最多 8 位，与 `@人` 互斥），长按引用可用 Agent 消息时自动预选该成员，取消引用自动收回。
- 讨论执行行显示实时耗时，本机活跃执行额外显示一行当前工具/状态进度和后台子代理数，点击「过程」可展开折叠该轮工具调用时间线。
- 以上讨论新功能需要 Posse for Mac 0.1.48 或更新版本。

### English

- Discussions now support mentioning up to 8 usable agents in one message (mutually exclusive with `@person`). Long-pressing to quote a usable agent message automatically preselects that member and clears it if the quote is removed.
- Discussion executions show live elapsed time, active local rounds show a single-line tool/status progress summary and background subagent count, and tapping Activity expands a collapsible timeline of tool calls for that round.
- These discussion features require Posse for Mac 0.1.48 or later.

## 0.1.32

- Antigravity 回复会在手机对话页逐步显示，发送图片时会作为原生附件交给 Antigravity。
- 查看 Antigravity 的上下文用量、各额度池余量和重置时间。套餐用量也可查看 AI 积分余额，并打开消费明细。
- 这些 Antigravity 功能需要 Posse for Mac 0.1.46 或更新版本。

### English

- Antigravity replies appear as they stream in mobile Chat, and images sent from Chat are passed to Antigravity as native attachments.
- See Antigravity context usage, remaining quota, and reset times. Plan usage also shows the AI credit balance and links to credit activity.
- These Antigravity features require Posse for Mac 0.1.46 or later.

## 0.1.31

- 连接安装了 ZCode CLI 的 Mac 后，可以新开 ZCode 终端、搜索会话历史并续接。需要 Posse for Mac 0.1.45 或更新版本。
- 从 ZCode 终端的「更多」菜单查看会话图片、视频、工具调用次数和 Token 用量。可上传图片，让 ZCode 从文件路径读取。

### English

- Start ZCode terminals, search their history, and resume sessions on a connected Mac with the ZCode CLI installed. Requires Posse for Mac 0.1.45 or later.
- Open the ZCode terminal's More menu to see session images, videos, tool counts, and token usage. Upload images for ZCode to read from their file paths.

## 0.1.30

- Antigravity 在同一个终端切换会话时，对话和草稿跟随各自的会话。发送消息和压缩上下文前会核对目标会话。需搭配 Mac 0.1.44 或更新版本。
- 可查看 Antigravity 套餐余量和重置时间。轻点会话的上下文读数可压缩上下文，终端的「更多」菜单可复制 `/skills` 和 `/mcp`。
- Antigravity 的历史记录缺少工作目录时，可以选工作区再恢复会话。

### English

- When Antigravity switches conversations within a terminal, messages and drafts stay with their own conversations. Sending and compacting context check the target conversation. Requires Mac 0.1.44 or later.
- See remaining Antigravity quota and reset times. Tap the context readout to compact context, or copy `/skills` and `/mcp` from the terminal's More menu.
- When an Antigravity history entry has no working directory, choose a workspace before resuming it.

## 0.1.29

- 跨行复制命令时保留参数间的空格。带选项的命令可保留换行或用空格连接，断行网址仍可不加空格拼接。

### English

- Copying wrapped commands preserves argument boundaries. Commands with options can keep their line breaks or join with spaces. Split URLs can still join without spaces.

## 0.1.28

- 普通「复制」和键盘复制快捷键遇到跨行选区时先预览。疑似折行的命令默认用空格连接，断行网址默认直接拼接，多行代码可保留原有换行和缩进。

### English

- Terminal Copy and its keyboard shortcut preview selections with line breaks. Wrapped commands suggest joining with spaces, split URLs suggest joining without spaces, and multiline code can keep its original line breaks and indentation.

## 0.1.27

- 终端复制会接回自动折行，保留正文的换行和缩进。跨硬换行的选区支持预览「复制为单行」，可用空格连接或直接拼接。重连后保留软换行需要 Mac 0.1.43。
- 终端可长按 OSC 8 超链接复制完整地址；对话和历史可长按 Markdown 链接或复制代码块原文。
- 历史支持 Codex 压缩会话、Cursor 编辑器对话和 Grok 完整对话流水，子会话挂在父会话下，思考过程默认折叠。支持启动和恢复 Antigravity 会话。
- 远程桌面工具条可拖动，返回和连接状态固定在顶部。键盘按操作位置避让，画面保持原缩放比例。

### English

- Terminal copy joins automatic wraps and preserves line breaks and indentation in the original text. For selections across hard breaks, Copy as One Line previews joining with or without spaces. Preserving soft wraps after reconnecting requires Posse for Mac 0.1.43.
- Long-press an OSC 8 terminal hyperlink to copy its full address. In Chat and History, long-press Markdown links or use the copy button on code blocks.
- History reads compressed Codex sessions, Cursor editor conversations, and complete Grok transcripts. Child sessions appear under their parent, and reasoning is collapsed by default. Antigravity sessions can be started and resumed.
- The remote desktop toolbar can be dragged, with Back and connection status fixed at the top. The keyboard keeps the active area visible at the current zoom.

## 0.1.26

- 看桌面的清晰度和帧率改为紧凑入口，选项以浮层展开，不再挤动屏幕与窗口列表；展开、切组和收起带过渡动画，选完保持面板打开。
- 修复打开或刷新套餐用量时面板上下跳动的问题。
- 文件树按 Git 状态上色，父目录汇总改动、忽略项淡化，与桌面保持一致；未跟踪文件使用 WebStorm 配色，与冲突红色区分（文件树上色需要 Mac 0.1.40 及以上）。
- 文件页可见时自动刷新，支持嵌套仓库和软链；Git 状态读取失败仍可浏览，切后台或换连接后停止旧目录请求。
- 历史列表列出全部会话，分组条数与电脑上的总会话数一致。搜索仍只显示最相关的结果。

### English

- Remote desktop quality and frame rate now use compact controls with floating options that do not shift the display and window lists. Opening, switching and closing animate smoothly, and selecting a value keeps the panel open.
- Fixed layout jumps when opening or refreshing plan usage.
- The file tree now matches desktop Git status colors, highlights changed parent folders and dims ignored entries. Untracked files use WebStorm colors, distinct from conflict red (file tree colors require Posse for Mac 0.1.40 or later).
- Files refresh automatically while visible, including nested repositories and symlinks. Browsing remains available when Git status fails, and pending directory requests stop when the app enters the background or changes connections.
- The history list includes every session, so group counts match the total on the Mac. Search still shows only the most relevant results.

## 0.1.25

- 看桌面的工具条始终展开，可用拖柄自由移动，松手不吸边，横竖屏位置分别记住；返回和 FPS 固定在画面上方，不占用画面空间，键盘弹出时自动避让。
- 看桌面新增页内设置，可直接切换触控板／直接点按、查看手势和清除记住的密码，与全局设置同步，无需重连。
- 触控板模式只保留三指拖动，抬手后 1 秒内可放回三指续拖；改用一指移动或双指滚动时先松开鼠标左键。直接点按模式仍支持长按拖动。
- iOS 通知和角标可独立开关，关闭角标立即清零，回前台时重新核对系统权限；独立控制需要 Mac 0.1.39 及以上，只更新角标还需要新版中转。
- 会话快捷工具条支持长按拖动排序，与设置页双向同步并保存；键盘入口保持固定。
- Git 文件状态颜色与桌面端一致，正确显示暂存后又修改或删除的文件；调整首页讨论图标大小，保留原点击区域。

### English

- The remote desktop toolbar stays expanded and can be moved freely with its drag handle without snapping to an edge. Portrait and landscape positions are remembered separately. Back and FPS remain fixed over the picture without reducing its size, and the toolbar avoids the keyboard.
- Remote desktop now has settings for switching between trackpad and direct touch, viewing gestures and clearing saved passwords. These share the global preferences and apply without reconnecting.
- Trackpad dragging now uses three fingers only. Lift and replace three fingers within one second to continue dragging; switching to one-finger pointing or two-finger scrolling releases the mouse button first. Direct touch still supports long-press dragging.
- iOS notifications and badges can be controlled independently. Disabling badges clears them immediately, and system permissions are rechecked on returning to the app. Independent control requires Mac 0.1.39 or later; badge-only updates also require the updated relay.
- Long-press and drag to reorder the session shortcut bar. Changes sync with Settings and are saved; the keyboard button stays fixed.
- Git status colors now match the desktop, including files modified or deleted after staging. The home screen discussion icon is smaller while retaining its tap area.

## 0.1.24

- 看桌面可选清晰度：自动、720p、1080p、1440p，在悬浮球的「画面」面板里与帧率分别设置，按电脑记住，观看中可直接切换（需要电脑端 0.1.38 及以上）。
- 自动档随网络调整画面尺寸；固定档保持所选尺寸上限，画面等比缩小、不放大小窗口，码率继续自适应。
- 连续选择清晰度时，尚未发出的选择只保留最后一档；切后台或观看目标变化时自动收起旧面板，窗口消失回到整屏时显示提示。

### English

- Choose Auto, 720p, 1080p or 1440p under Picture in the floating ball, independently of frame rate. The choice is remembered per computer and can be changed while viewing (requires Posse for Mac 0.1.38 or later).
- Auto adapts video dimensions to the network. Fixed presets retain their size limit, preserve the aspect ratio without upscaling small windows, and keep adaptive bitrate enabled.
- Rapid resolution changes keep only the latest choice that has not yet been sent. The picture panel closes when the app enters the background or the capture target changes, and a message appears when a missing window falls back to the full display.

## 0.1.23

- 看桌面可以手选帧率：悬浮球的「画面」里选自动、60、30、15，按电脑记住，观看中改了立即生效（需要电脑端 0.1.35 及以上）。
- 修复：看桌面时一次输入或粘贴较长的文字，前面一段会重复发到电脑上。
- 修复：终端打不开时一直转圈，现在显示电脑给出的原因。
- 修复：换网络、换连接时正在上传的文件失败；上传大文件更快。
- 修复：讨论里被电脑拒绝的发言之后输入框不能再用；讨论详情页自己操作出错时没有提示。
- 修复：看桌面在弱网下确认迟迟不到时，连续输入越打越卡。
- 修复：项目图标加载失败后不再重试；工作区列表偶尔停止刷新。
- 修复：iOS、安卓看桌面在切换、退出时偶发的问题；按 IPv6 地址配对的电脑看不了桌面。
- 以及一批来自全项目审查的稳定性修复。

### English

- Pick the frame rate when viewing the desktop: choose Auto, 60, 30 or 15 under Picture in the floating ball. It is remembered per computer and applies at once while viewing (needs Posse for Mac 0.1.35 or later).
- Fix: typing or pasting a long piece of text while viewing the desktop sent its first part to the computer twice.
- Fix: a terminal that could not open kept spinning; it now shows the reason the computer gave.
- Fix: an upload in progress failed when the network or connection changed; large uploads are faster.
- Fix: after the computer rejected a message in a discussion, the message box could not be used again; the discussion details page showed nothing when your own action failed.
- Fix: on a weak network, typing while viewing the desktop got slower and slower when acknowledgements were late.
- Fix: a project icon that failed to load was never retried; the workspace list occasionally stopped refreshing.
- Fix: occasional problems when switching or leaving the desktop view on iOS and Android; the desktop could not be viewed on a computer paired by an IPv6 address.
- Plus a batch of stability fixes from a full review of the project.

## 0.1.22

- 修复：传大图、视频时 App 卡住、延迟一下子飙升。现在按电脑的确认控制发送速度，上传时按键和其他操作照常响应（需要电脑端 0.1.34 及以上，旧版电脑照旧）。
- 配合电脑端 0.1.34：看桌面时在 iOS 模拟器、iPhone 镜像这类窗口里打字，数字和字母不再全变成 a。

### English

- Fix: the app froze and latency spiked while uploading a large image or video. Uploads are now paced by acknowledgements from your Mac, so keystrokes and other actions stay responsive (needs Posse for Mac 0.1.34 or later; older Macs behave as before).
- With Posse for Mac 0.1.34: typing into windows such as the iOS Simulator or iPhone Mirroring while viewing the desktop no longer turns every digit and letter into "a".

## 0.1.21

- 讨论模块重新整理：添加、编辑 Agent 改成整页表单，标签都在输入框上方；打开终端、继续、重启、移除放在编辑页底部。
- 回答 Agent 的提问改在输入框里写，上方标出在回答谁；输入框多了一个 @ 按钮。
- 搜索消息改在顶栏，讨论详情改成整页；讨论列表右上一个「+」新建或加入，归档的讨论在列表底部。

### English

- Reorganized discussions: adding or editing an agent is now a full-page form with labels above each field; open terminal, resume, restart and remove sit at the bottom of the edit page.
- Answer an agent's question right in the message box, which shows who you're answering; the message box also has an @ button.
- Message search moved to the top bar and discussion details open as a full page; one "+" in the list creates or joins a discussion, and archived discussions are at the bottom of the list.

## 0.1.20

- 新增「名字和头像」：在设置里或讨论详情里点自己，就能改名字、换头像，和电脑端设置里的个人资料是同一份；讨论里的消息显示每个人的名字和头像。
- 修复讨论页轮询时消息区域上下抖动。
- 名字和头像需要电脑端 0.1.29 及以上。

### English

- New "Name and avatar": change your name and avatar in Settings or by tapping yourself in discussion details. They are the same profile as on your Mac, and discussion messages show each person's name and avatar.
- Fixed the message area jittering while the discussion page polls for updates.
- Name and avatar need Posse for Mac 0.1.29 or later.

## 0.1.19

- 讨论页重新设计，和电脑端一致：自己的话靠右，执行状态收成一行，详情改成分组卡片。
- 讨论里的图片直接显示缩略图，点开看原图。
- 输入框打 @ 弹出候选；别人放开给你的 Agent 也能直接 @，用不了的会提醒它的拥有者。
- 讨论列表标出 @ 你的讨论；自己的 Agent 可以设「谁能 @ 它」。
- 需要电脑端 0.1.29 及以上才能看到图片缩略图。

### English

- Redesigned discussions to match the Mac: your messages sit on the right, execution status is a single line, and details use grouped cards.
- Images in discussions show as thumbnails; tap to open the original.
- Typing @ in the message box shows suggestions. You can @ agents their owners opened to you; picking one you can't use reminds its owner.
- The discussion list marks rooms where someone mentioned you, and you can set who can @ your agents.
- Image thumbnails need Posse for Mac 0.1.29 or later.

## 0.1.18

- 手机端新增群聊讨论：创建和加入讨论，发送消息、引用、标记关键内容、搜索历史及分享附件。
- 可在讨论中添加自己的 Agent、发起任务、回答提问、取消排队或停止执行，并打开对应终端。
- 支持协作成员权限、归档恢复和离线阅读；断线保留草稿，发送重试避免重复消息。
- 群聊需要电脑端支持手机讨论协议；连接旧版电脑时会提示升级。

### English

- Discussions are now available on mobile: create and join rooms, send and quote messages, mark key content, search history, and share attachments.
- Add your own agents, start tasks, answer questions, cancel queued requests or stop executions, and open their terminals.
- Includes collaboration permissions, archiving and restoring, and offline reading. Drafts survive disconnects, and retries avoid duplicate messages.
- Discussions require a Mac version with mobile discussion support; older versions show an upgrade prompt.

## 0.1.17

- iOS 切到别的 App 后连接留约 30 秒，这期间回来不用重连。
- 设置新增「后台保持连接」：打开后 App 在后台一直保持连接，终端照常收输出；更费电，默认关闭。

### English

- On iOS, the connection now stays for about 30 seconds after you switch to another app, so coming back within that time doesn't reconnect.
- New Settings option, Stay Connected in Background: keeps the connection alive while the app is in the background so terminals keep streaming. Uses more battery; off by default.

## 0.1.16

- 工作区和 Git 页面支持查看、搜索并切换分支，多仓库工作区可分别操作；切换结果记入电脑端 Git 操作记录，需要电脑端 0.1.25。
- 工作区页的分段切换移到顶栏，统一页面间距；横屏避开刘海，底部面板内容可滚动。
- 弹框统一字号与边距，确认框使用等宽按钮，并优化弹出和分层动画。
- 配合电脑端 0.1.25，图片与视频页可查看 Codex 工具返回的图片快照，生成目录中的相同副本不再重复显示。

### English

- Browse, search and switch Git branches from workspace and Git pages, including individual repositories in multi-repository workspaces. Results appear in the desktop Git operation log. Requires Posse 0.1.25 on the Mac.
- Workspace tabs move into the top bar with consistent page spacing. Landscape layouts respect display cutouts, and bottom-sheet content can scroll.
- Dialogs use consistent typography and spacing, equal-width confirmation buttons, and improved entrance and layered animations.
- With Posse 0.1.25 on the Mac, the media page shows snapshots returned by Codex tools without duplicate copies from the generated-images directory.

## 0.1.15

- 工作区下有多个 Git 仓库时，改动页按仓库分组：每组标题写仓库名、文件数、正在进行的合并或变基、分支和领先落后数，点标题收起；从改动打开完整文件会打开对应仓库里的那个文件。
- 改动列表去掉状态字母，文件名按状态上色。
- 工具时间线里用户发的网址整条显示。
- 按仓库分组需要电脑端 0.1.24，连旧版电脑时照旧只显示一个仓库。

### English

- When a workspace has several Git repositories, the Changes page groups files by repository: each header shows the repository name, file count, a merge or rebase in progress, the branch and ahead/behind counts, and tapping it collapses the group; opening the full file from a change opens the file in that repository.
- The change list drops status letters; file names are colored by status.
- URLs sent by the user are shown in full in the tool timeline.
- Grouping by repository requires Posse 0.1.24 on the Mac; with an older Mac, a single repository is shown as before.

## 0.1.14

- 新建会话只列电脑上能用的 agent（装了、又没在设置里关掉的），默认选第一个；电脑上刚改了开关，打开面板就生效。

### English

- New sessions list only the agents available on the computer (installed and not turned off in Settings), with the first one selected; a switch just changed on the computer takes effect when you open the panel.

## 0.1.13

- 看桌面解锁更安全：密码作为单独的消息发给 Mac，只在锁屏时打进登录窗口，不会落进解锁后的 App；点了解锁后按钮显示「正在解锁…」，连点不会重复发送。
- 连着旧版电脑时不再发密码，提示先更新电脑上的 Posse。
- 记住的密码要确认整串打完、Mac 解开后才存。
- 首页不再显示看过的本轮完成。
- 解锁需要电脑端 0.1.22。

### English

- Safer unlocking in Desktop view: the password goes to the Mac as its own message and is typed into the login window only while the Mac is locked, never into the app in front after unlocking; after tapping Unlock the button shows “Unlocking…” and repeated taps don’t send it again.
- With an older Mac, the password isn’t sent; you’re asked to update Posse on the Mac first.
- A remembered password is saved only after it was fully typed and the Mac unlocked.
- The home screen no longer shows finished turns you’ve already seen.
- Unlocking requires Posse 0.1.22 on the Mac.

## 0.1.12

- 界面支持英文：设置里的「语言」可选跟随系统、简体中文或 English；报错、日期、权限说明都跟着界面语言，电脑发来的报错也按手机的语言显示。
- 钟点跟系统的 12/24 小时设置。
- iOS 推送通知：App 在后台、Mac 没人在用时，agent 等你处理或一轮完成，iPhone 收到通知，点开进到那个会话；在设置里可以分别开关。电脑要配好外网中转。
- 手机终端照电脑窗口的配色画，跟着电脑的深浅主题。
- 和电脑之间的消息改走加密的二进制帧，更省流量；发图片不再整张转 base64。连上旧版电脑时提示先更新电脑。
- 设置「关于」可以手动检查更新。
- 首页恢复显示看过的本轮完成。
- 已连着别的电脑时，添加电脑失败的原因不再被刷新清掉。
- 安卓的包名换了：这一版装上是一个新的 App，要重新配对；旧版不会被替换，确认新版能用后把旧版卸掉。
- 需要电脑端 0.1.21。

### English

- English interface: choose System, 简体中文 or English under Language in Settings; errors, dates and permission prompts follow the interface language, and errors from the Mac are shown in the phone’s language too.
- Times follow the system’s 12/24-hour setting.
- iOS push notifications: while the app is in the background and nobody is using the Mac, your iPhone is notified when an agent needs you or finishes a turn, and tapping it opens that session; each kind can be turned off in Settings. The Mac needs a relay set up.
- Terminals on the phone use the Mac window’s colors and follow its light or dark theme.
- Messages to and from the Mac now travel as encrypted binary frames, using less data; images are no longer converted to base64 before sending. Connecting to an older Mac asks you to update it first.
- You can check for updates manually under About in Settings.
- The home screen shows finished turns you’ve already seen again.
- When already connected to another Mac, the reason adding a Mac failed is no longer cleared by the refresh.
- Android has a new package name: this version installs as a new app and needs pairing again; the old app isn’t replaced, so uninstall it once the new one works.
- Requires Posse 0.1.21 on the Mac.

## 0.1.11

- 文件页的 Markdown 预览照 GitHub 解释 HTML：居中的标题和徽章、折叠内容、图文表格、按键和换行都能正常显示，不再露出标签原文。
- Markdown 里的图片都能显示：网上的图、徽章（SVG）、内嵌的图，以及工作区里按相对路径或根目录写的图；点包在链接里的图打开链接。

### English

- Markdown previews in Files now render HTML the way GitHub does: centered headings and badges, collapsible sections, tables with images, keys and line breaks display properly instead of showing raw tags.
- All images in Markdown now display: web images, badges (SVG), embedded images, and workspace images referenced by relative or root paths; tapping an image wrapped in a link opens the link.

## 0.1.10

- 报错按界面语言显示，不再露出英文原文和错误码。
- 改动页的文件按类型显示文件图标，删除的文件图标变灰，子模块和嵌套仓库显示成目录。

## 0.1.9

- 终端里 Cursor 整段重画时不再闪出对话开头，等这一屏画完再显示。
- 首页新增「项目 | 运行中」切换：「运行中」把所有项目的 agent 会话平铺在一张卡片里，按状态排好。
- 首页的项目和工作区可以点击展开收起，收起时显示里面最需要注意的会话状态。
- 文件页照电脑端按类型显示文件图标。

## 0.1.8

- 项目详情页新增「历史」：只列这个项目目录里的会话，可以搜索、按 agent 筛选，点开或在电脑上继续。

## 0.1.7

- 终端等程序一次画完再显示，Claude Code 重画时不再出现画到一半、整屏上下跳动的情况。
- 打开终端时，第一屏内容到达前显示加载动画，不再空白多等一会儿。
- 首页的项目和工作区换成和电脑侧栏一样的图标，缩进逐级对齐。
- 统一了各页面的颜色、图标和选中样式。

## 0.1.6

- 技能与工具页新增网址一组：列出访问过的网址和网页搜索，可以只看网址。
- 出错的 MCP 调用标成「失败」，不算进次数；技能识别更准（需要电脑端 0.1.15）。
- 电脑端以后新加的记录类型先跳过，不再显示成没有名字的条目。

## 0.1.5

- Markdown 里的 mermaid 代码块画成图，和电脑上一样。
- 看桌面选屏时，排布图上直接写屏幕名称和分辨率，下面不再列一遍。
- 首页显示电脑上自定义的项目图标（需要电脑端 0.1.13）。
- 看过的本轮完成不再显示状态图标，和电脑同步。

## 0.1.4

- 修复从后台回来时偶尔整个界面卡住、点不动的问题。
- 安卓可以扫码配对、看桌面了，和 iPhone 一样。
- 安卓在 App 里就能更新：首页提示新版本，点「更新」下载后系统弹出安装确认，配对不会丢。
- 修复安卓在首页按返回键后再打开会闪退的问题。

## 0.1.3

- 对话页实时显示 agent 正在输出的内容，不用等整段写完；写完后换成正式的排版（需要电脑端 0.1.7）。
- 会话页状态行在延迟后面标出这条连接是「中转」还是「直连」。
- 首页和电脑同步等待任务、启动中、完成未读和未恢复的状态；工作区照电脑侧栏命名，设置里可以隐藏分支和路径。
- 会话页、终端页的「更多」里加了「技能与工具」，和电脑上的面板一样。
- 看桌面改成常驻功能，默认打开、默认用触控板；触控板改用三指拖动，悬浮球里加了「手势」说明。
- 改动页去掉下拉刷新，打开着时每 3 秒自己刷新。
- 更新弹窗说明安装在后台下载，可以在主屏幕看进度。

## 0.1.2

- 看桌面的帧率、码率等读数改成每秒更新一次，不再一秒跳好几下；刚出画面时先显示「— fps」。

## 0.1.1

- App 里提示新版本：首页顶部出现更新提示，点开看更新内容，一键更新；设置「关于」里也能更新。
- 历史页重新设计：搜索框放进顶栏，按 agent 筛选换成一行小标签，分组可以展开收起，默认只展开第一组。
- 手机和电脑深浅色相反时，终端里的暗灰、黄色等文字自动加深或提亮，看得清。
- 上下文圆圈和延迟信号格一样粗；套餐用量面板去掉关闭按钮，电脑列表去掉标题，首页电脑胶囊和卡片左对齐。
