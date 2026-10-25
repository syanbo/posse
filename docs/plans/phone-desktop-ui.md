# 看桌面的界面

2026-10 查了五家 iOS 远程桌面的官方说明（触控、工具条、键盘、缩放），对照 `docs/plans/phone-desktop.md` 的直接点按。手势表不改：长按是拖动，不是右键。

## 各家怎么做

**Windows App**（[触控](https://learn.microsoft.com/en-us/windows-app/input-keyboard-mouse-touch-pen)、[连接条](https://learn.microsoft.com/en-us/previous-versions/remote-desktop-client/remote-desktop-ios-ipados)）。直接点按：点哪就是点哪，不画指针；右键是长按。指针模式才用双指轻点当右键，捏合只缩放本地画面。连接条在顶上，按住可拖到顶边或侧边；上面有缩放、键盘、会话名。键盘图标开关系统键盘，键盘出来时平移控件跟着出现。

**Jump Desktop**（[手势](https://support.jumpdesktop.com/hc/en-us/articles/216423503-Getting-Started-Jump-Desktop-Controls-and-Gesture-Reference)、[键盘](https://support.jumpdesktop.com/hc/en-us/articles/216423643-Keyboard-Input)）。点按单击，双击双击，长按或双指轻点右键，点两下按住再拖是左键拖动，双指滑滚动，捏合缩放。iPhone 右上角键盘图标或三指轻点开系统键盘；键盘旁有一排扩展键（方向、功能键、修饰键），可以滑着换组。另有触控板、笔、直接点按三种配置。虚拟显示器跟设备分辨率走，转屏跟着转。

**Screens 5**（[光标和手势](https://help.edovia.com/en/screens-5/features/cursor-control-modes-and-other-gestures)）。默认直接点按，也可改触控板。双指双击藏起底部工具条进全屏，捏合缩放本地画面。横屏有更矮的紧凑工具条。从屏幕边缘直线滑是把远程光标甩到那一边（Dock、菜单栏），不是系统手势。键盘开着时，向上的边缘滑改在快捷键条上做。

**Chrome Remote Desktop**（[iOS 菜单](https://www.techsolutions.support.com/how-to/how-to-use-chrome-remote-desktop-12717)）。底角菜单里开关键盘，并在触控板和直接点按之间切。直接点按：点在控件上、长按右键、双指滑滚动、捏合缩放、按住再拖。触控板：单指移动光标，双指轻点右键，三指轻点中键。三指下滑出工具条，四指点出菜单（版本之间不一致）。

**Parsec**（官方客户端偏游戏推流，[沉浸模式](https://support.parsec.app/hc/en-us/articles/32361385571860-Immersive-Mode-Setting) 是把键鼠锁给主机；触控说明以 [OpenParsec](https://github.com/hugeBlack/OpenParsec) 为准）。设置里选触控板或直接点按。三指轻点出键盘。浮层里二指可以改成滚动或缩放。单指点左键，双指点右键，按住拖动。浮层按钮可以整个关掉。没有给办公准备的连接条。

## 采用

| 做法 | 来自 | 为什么 |
| --- | --- | --- |
| 顶上一条小胶囊，按住沿顶边拖 | Windows App 连接条 | 手机窄，侧边一条会挡住竖屏的画面。平时只留信号和帧率，点开才是按钮 |
| 几秒不碰自己收起 | 没有一家这么做 | 连接条不收的话，七个按钮一直占着顶上一行。4 秒不碰收起 |
| 快捷键栏默认不出现，系统键盘弹出时贴在键盘上 | Jump 的扩展键行 | 现在的栏一直占着底部。键盘出来时修饰键和方向键要够得着 |
| 横屏用按钮锁住，不跟设备自由转 | 自己的 | Jump 是整页跟着转。Posse 其余页面是竖的，离开看桌面要回到原来允许的方向 |
| 横屏栏更矮、半透明 | Screens 的紧凑条 | 横屏高度不够，不透明的 40 再加安全区会吃掉一条画面 |
| 长按拖动、右键给轻触感 | iOS 长按的习惯 | 直接点按没有指针，进入拖动和右键没有别的反馈 |
| 第一次进页面给一张手势说明，点一下关掉 | Jump 有手势表 | 单指拖是平移画面、双指轻点是右键，和系统手势相反，只说一次 |
| 顶边和底边系统手势延后 | 各家全屏都会撞上主屏幕指示条 | 边缘拖动和回主屏幕、通知中心抢同一下 |

不采用：Windows 直接点按里「长按即右键」（我们的长按是拖窗口）；Jump 的三指开键盘、Chrome 的三指或四指叫菜单（和系统手势、我们的双指定性抢）；Parsec 那种常驻浮层按钮；触控板模式（计划里留到后面）。

## 这一版

- 竖屏胶囊在顶边，可拖到安全区里的任意位置。横屏改贴没有刘海的那一侧（左右一样时靠右），竖着排，可上下拖；收起是窄条，不挡菜单栏和画面中间，展开后图标也竖着排。横竖屏各记各的位置，不写入设置，离开页面回到居中。收起是信号格（和 `LatencyLabel` 同一套：150 ms 内满格绿，300 ms 内两格灰，再慢一格琥珀，没响应红色；还没测到是淡的空格，不当成满格）加帧率。展开是返回、横竖屏、键盘、快捷键、回到 1 倍、调试、断开。返回离开页面，左缘拖动留给画面，不触发页面返回。断开停掉这一路画面并留在页上，已经停了再点是重新连接。调试打开原来的读数（帧率、码率、丢包、输入），里面可以改连接地址。
- 横屏锁住两个横屏方向，再点按当前窗口朝向锁到另一边，不按上次记下的值取反。记在 `AppSettings.deskLandscape`。进来就按记住的锁。离开时传空列表，由 Info.plist 决定方向（空列表在 iOS 上是全部方向，再和 Info.plist 取交集；iPad 保留倒置竖屏）。横屏画面铺满安全区宽度，左右让开刘海，上下仍按比例留空。
- 快捷键栏在「自己打开」或「系统键盘开着」时出现，贴在键盘上方。横屏高度 32、底色半透明。修饰键仍是点一下只管下一个键、长按锁住。
- 长按满 300 ms 和双指右键各一次轻触感。
- 手势提示只出现一次，点任意处关掉，记在 `deskGesturesSeen`。
- 看桌面时 `preferredScreenEdgesDeferringSystemGestures` 为顶边和底边，离开页面清空。根控制器是 `PosseFlutterViewController`，Flutter 没有这个开关。
