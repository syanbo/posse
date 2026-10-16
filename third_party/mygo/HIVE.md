# Hive 带的 mygo

github.com/egoist/mygo v0.4.8 的副本，go.mod 用 replace 指向这里。去掉了 website、examples、docs 和测试文件，其余原样。

## 改动

改动处的注释都带 `Hive`。

`ui/editor` 的 diff 查看器：

- `diffview.go`：加 `DiffState.IgnoreWhitespace`。
- `diffmodel.go`：开着它时比较前去掉每行的空白（`cmpLines`），只差空白的行算没改；两边没改的段行数仍然相等，左右对照照常对齐。显示、行内高亮和回滚用的仍是原文。没有做"忽略空行"：把只增删空行的段去掉会让两边对不齐。

- `diffview.go`、`diffside.go`：加 `DiffState.Current`（改动的序号加一，0 表示没有），这一处的底色和行号栏比别的改动深、竖条总是显示，F7 上下切换时看得出走到了哪。

终端插件 `plugins/terminal`：

- `links.go`：加 `spanAt`，返回链接在一行里覆盖的列；`trimURL` 也去掉结尾的中文标点（"。，）"等）。
- `terminal.go`：加 `Options.LinkUsable`，链接打不开（文件已不存在）时不显示下划线、点击无效；加 `Options.OnHoverLink`，悬停的链接变化时通知（Hive 用来预览图片）。
- `view.go`：
  - 指针在链接上时（不必按 Command）显示手形指针，并记下链接的范围（`hover`）。
  - 在链接上按下、没有拖动就松开，打开链接（`linkDown`）；拖动超过 4 DIP 照常选文字。Command+点击照旧。
  - 程序接管鼠标时，点在链接上的这一下不交给程序。
- `paint.go`：在悬停的链接下面画一条单下划线。

## 升级 mygo

换新版本时重新复制并去掉同样的目录，再把上面的改动合进去（搜 `Hive`），然后跑 `go test ./internal/app -run TestClickOpensLinkCard` 和 `go test ./internal/gitx`。
