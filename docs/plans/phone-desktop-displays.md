# 看桌面：多块屏

2026-10 对照 RustDesk（`github.com/rustdesk/rustdesk` 1297da6，手机端同样是 Flutter）定方案。

## RustDesk 怎么做

- 协议：登录回复里带全部屏（`PeerInfo.displays`：x、y、宽、高、名字、缩放，加 `current_display`）。控制端发 `SwitchDisplay{display}`，被控端换采集服务后回一条 `SwitchDisplay`，带新屏的位置、尺寸和分辨率（`libs/base/protos/message.proto`）。
- 手机端：远程页「选项」弹框顶上一排编号方块 1、2、3，当前那块高亮，点了就切（`flutter/lib/mobile/pages/remote_page.dart` 的 `showOptions`）。桌面端工具栏的图标按真实排布画出几块屏的相对位置和大小（`globalMonitorsWidget`）。
- 记住选择：`lastUserDisplay`；重连后等画面出来再切回上次选的屏（先切会因为屏大小不同而错位），1.5 秒没出画面也切（`flutter/lib/models/model.dart` 的 `pendingMonitorRestore`）。上次的屏不在了用主屏。
- 跟随：「跟随远程鼠标」「跟随远程焦点窗口」两个开关。被控端发现鼠标进了另一块屏、或焦点窗口换到另一块屏时，只发 `follow_current_display` 建议，由控制端去切（`src/server/connection.rs` 的 `handle_cursor_switch_display`、`src/server/input_service.rs` 的 `run_window_focus`）。
- 切屏前清掉旧画面，免得新屏开头先闪一下上一块屏的最后一帧（`openMonitorInTheSameTab`）。
- 鼠标坐标用全局坐标（加上当前屏的原点），不是相对当前屏。
- 另有「所有屏一起看」「每块屏一个窗口」，是桌面端的；手机上太小，不采用。

## 采用

| 做法 | 来自 | 怎么落 |
| --- | --- | --- |
| 屏列表随订阅给出，排布、名字、分辨率都带上 | PeerInfo.displays | 采集子进程发新帧类型 `KindDisplays`（JSON），常驻进程转成 `desk.subscribe` 流里的 `{type:"displays"}`；拔插、改分辨率后重发 |
| 按真实排布画缩略图选屏 | 桌面端工具栏 | 悬浮球里多一个「屏幕」按钮（只有一块屏时不出现），点开是排布图加列表 |
| 记住上次看的屏，重连回到它 | lastUserDisplay | 手机按电脑记住屏 ID（CGDirectDisplayID 同一台显示器重启后不变），订阅时带上；不在了用主屏。我们是订阅时就指定，不用等画面出来再切 |
| 切屏时不闪旧画面 | clearImage | 新屏的第一个关键帧来之前画面保持不动，缩放回到 1 倍 |
| 跟随 | follow_remote_cursor / window | 第二步再做。Mac 自己判断、自己切，不绕回手机 |

不采用：全局坐标。我们的输入是相对当前画面的 0–1，切屏那一刻子进程同时换掉注入用的屏范围；切屏是用户在菜单里点的，那几百毫秒里手不在画面上，不值得为它改输入协议。

## 这一版

1. 子进程：`--display <id>` 指定开始看哪块屏（不在就主屏）；控制消息 `CtrlDisplay` 切到别的屏；注入、滚动按当前看的屏换算；拔插、改分辨率时还在就留在这块屏，不在了回主屏；每次变化发 `KindDisplays`。
2. 常驻进程：`KindDisplays` 转给手机；`desk.display {id}` 写 `CtrlDisplay`；`DeskRequest.Display` 传给子进程。
3. 手机：悬浮球的「屏幕」按钮和选屏面板；按电脑记住选的屏，订阅时带上。
