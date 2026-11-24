# 双端内置 Tailscale（`tsnet`）方案

在 Posse 的 Mac 常驻进程和手机 App（`posse_quic` Go 库）两端同时嵌入 Tailscale 用户态节点（`tailscale.com/tsnet`），默认关闭、按需懒启动。

## 目标与定位

- **打破手机单 VPN 槽位限制**：iOS / Android 同时只能运行一个系统 VPN，用户挂 Surge / Shadowrocket / Clash 时无法开启官方 Tailscale App。进程内 `tsnet` 不占系统 VPN 槽位。
- **零公网 VPS 门槛**：无需部署自建 `deskrelay` 中转服务器，也不用 Mac 和手机另装官方 Tailscale App；各自登录同一个 Tailscale（或自建 Headscale）账号即可互通。
- **与 `deskrelay` 互补而非替代**：离线 iOS APNs 推送、国内自建低延迟中转仍由 `deskrelay` 提供；两者并存时自动按直连优先、中转优先于 DERP 的原则选路。
- **兼容官方 Tailscale**：支持双端内置、Mac 官方 + 手机内置、Mac 内置 + 手机官方三种组合互通：
  - **Mac 官方 + 手机内置**：Mac 的 `LocalEndpoints()` 照旧将本机 `100.64/10` 放入配对码与 `posse.quic`；手机内置 `tsnet` 进入 `Running` 后，从 `offer.dialUrls` 与 `quicUrls` 中的 `100.64/10` 主机自动派生 `tsquic://` 候选参与竞速。
  - **Mac 内置 + 手机官方**：Mac 的内置 `tsnet` 同时监听同端口的 TCP WebSocket（`tslink.Node.ListenTCP(port)` 接入现有 `http.Server`）和 UDP QUIC（`tslink.Node.ListenPacket(port)` 接入 `acceptQUIC`），并将内置 `100.64/10` 放进配对码和 `posse.quic`；手机只开官方 Tailscale 时首次扫码即可直接经 `ws://100.x:port` 完成配对握手并升级到 `hquic://100.x:port`（以及看桌面直连 `100.x:<deskPort>`），无需开启手机内置 `tsnet`。

## 构建与体积策略

- 仓库根 `go.mod` 升级至 `go 1.27.4` 并引入 `tailscale.com@v1.106.0`（MyGo 要求 1.27.3+，无锁死），Mac 端与手机端共用同一套依赖与共享包 `internal/tslink`，避免嵌套模块和双工具链分叉。
- 统一使用 `tailscale.com/cmd/featuretags` 生成的裁剪构建标签：
  - **macOS / iOS**：`--min --add=netstack,nattraversal,cachenetmap,useproxy`
  - **Android**：`--min --add=netstack,nattraversal,cachenetmap,useproxy,androidbin`
- 关键约束：
  - 必须包含 `nattraversal`（STUN / disco 打洞），否则只能走 DERP；
  - 必须保持 `ts_omit_logtail`，禁止向 `log.tailscale.com` 上传节点日志；
  - 保持 `ts_omit_ipnbus`，状态读取统一走 `LocalClient.Status(ctx)`，避免额外代码体积；
  - Android 显式空导入 `tailscale.com/feature/androidbin`（绕过 Android 11+ `NETLINK_ROUTE` `EPERM`）并支持 JNI `netns.SetAndroidBindToNetworkFunc`。
- 探针实测增量（`tailscale.com@v1.106.0`，`go build -trimpath -ldflags="-s -w" -tags=<裁剪标签>`，以含 `net/http` + `crypto/tls` + `encoding/json` 的 4.04 MB 空壳为基线；阶段 7 完成后以完整 Release 包复测替换）：
  - `darwin/arm64` 探针：原始 `+10.25 MB`，LZMA（对应 MyGo `hdiutil -format ULMO`）`+2.65 MB`；预计 `Posse.app` +9~10 MB、`Posse.dmg` +2.5~2.7 MB（13.5 MB → ~16 MB）；
  - `ios/arm64`（`c-archive` + `clang -dynamiclib`）纯探针 `16.2 MB`，`android/arm64`（`c-shared`）纯探针 `16.6 MB`；合并进现有 `libposse_quic`（9.6~10 MB）预计未压缩 +12~16 MB，iOS `.ipa` 约 +4.5~5.5 MB。

## 传输与连接架构

### 1. 控制面与终端流：`tsquic://`（QUIC over `tsnet` UDP）

- 常规会话复用现有 `mquic`、E2EE v2（`framing: 3`）、`hq_*` 回调和 `QuicByteSocket`（仅 Mac 端额外开放同端口 `tslink.Node.ListenTCP` 接入现有 WebSocket 服务，供官方 Tailscale 手机首次扫码配对）：
  - **Mac 端**：在 `internal/mobile/quic.go` 中为内置 Tailscale 增加一路 `quic.Transport`（`tslink.Node.ListenPacket(port)`，端口与局域网 QUIC 相同），复用到同一套 `acceptQUIC`；同时经 `tslink.Node.ListenTCP(port)` 提供首次扫码 WebSocket 入口。`posse.quic` 与配对码优先附带内置 Tailscale 的 IPv4（`100.64.0.0/10`）。
  - **手机端**：通过 `tslink.Node.ListenPacket(0)` 取得 `net.PacketConn`，交给 `mquic.DialOver(ctx, pc, raddr, mquic.TSConfig())`。
- **MTU 关键修正**：`quic-go` 默认 `InitialPacketSize = 1280` 加上 IP/UDP 头（1308 B）会超 Tailscale 默认 1280 B MTU，且 gVisor `gonet` 不会返回 `syscall.EMSGSIZE` 触发回退。因此 `mquic.TSConfig()` 必须固定 `InitialPacketSize: 1200` 和 `DisablePathMTUDiscovery: true`。

### 2. 看 Mac 桌面（HEVC over RTP/UDP）

- **Mac 端**：在 `internal/desk/route.go` 的 `Routes` 上挂接可选的 `ts` `net.PacketConn` 与对端路由查询函数 `tsIsDERP(addr)`。收到 `tsnet` 包后按该对端是否为 DERP 决定：
  - Tailscale P2P 直连：走直连码率策略（最高 40 Mbps）；
  - Tailscale DERP 中继：视同中转（最高 20 Mbps），且不阻止切往更快链路。
- **手机端**：在 Go 层增加回环 UDP 桥（`hq_ts_udp_bridge`）：
  - `tsnet` 侧通过 `tslink.Node.DialUDP(ctx, "<mac-100.x>:<deskPort>")`（底层基于 `ListenPacket` 包装，只走 netstack 绝不回退宿主 UDP，Mac 端回包端口固定）；
  - 回环侧用非连接型 `127.0.0.1:0`，逐包重学原生 `DeskSession` 的来源端口（暂停恢复时原生 socket 会重建），并用同一回环 socket 回包以满足 `noteActive` 校验；
  - Dart 侧不复用 `deskHostOf` / `deskUdpHost` 推断，改为显式的 `tailscale` 候选类型传入原生 `DeskSession`（线路名报 `tailscale`），对端为 DERP 且配了自建中转时优先中转。
- **MTU**：看桌面单包上限 `PacketLimit = 1200`，加 IP/UDP 头为 1228 B ≤ 1280 B，不会分片。

### 3. `PosseLink` 竞速与状态语义

- 仅在手机端 `tsnet` 处于 `Running` 时才把 `tsquic://<100.x>:<port>` 加入竞速列表（避免未登录时每轮竞速挂满 10s）。
- `tsquic://` 属于 QUIC 族：免 2 秒 `quicHeadStart`，赢了后不触发 `_learnQuic` 升级（避免刚连上就被中转 QUIC 顶掉）。
- 物理网络变化（Wi-Fi ↔ 蜂窝）时，对 `tsquic://` 链路先探测而非立即断开，保留 Tailscale 漫游能力。
- 手机和桌面 UI 线路明确区分「Tailscale 直连」与「Tailscale 中继 (DERP)」。
- 同时配了自建 `deskrelay` 中转且对端走 DERP 时，优先中转、Tailscale 作为兜底。

## 认证、持久化与生命周期

- **双登录模式**：
  - 默认：交互式浏览器登录（读取 `Status.AuthURL` 拉起系统浏览器 / `ASWebAuthenticationSession` / Custom Tabs 授权）；
  - 高级：一次性 `AuthKey`（Posse 不落盘且启动后清除自身与 `tsnet.Server` 字段引用；上游 `controlclient` 在节点生命周期内会持有该字段用于注册，且未填 `AuthKey` 时上游会回退读 `TS_AUTHKEY`/`TS_AUTH_KEY` 环境变量）+ 可选自定义 `ControlURL`（支持 Headscale，未填时显式传入 `ipn.DefaultControlURL` 避免被 `TS_CONTROL_URL` 环境变量静默劫持）。
- **状态目录与防克隆**：
  - Mac：配置存 `$POSSE_HOME/tailscale.json`（0600），节点状态存 `$POSSE_HOME/tailscale/`；
  - iOS：存排除 iCloud 备份的私有目录（`isExcludedFromBackup = true`，数据保护级别保持 `CompleteUntilFirstUserAuthentication` 以支持锁屏保活写 netmap）；
  - Android：存 `getNoBackupFilesDir()`（避免 Auto Backup 换机后克隆节点密钥互踢）。
  - 始终设置 `Ephemeral = false`；提供「退出并清除身份」支持重登与应对 180 天 key 过期。
- **生命周期与交接**：
  - **Mac 端**：随 `d.startMobile()` 在拿到 `possed.lock` 后启动，常驻进程退出/交接前关闭 `tsnet.Server`，确保新旧进程绝不同时使用同一 `StateDir` 在线互踢。
  - **手机端**：`tsnet.Server` 为进程级单例，不放进 `conns` 表；`hq_reset()` 只清理 Dart 回调、不拆节点；附加空闲关停机制避免 Android Activity 销毁后长期保活 DERP；iOS 回到前台 `wake()` 时调 `InjectLinkChange()`（`netMon.InjectEvent()`）加速重探。
- **代理共存开关**：
  - iOS/macOS 默认由 Tailscale `netns` 绑底层物理网卡（利于 UDP 打洞），提供可关闭开关（`DisableBindInterface` → `netns.SetDisableBindConnToInterface`）以便国内网络让控制面/DERP 走系统代理；
  - Android 通过 JNI 注入 `netns.SetAndroidBindToNetworkFunc`（`Network.bindSocket`）绑定底层网络，同样受开关控制。

## 分阶段实施

1. `docs/plans/embedded-tailscale.md`、根 `go.mod` 升级与共享核心包 `internal/tslink`
2. `internal/mquic` 扩展 + Mac 端 `mobile`、`desk`、`daemon` 与 CLI 接入
3. Mac 桌面手机页「内置 Tailscale」卡片与多语言
4. 手机 Go FFI（`posse_quic`）接入 `tsnet`、`tsquic://`、回环 UDP 桥与原生平台钩子
5. 手机端 `PosseLink` 竞速与看桌面显式 Tailscale 线路
6. 手机端设置页「内置 Tailscale」UI 与线路标注
7. 打包脚本裁剪 tag、全端构建验证与文档更新
