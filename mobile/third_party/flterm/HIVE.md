# Hive 带的 flterm

pub.dev 上 flterm 0.0.5 的副本（MIT），`pubspec.yaml` 用 `dependency_overrides` 指向这里。只留了 lib、LICENSE、README、CHANGELOG 和 pubspec.yaml（去掉 `resolution: workspace`，它只在上游的 workspace 里成立）。

## 改动

改动处的注释都带 `Hive`。

- `lib/src/widgets/terminal_input_client.dart`：键盘右下角从"换行"改成"发送"（`inputAction: .send`），`performAction` 收到 send 也当回车。终端里回车就是把这一行交给程序，用户要求写"发送"。
- `lib/src/widgets/terminal_gesture_detector.dart`、`terminal_raw_gesture_detector.dart`、`terminal_view.dart`：不是鼠标的指针按下不选字、不聚焦，也不按鼠标拖选发给程序。移动用屏幕坐标量，超过 10px 取消还没成立的长按。停满 500ms 先选中一个词，同一指继续移动是在扩大选区，这段按住滚动位置（拖到边缘的自动滚动照常）。松手后选区留着，交给外层的「复制」。抬手时移动不超过 24px、按下不超过 700ms 才算点击：这时才聚焦，并给正在跟踪鼠标的程序发一次点击。鼠标拖选不动。外层不再用 `onTap` 抢先聚焦。
- `lib/src/widgets/terminal_view.dart`：`initState` 里把控制器当前的屏幕同步给滚动控制器。原版只在控制器变化时同步，接上时已经停在备用屏幕（less 不再输出）的话，滚动按主屏幕夹住，不会换成方向键。
- `lib/src/rendering/terminal_frame_builder.dart`、`atlas/sprite_buffer.dart`、`painters/shaped_run_painter.dart`：连成一串的 ASCII 符号（`----|`）按段落排版画，宽度是字形自己的步进，格子宽度却向上取整到设备像素；字号小时（手机的电脑布局）差出一成多，一长串符号比格子短，后面按格子定位的字和它隔开一截。画的时候按格子宽度横向拉伸。
- `lib/src/rendering/terminal_renderer.dart`：切换主屏幕和备用屏幕时也重新布局。原版只在回滚行数变化时重新布局，切进备用屏幕（Claude Code 的全屏界面）时回滚行数常常不变，滚动范围停在主屏幕的 0..0，要等改尺寸（比如弹一次键盘）才能滑。`terminal_view.dart` 换屏时把滚动换算的基准对齐当前位置，免得第一次滑动发出一大串滚轮。

- `lib/src/rendering/terminal_frame_builder.dart`、`terminal_render_pipeline.dart`、`terminal_renderer.dart`、`widgets/terminal_view.dart`：格子外面的底色照 Ghostty 的 `window-padding-color = extend` 画。出帧时记下每行最左、最右一格的底色（`RowEdgeColors`），渲染盒画完格子后把铺不满的零头和 `TerminalView.backgroundBleed` 那一圈按它补上：左右按每行两端，上下按第一行、最后一行（两端同色才延伸）。没设底色的格子还是主题色。grok 给每一格都铺 `#141414`、不查终端底色，原版外面露出一圈主题色，像套了个框。

- `lib/src/widgets/terminal_controller_impl.dart`：回答终端的尺寸查询（CSI 14t/16t/18t）用布局时记下的行列数，不再调控制器自己那份 `RenderState.update`。update 会清掉终端上的行脏标记，渲染器自己的 RenderState 就看不到这一批输出改了哪些行；Claude Code 每次改尺寸重画都带一个 CSI 16t，弹出、收起键盘或从后台回来之后，重画里不再变的行（分隔线、状态栏）画面上一直是旧的（缺行或叠成两份），内核里却是对的。
- `lib/src/rendering/terminal_frame_builder.dart`、`terminal_renderer.dart`、`widgets/terminal_view.dart`：出帧时记下每行有没有内容（有字或程序铺了底色，记在 `RowEdgeColors`），画完一帧把"到第几行为止有内容"（最后一行有内容的行和看得见的光标所在行取靠下的）和上次比，变了在帧末经 `TerminalView.onContentRows` 报出来。手机弹键盘时终端不改尺寸，按它决定往上挪多少；不用 libghostty 的 RenderState 取光标，它的 update 会清掉行脏标记。
- `lib/src/widgets/terminal_view.dart`、`terminal_gesture_detector.dart`：加 `TerminalView.onTap`。手指点击成立（不是链接）时调用，设了就不再自己聚焦，弹不弹键盘交给外层：手机终端页点终端时键盘收着就弹出，弹着不动。
- `lib/src/widgets/terminal_view.dart`、`terminal_view_binding.dart`、`terminal_controller_impl.dart`：全屏程序跟踪鼠标时，手指滑动换成的滚轮报在手指所在的格子（`TerminalView` 记下最近一次按下、移动的位置，`handleScroll` 带上它；还没有时报在屏幕中间）。原版报在左上角，Codex 按位置分区处理滚轮，第一行是钉住的问题，滚轮落在那里不滚，手机上 Codex 滑不动。
- `lib/src/widgets/terminal_controller_impl.dart`：全屏程序不跟踪鼠标时，滑动只在开着 alternate scroll（1007）时换成方向键。原版不看这个模式，Codex 关掉 1007 也照发，方向键在它的输入框里是翻历史。
- `lib/src/widgets/terminal_controller.dart`、`terminal_controller_impl.dart`：加 `selectionAnchor` 和 `selectionEnds`，报告选区在可见屏幕上的位置；`moveSelectionEnd` 拖一端时另一端用屏幕坐标钉住，`nudgeViewport` 让拖到边缘时回滚跟着走。选区端点落在宽字符（中文占两格）中间时扩成整字，高亮不会切半个字。`selectParagraph` 把选区扩到上下空行之间的这一段。手机在选区两端画把手，「复制」贴在选区上沿，全选走这一段而不是整屏回滚。
- `lib/src/widgets/terminal_gesture_detector.dart`：长按选中词时不震动。
- `lib/src/widgets/terminal_input_client.dart`、`terminal_controller_impl.dart`、`terminal_controller.dart`：一次插进好几个字又带换行的（系统菜单粘贴、听写）交给 `onPasted` 当粘贴，不拆成字和回车；`paste` 去掉末尾的换行（`trimPastedNewlines`，导出给外层复制用），粘贴完不直接回车发出去。

升级 flterm 时从 pub 缓存重新复制，再把上面的改动补回来。
