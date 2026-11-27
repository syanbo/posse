# 无头服务端与 SSH 自动推送（形态 2）

目标：优先让目标 Mac **无需预先安装或打开 `Posse.app`**，只需开启 SSH（远程登录），主 Mac 即可像 VS Code Remote-SSH 一样直接连接，自动推送轻量无头服务端并完整复用现有远程工作区能力（终端、Agent 状态、文件、Git、断线不断终端）。

## 1. 架构与已落地实现（Phase 1）

### 单一二进制 + `headless` 构建标签
- 不另起 `cmd/possed`，保留单一 `cmd/posse`（远端终端里的 hooks、`agent exec`、`statusline`、`task`、`remote probe` 全走 `$POSSE_HOME/bin/posse`）。
- 切开 `internal/cli` 对 `internal/app` 的依赖：`CtlSocket` 迁入 `internal/rpc/ctl.go`，`cmdApp` 拆为 `app_gui.go`（`!headless`）与 `app_headless.go`（`headless`）。
- `internal/vt/library.go` 自管 `libghostty-vt` 查找（顺序：`POSSE_GHOSTTY_VT` / `MYGO_GHOSTTY_VT` → `.app/Contents/Resources` → 可执行文件真实目录 → `$POSSE_HOME/natives/<sha256>` → 用户缓存），`headless` 构建禁止运行期从 GitHub 下载，在 `daemon.Run` 启动阶段早失败校验。

### Mac 无头服务端产物与签名（`scripts/build-server.sh`）
- 输出 `build/server/posse-server-<version>-darwin-<arch>.tar.gz`（含 `posse`、`libghostty-vt.dylib`、`manifest.json`）。
- 检测到 `mygo.json` 里的 Developer ID 证书（Team `QKSM9Z4U85`）时，以 Hardened Runtime + 固定 identifier `com.dengbo.posse.server` 签名 `posse`，同证书重签 `libghostty-vt.dylib`，确保跨升级保留 TCC 授权并满足库校验；无证书测试环境可加 `--adhoc`。

### 远端版本化布局与引用回收
```text
$POSSE_HOME/server/
  versions/<version>-<sha12>/
    posse
    libghostty-vt.dylib
    refs/<homeHash>
  current -> versions/<version>-<sha12>
```
- 永不原地覆盖运行中的二进制（防 macOS 签名校验触发 SIGKILL）。
- `internal/spawn/runtime.go` 扩展识别 `<home>/server/versions/<tag>/posse`，复用 `Retain` / `Collect` 引用回收，并始终保留 `server/current` 指向的版本，直到旧版本拉起的终端全部退出后自动回收。

### SSH 自动推送、拉起与无损交接
- `internal/remote/ssh.go` 发现列表优先查 `$POSSE_HOME/server/current/posse`，全部缺失时单次往返返回 `POSSE_REMOTE_MISSING:<os>:<arch>`。
- `internal/remote/server_pkg.go` 按序解析对应架构的压缩包：显式 `--file` → `POSSE_SERVER_DIR` → 应用内 `Resources/server/` / 仓库 `build/server/` → R2 线上清单（Ed25519 + SHA-256 验签，命中 `$POSSE_HOME/server-cache/objects/<sha256>.tar.gz` 内容寻址对象缓存时免重复下载）。
- `internal/remote/install.go` 复用已认证的 SSH `ControlMaster` 流式推送并原子落盘；`posse remote install <host>`、`posse remote check <host> --install` 与桌面窗口主机菜单（`selectRemoteHost`）均已接入；远端 daemon 已在运行时自动走 `daemon.handoff` 无损交接。
- `daemon.info` / `daemon.status` / `remote probe` 新增兼容字段 `edition`（`full` / `headless`）和 `features`；本机桌面窗口 `autoUpgrade` 只向同 `edition` 交接，避免同机共存时与无头服务来回切换。

## 2. 使用方式

```sh
# 1. 在主 Mac 构建（离线/本地测试时；正式发布通过 scripts/release.sh 自动同步到 R2）
scripts/build-server.sh            # 或 --all 同时打 arm64 + amd64；发布用 --require-signing

# 2. 添加目标 Mac（目标 Mac 只需开启「系统设置 → 通用 → 共享 → 远程登录」）
posse remote add builder user@mac-builder.local

# 3. 方式 B：直接在 Posse 左下「+」或状态栏内存左侧「添加 SSH」添加 builder，随后点击最左侧导航栏里的 builder 磁贴（或按 ⌃⌥H）自动推送拉起并自动接管
posse remote install builder [--launchd]
posse remote check builder --install

# 4. 在目标 Mac 上单独管理开机自启（Aqua 会话，方便钥匙串/codesign）：
posse server install --launchd [--interval 60]
posse server status --launchd
posse server uninstall --launchd
```

## 3. 已落地运维与发布接入（Phase 2）

- **R2 发布接入**：`scripts/release.sh` 在发布桌面版的同时，针对 `darwin/arm64` 与 `darwin/amd64` 分别校验 `server/server-darwin-<arch>.json`、以 `scripts/build-server.sh --require-signing` 构建 Developer ID 签名压缩包，调用 `tools/servermanifest` 使用同一份 `mygo-update.key`（`MYGO_UPDATER_PRIVATE_KEY`）签发 Ed25519 清单并核对 `mygo.json` 的 `updates.publicKey`，按先包后清单的顺序上传至 R2 `posse-updates/server/`（支持断点重试跳过已发架构）。
- **钥匙串与开机常驻（Mac 打包机场景）**：`posse server install --launchd`（及 `posse remote install <host> --launchd`）在 `~/Library/LaunchAgents/` 写入 Aqua 会话 `LaunchAgent`（`RunAtLoad` + `StartInterval=60` + `ProcessType=Interactive`，不设 `KeepAlive`，托管无头模式固定启动 `$POSSE_HOME/server/current/posse daemon start` 并传入 `POSSE_HOME`）。首次无人值守部署且未运行 daemon 时优先经 Aqua LaunchAgent 拉起；若当前未登录 GUI（无活跃 Aqua 会话），plist 保留待下次 macOS 图形登录生效（`fdesetup authrestart` 只跳过 FileVault 磁盘解锁，Aqua 会话与登录钥匙串仍需图形自动登录或交互登录）。卸载与查询对应 `posse server uninstall --launchd` 与 `posse server status --launchd`（卸载自启不停止已运行的 daemon）。

## 4. 已落地 Linux 无头节点与 0 `mygo` 依赖（Phase 3）

- **彻底去 `mygo` 依赖**：`internal/daemon/history_watch.go` 改用轻量目录树摘要轮询与防抖（不占额外 kqueue/inotify 描述符），`headless` 构建彻底实现 0 `github.com/egoist/mygo` 依赖。
- **Linux 平台适配**：
  - `internal/term`：`watch_linux.go` 使用 `pidfd_open` + `poll`（旧内核 `ENOSYS` 回退 `kill(pid, 0)` 轮询）监视交接进程退出；`pty_ioctl_linux.go` 使用 `TIOCINQ` 与 `TCFLSH`/`TCIFLUSH`；缺省 `LANG` 在 Linux 上回退 `C.UTF-8`。
  - `internal/agent`：`UserShell()` 在 Linux 缺 `SHELL` 时依次读取 `/etc/passwd` 与 `/bin/bash` → `/bin/sh`，不再硬编码 `/bin/zsh`。
  - `internal/procmem/procmem_linux.go`：基于 `/proc/<pid>/smaps_rollup`（PSS）、`status`（VmRSS）、`task/*/children`、`exe`/`cmdline`、`cwd`、`fd` 实现全套进程内存与上下文查询。
  - `internal/desk/run_other.go`：非 macOS 桌面采集子进程桩函数。
  - `internal/spawn/runtime_linux.go`：通过 `/proc/[0-9]*/exe` 识别活跃无头版本目录，使 `server/versions` 旧版本可按引用回收。
- **`systemd --user` 托管**：`internal/spawn/systemd.go`、`posse server install/status/uninstall --systemd` 与 `posse remote install <host> --systemd` 写入 `Type=oneshot` + `KillMode=process` 的 `<unit>.service` 和周期 `<unit>.timer`，保证 `daemon.handoff` 无损热交接与卸载都不会误杀运行中的常驻进程和终端；SSH 会话无活跃 user bus 时保留单元文件并提示启用 linger。
- **构建、远端接入与发布**：`internal/tslink.BuildTagsLinux` 与 `scripts/ts-tags.sh`（`posse_ts_tags_linux`）、`internal/remote` 放开 `darwin`/`linux` 探针与自动安装、`scripts/build-server.sh --os linux --all`（含 `libghostty-vt.so` SHA-256 核验与 ELF 架构校验）以及 `scripts/release.sh` 四平台 R2 发布（`darwin/arm64`、`darwin/amd64`、`linux/amd64`、`linux/arm64`）。
