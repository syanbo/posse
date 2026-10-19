# Hive 带的 flterm

pub.dev 上 flterm 0.0.5 的副本（MIT），`pubspec.yaml` 用 `dependency_overrides` 指向这里。只留了 lib、LICENSE、README、CHANGELOG 和 pubspec.yaml（去掉 `resolution: workspace`，它只在上游的 workspace 里成立）。

## 改动

改动处的注释都带 `Hive`。

- `lib/src/widgets/terminal_input_client.dart`：键盘右下角从"换行"改成"发送"（`inputAction: .send`），`performAction` 收到 send 也当回车。终端里回车就是把这一行交给程序，用户要求写"发送"。
- `lib/src/widgets/terminal_gesture_detector.dart`、`terminal_raw_gesture_detector.dart`、`terminal_view.dart`：不是鼠标的指针按下不选字、不聚焦，也不按鼠标拖选发给程序。移动用屏幕坐标量，超过 10px 取消还没成立的长按。停满 500ms 先选中一个词，同一指继续移动是在扩大选区，这段按住滚动位置（拖到边缘的自动滚动照常）。松手后选区留着，交给外层的「复制」。抬手时移动不超过 24px、按下不超过 700ms 才算点击：这时才聚焦，并给正在跟踪鼠标的程序发一次点击。鼠标拖选不动。外层不再用 `onTap` 抢先聚焦。
- `lib/src/widgets/terminal_view.dart`：`initState` 里把控制器当前的屏幕同步给滚动控制器。原版只在控制器变化时同步，接上时已经停在备用屏幕（less 不再输出）的话，滚动按主屏幕夹住，不会换成方向键。
- `lib/src/rendering/terminal_frame_builder.dart`、`atlas/sprite_buffer.dart`、`painters/shaped_run_painter.dart`：连成一串的 ASCII 符号（`----|`）按段落排版画，宽度是字形自己的步进，格子宽度却向上取整到设备像素；字号小时（手机的电脑布局）差出一成多，一长串符号比格子短，后面按格子定位的字和它隔开一截。画的时候按格子宽度横向拉伸。
- `lib/src/rendering/terminal_renderer.dart`：切换主屏幕和备用屏幕时也重新布局。原版只在回滚行数变化时重新布局，切进备用屏幕（Claude Code 的全屏界面）时回滚行数常常不变，滚动范围停在主屏幕的 0..0，要等改尺寸（比如弹一次键盘）才能滑。`terminal_view.dart` 换屏时把滚动换算的基准对齐当前位置，免得第一次滑动发出一大串滚轮。

升级 flterm 时从 pub 缓存重新复制，再把上面的改动补回来。
