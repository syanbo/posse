# 手机端更新内容

每个版本一节，标题是 `## <版本>`：发布 iOS、安卓安装包时这一节写进 latest.json 和安装页，App 首页提示更新时显示它。
一节里先写中文条目，再在 `### English` 小节里写对应的英文条目：中文条目写进 latest.json 的 `notes` 和安装页，英文条目写进 `notes_en`。

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
