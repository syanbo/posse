# Hive 带的 mygo

github.com/egoist/mygo v0.4.8 的副本，go.mod 用 replace 指向这里。去掉了 website、examples、docs 和测试文件，其余原样。

## 改动

改动处的注释都带 `Hive`。

`ui/editor` 的 diff 查看器：

- `diffview.go`：加 `DiffState.IgnoreWhitespace`。
- `diffmodel.go`：开着它时比较前去掉每行的空白（`cmpLines`），只差空白的行算没改；两边没改的段行数仍然相等，左右对照照常对齐。显示、行内高亮和回滚用的仍是原文。没有做"忽略空行"：把只增删空行的段去掉会让两边对不齐。

- `diffview.go`、`diffside.go`：加 `DiffState.RenderHunkMark`，左右对照时在旧版一侧的右边缘（挨着分隔线）给每处改动放一个按钮（`hunkTop` 找这处改动在旧版一侧的位置，没有行时是占位块），Hive 画成 WebStorm 的 `>>`；加 `DiffState.OldNumbersRight`，左右对照时旧版一侧的行号栏放到右边缘，两列行号挨着分隔线，`>>` 在行号左边（编辑器选项 `gutterRight`、`gutterExtra`，`editor.go` 的 `textLeft`、`rightGutter`、`inGutter`，`paint.go`、`input.go` 里原来按行号栏在左边算的地方改用它们）。
- `diffview.go`、`diffside.go`：加 `DiffState.Current`（改动的序号加一，0 表示没有），这一处的底色和行号栏比别的改动深、竖条总是显示，F7 上下切换时看得出走到了哪。

`ui/editor` 的编辑器（`gitdiff.go`、`input.go`、`paint.go`、`state.go`）：加 `State.OnChangeClick`，点行号栏里相对 DiffBase 的改动色条时回调（改动序号和这一行底边的位置），色条的位置抽成 `changeBarX` 和绘制共用；加 `State.Changes`（改动列表和改之前的内容）、`State.RevertChange`（回滚一处，可以撤销）、`State.LineBottom`（某一行底边相对编辑器顶的位置）。Hive 用它们做 WebStorm 那样的改动卡片。

`ui/widgets.go`：对话框（`coreModal`）打开时遮罩淡入、对话框淡入并从 96% 放大，弹出面板（`stylePanel`：Popover、Select、ComboBox、日期、取色）淡入并下落 4 DIP，都用 `Keyframes` 播 160 ms，只在出现时播一次。

`ui/input.go`、`ui/context.go`、`ui/runtime.go`、`ui/api_gen.go`：加 `Element.MiddleClicked`（中键点击，Hive 用来关标签），`ui/headless.go` 加 `Tester.MiddleClickAt`。

`ui/theme.go`、`ui/runtime.go`（`updateCursor`）：加 `Theme.PointerCursor`，开着时能点的元素（没有禁用）显示手形指针、窗口内拖动值时显示抓握的手，照网页的习惯；mygo 默认照原生控件用箭头。只为挡住点击才标成可点的对话框遮罩和面板、弹出面板（`base.go`、`combobox.go`）明确设成箭头。

`ui/menu.go`、`ui/runtime.go`：加 `Context.PresentMenus`，设了就不弹系统菜单，把菜单项（`MenuEntry`）、位置和选中回调交给应用自己画；右键菜单、菜单按钮、文字输入框和编辑器的编辑菜单都走它。

`ui/menu.go`、`ui/api_gen.go`：加 `Element.ContextMenuAsked`，元素照 `ContextMenu` 接右键、菜单键和 Shift+F10（文字选区也一样），但不弹系统菜单，下一帧报告位置（窗口坐标），Hive 自己画菜单。

`ui/frame.go`：加 `Context.ModalOpen`，报告上一帧有没有对话框在最上层；Hive 的菜单项（⌘W 关闭标签）在帧外触发，用它和快捷键一样在对话框打开时不动作。

`internal/text/coretext_darwin.go`（`line`、`widenTo`）：Core Text 在中文和紧挨着的英文之间（如"狗粮icon"）自动加的间距只在字形位置里、不在 advance 里，mygo 按 advance 量宽度，单行文字按量出的宽度排版时放不下、末尾被省略。从左到右的段里把这段间距补进前一个字形的 advance；advance 为 0 的附加符号（重音、泰文标记）不补。

终端插件 `plugins/terminal`：

- `links.go`：加 `spanAt`，返回链接在一行里覆盖的列；`trimURL` 也去掉结尾的中文标点（"。，）"等）；网址和路径遇到全角标点就结束（`fullWidthPunct`），`~/a.md)，然后` 这样紧跟着中文的也认得出。
- `terminal.go`：加 `Options.LinkUsable`，链接打不开（文件已不存在）时不显示下划线、点击无效；加 `Options.OnHoverLink`，悬停的链接变化时通知（Hive 用来预览图片）。
- `view.go`：
  - 指针在链接上时（不必按 Command）显示手形指针，并记下链接的范围（`hover`）。
  - 在链接上按下、没有拖动就松开，打开链接（`linkDown`）；拖动超过 4 DIP 照常选文字。Command+点击照旧。
  - 程序接管鼠标时，点在链接上的这一下不交给程序。
- `paint.go`：在悬停的链接下面画一条单下划线，颜色取链接第一个字的颜色（`textColorAt`）。
- `terminal.go`、`view.go`：加 `Options.ContextMenu`，设了就不弹菜单，把右键的位置交给 Hive 自己画菜单。
- `terminal.go`：加 `Options.Menu`（换掉插件自带的英文右键菜单）和给它用的 `Terminal.Selection`、`SelectAll`、`PasteClipboard`（照常经过 `OnPaste`）。
- `terminal.go`：加 `Terminal.ScrollToEdge`、`AltScreen`，给 Hive 的 ⌘↑/⌘↓ 和编辑键用（视图把带 Command 的键都交给 app）。

## 升级 mygo

换新版本时重新复制并去掉同样的目录，再把上面的改动合进去（搜 `Hive`），然后跑 `go test ./internal/app -run TestClickOpensLinkCard` 和 `go test ./internal/gitx`。
