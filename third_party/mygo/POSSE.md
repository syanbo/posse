# Posse 带的 mygo

github.com/egoist/mygo v0.6.3 的副本，go.mod 用 replace 指向这里。去掉了 website、examples、docs、测试文件和 testdata 目录，其余原样。

## 改动

改动处的注释都带 `Posse`。

`ui/editor` 的 diff 查看器：

- `diffview.go`：加 `DiffState.IgnoreWhitespace`。
- `diffmodel.go`：开着它时比较前去掉每行的空白（`cmpLines`），只差空白的行算没改；两边没改的段行数仍然相等，左右对照照常对齐。显示、行内高亮和回滚用的仍是原文。没有做"忽略空行"：把只增删空行的段去掉会让两边对不齐。

- `diffview.go`、`diffside.go`：加 `DiffState.RenderHunkMark`，左右对照时在旧版一侧的右边缘（挨着分隔线）给每处改动放一个按钮（`hunkTop` 找这处改动在旧版一侧的位置，没有行时是占位块），Posse 画成 WebStorm 的 `>>`；加 `DiffState.OldNumbersRight`，左右对照时旧版一侧的行号栏放到右边缘，两列行号挨着分隔线，`>>` 在行号左边（编辑器选项 `gutterRight`、`gutterExtra`，`editor.go` 的 `textLeft`、`rightGutter`、`inGutter`，`paint.go`、`input.go` 里原来按行号栏在左边算的地方改用它们）。
- `editor.go`、`input.go`、`paint.go`：左右对照的横向滚动加一个两边共用的位置（`sharedScroll.x`），上限取两边里最远的（`maxX`），每边按自己的上限截；唯一的横向滚动条（新版一侧）、滚轮和光标跟随都改它（`sideways`、`setScrollX`）。旧版行号在右边时文字区更窄，原来新版滚到头时旧版行尾还压在行号栏下面；只有旧版超宽时新版也显示横向滚动条。
- `diffview.go`：加 `DiffState.Split`、`SplitDragging`，左右对照时旧版一侧占的宽度比例（0 是一半），分隔线上盖一条 5 DIP 宽的拖动条（`split-grip`，按上一帧的宽度摆），拖动改比例、限制在 0.15 到 0.85，拖动中 `SplitDragging` 为真，应用在松手后保存。
- `diffahead.go`、`diffmodel.go`：加 `CompareTexts`（任何协程都能调，和 `DiffState` 一样比较两段文字）和 `DiffState.UseComparison`：下一次整体比较时两边文字和忽略空白的设置都和算过的一样，就直接用结果（`diffModel.takeAhead`），否则照常比较。Posse 在读文件的协程里先比好，几 MB 的文件不卡界面线程。
- `diffview.go`、`diffside.go`：加 `DiffState.Current`（改动的序号加一，0 表示没有），这一处的底色和行号栏比别的改动深、竖条总是显示，F7 上下切换时看得出走到了哪。

`ui/editor` 的编辑器（`gitdiff.go`、`input.go`、`paint.go`、`state.go`）：加 `State.OnChangeClick`，点行号栏里相对 DiffBase 的改动色条时回调（改动序号和这一行底边的位置），色条的位置抽成 `changeBarX` 和绘制共用；加 `State.Changes`（改动列表和改之前的内容）、`State.RevertChange`（回滚一处，可以撤销）、`State.LineBottom`（某一行底边相对编辑器顶的位置）。Posse 用它们做 WebStorm 那样的改动卡片。

`ui/editor` 的跳到定义（`state.go`、`editor.go`、`input.go`、`build.go`、`paint.go`）：加 `State.LinkAt`，按住 Command 时指针下要画下划线的范围（Posse 用它认引号里的路径和网址），没设或返回空范围时是指针下的词；`pointerMove` 记下这段（`link`），`build` 在 Command 按住、指针在文字上、设了 `OnDefinition` 时把指针换成手形（`linkOn`），`paint` 在这段下面画一条 1 DIP 的线，照 Zed 按住 Command 时的链接样式。

`ui/editor` 的粗体和字体平滑（`state.go`、`diffview.go`、`editor.go`、`paint.go`、`view.go`）：上游主题里给 Markdown 标题、加粗标了 `Bold` 却不画；加 `State.BoldWeight`、`DiffState.BoldWeight`（为 0 时 700），`paintRow` 把这些记号空出来，另用粗体字重排字（`boldShaped`）画在原位（等宽字体，宽度不变）；`Font.Antialiased` 开着时不用字体平滑画字（`setStyle` 记下它，它不进排版的 `text.Style`）。`ui/glyphs.go` 为此加 `Painter.TextLineWith`：可以不平滑，颜色透明的字不画。

`ui/headless.go`：加 `Tester.MoveWith`、`Tester.ScrollWith`，带修饰键移动指针、滚动（平台每个指针事件都带当前修饰键，`Move` 不带会把修饰键清掉）。

`ui/widgets.go`：对话框（`coreModal`）打开时遮罩淡入、对话框淡入并从 96% 放大，弹出面板（`stylePanel`：Popover、Select、ComboBox、日期、取色）淡入并下落 4 DIP，都用 `Keyframes` 播 160 ms，只在出现时播一次。

`ui/input.go`、`ui/context.go`、`ui/runtime.go`、`ui/api_gen.go`：加 `Element.MiddleClicked`（中键点击，Posse 用来关标签），`ui/headless.go` 加 `Tester.MiddleClickAt`。

`ui/theme.go`、`ui/runtime.go`（`updateCursor`）：加 `Theme.PointerCursor`，开着时能点的元素（没有禁用）显示手形指针、窗口内拖动值时显示抓握的手，照网页的习惯；mygo 默认照原生控件用箭头。只为挡住点击才标成可点的对话框遮罩和面板、弹出面板（`base.go`、`combobox.go`）明确设成箭头。

`ui/theme.go`、`ui/paint.go`（`textForeground`）：加 `Theme.FlatText`，开着时元素的文字不用字体平滑画（和 `Font.Antialiased` 一样）。

`ui/theme.go`、`ui/context.go`（`weightFrom`）：加 `Theme.FontWeight`，大于 0 时设在窗口的根节点上，没设字重的文字都继承它；mygo 默认不设，字体自己的常规字重。

`ui/menu.go`、`ui/runtime.go`：加 `Context.PresentMenus`，设了就不弹系统菜单，把菜单项（`MenuEntry`）、位置和选中回调交给应用自己画；右键菜单、菜单按钮、文字输入框和编辑器的编辑菜单都走它。

`ui/menu.go`、`ui/api_gen.go`：加 `Element.ContextMenuAsked`，元素照 `ContextMenu` 接右键、菜单键和 Shift+F10（文字选区也一样），但不弹系统菜单，下一帧报告位置（窗口坐标），Posse 自己画菜单。

`ui/frame.go`：加 `Context.ModalOpen`，报告上一帧有没有对话框在最上层；Posse 的菜单项（⌘W 关闭标签）在帧外触发，用它和快捷键一样在对话框打开时不动作。

`internal/gpu/metal/metal.go`：窗口的 drawable 最多 2 个（上游是 3 个）。上游在窗口 2 秒没画东西后才还掉多的那个，Posse 的窗口一直在画（终端光标闪烁、状态栏每秒刷新），第三个一直占着，约 30 MB。

终端插件 `plugins/terminal`（新加的方法放在 `posse.go`，升级时少冲突）：

- `links.go`：网址和路径遇到全角标点就结束（`fullWidthPunct`），`~/a.md)，然后` 这样紧跟着中文的也认得出；`trimURL` 也去掉结尾的中文标点（"。，）"等）。
- `terminal.go`：加 `Options.LinkUsable`，链接打不开（文件已不存在）时不显示下划线、点击无效；加 `Options.OnHoverLink`，悬停的链接变化时通知（Posse 用来预览图片）；加 `Options.LinkClick`，设了就代替 `OpenLink`，多告诉点击时按着的修饰键（Posse 单击网址弹卡片、Command+点击在 Posse 里打开、Shift+Command+点击交给浏览器）；加 `Options.ContextMenu`，设了就不弹菜单，把右键的位置交给 Posse 自己画菜单；加 `Options.OnText`（`view.go` 的 `typed`），打进来的文字先给它看，它拿走的不发给程序，那个键的松开也一起吞掉（Posse 用来把 ⌘F 之后、查找框还没拿到焦点时打的字接进搜索词）。
- `view.go`：
  - 上游只在按住 Command 时给链接画下划线、换手形指针（`hover`、`linkUnder`）；Posse 改成不按 Command 也这样（`hoverUnder`、`setHover`），下划线沿用上游的画法。
  - 在链接上按下、没有拖动就松开，打开链接（`linkDown`）；拖动超过 4 DIP 照常选文字。Command+点击照旧。程序接管鼠标（鼠标跟踪）时，链接上的这次按下不交给程序，而是开始选区，之后的移动和松开跟着选区走，拖动照样能选中文字。
- `font.go`：加 `Font.BoldWeight`，粗体的字重可以单独设；为 0 时照上游比正文重 300、至少 700。加 `Font.Antialiased`，照 `ui.Font.Antialiased` 不用字体平滑画字：Core Text 的平滑让深色底上的浅色字看上去重一档。
- `terminal.go`、`view.go`：加 `Options.NoWheelZoom`，开着时按住 Command 滚动滚轮或在触控板上滑动照常滚动，不缩放字号；调字号的快捷键（⌘=、⌘-、⌘0）不受影响。
- `posse.go`：`Terminal.Selection`、`SelectAll`、`PasteClipboard`（照常经过 `OnPaste`）给 Posse 自己的右键菜单用；`ScrollToEdge`、`AltScreen` 给 Posse 的 ⌘↑/⌘↓ 和编辑键用（视图把带 Command 的键都交给 app）。

`updater.go`、`updater_stage.go`：`Update.Install` 把下载和换包放在一步。拆出 `stageUpdate`、`swapUpdate`（`Install` 照旧用它们），加 `Update.Stage`（下载、校验、解压到 app 旁边的 `.<名字>.app.update-*`，app 不动）、`Staged.Apply`（换包）、`Staged.Discard`、`Updater.CleanStaged`（清掉上次留下的暂存和换包残留）和 `Updater.Configured`（这个包带不带更新地址）。macOS 上包被换掉后，进程还没用过的隐私授权（文稿、桌面等）会失效，常驻进程也从同一个包运行，所以 Posse 后台只暂存，用户重启时才换包。

## 升级 mygo

用三方合并，不手工对着改：

1. `go mod download -json github.com/egoist/mygo@<旧版本>` 和 `@<新版本>`，拿到模块缓存里的两份源码。
2. 在临时目录建一个 git 仓库：旧版本按上面的规则去掉目录后提交成 `base`；从它分出 `posse` 分支，换成这里的内容；再从 `base` 分出 `up` 分支，换成新版本（同样去掉目录）。
3. 在 `posse` 分支上 `git merge up`（`merge.conflictstyle diff3` 看得到 base），解决冲突。上游自己做了同样的事时，用上游的、删掉这里的改动。
4. 合并结果拷回这里，改 go.mod 的版本，`go mod tidy`。逐个文件确认 `git diff base posse` 里加的行都还在（除了有意去掉的）。

然后跑 `go vet ./... && go test ./... && go tool mygo vet`，生成离屏预览和升级前的逐张比对（`POSSE_PREVIEW_DIR`），打包后用 `footprint` 比较窗口的内存。

v0.5.5 起 `ui.Tester` 有自己的时钟，只有 `Advance` 才走：动画、`c.After` 不再随真实时间推进。界面测试里等待用 `until`、`settle`、`pass`，它们让真实时间和 Tester 的时钟一起走。
