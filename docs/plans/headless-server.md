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
- `internal/remote/server_pkg.go` 按序解析对应架构的压缩包：显式 `--file` → `POSSE_SERVER_DIR` → 应用内 `Resources/server/` / 仓库 `build/server/` → `$POSSE_HOME/server-cache/<ver>/` → R2 线上清单（Ed25519 + SHA-256 验签）。
- `internal/remote/install.go` 复用已认证的 SSH `ControlMaster` 流式推送并原子落盘；`posse remote install <host>`、`posse remote check <host> --install` 与桌面窗口主机菜单（`selectRemoteHost`）均已接入；远端 daemon 已在运行时自动走 `daemon.handoff` 无损交接。
- `daemon.info` / `daemon.status` / `remote probe` 新增兼容字段 `edition`（`full` / `headless`）和 `features`；本机桌面窗口 `autoUpgrade` 只向同 `edition` 交接，避免同机共存时与无头服务来回切换。

## 2. 使用方式

```sh
# 1. 在主 Mac 构建（离线/本地测试时；正式发布接入 R2 后可自动拉取）
scripts/build-server.sh            # 或 --all 同时打 arm64 + amd64

# 2. 添加目标 Mac（目标 Mac 只需开启「系统设置 → 通用 → 共享 → 远程登录」）
posse remote add builder user@mac-builder.local

# 3. 方式 B：直接在 Posse 左下「+」或状态栏内存左侧「添加 SSH」添加 builder，随后点击最左侧导航栏里的 builder 磁贴（或按 ⌃⌥H）自动推送拉起并自动接管
posse remote install builder
posse remote check builder --install
```

## 3. 已知边界与后续规划（Phase 2 / 3）

- **钥匙串与开机常驻（Mac 打包机场景）**：经 SSH 会话首次拉起的 daemon 继承非 Aqua 安全会话，执行 `xcodebuild`/`codesign` 或读取登录钥匙串里的 agent 凭据可能受限；需要开机常驻和签名时，后续补 `posse server install --launchd`（`LaunchAgent` 调幂等的 `posse daemon start`，`RunAtLoad` + `StartInterval`，不设 `KeepAlive`，配合自动登录或 `fdesetup authrestart`）。
- **R2 发布接入**：将 `scripts/build-server.sh --all` 产物与 Ed25519 签名的 `server-darwin-{arm64,amd64}.json` 接入 `scripts/release.sh`。
- **彻底去 `mygo` 依赖**：替换 `internal/daemon/history_watch.go` 的 `mygo.WatchFiles`，使 headless 构建达到 0 `mygo` 依赖。
- **Linux 无头节点（Phase 3）**：补齐 `pidfd` 进程退出监视、`TCFLSH`/`FIONREAD` 平台化、`desk` 桩函数、`procmem_linux.go`（`/proc`）、`UserShell`/`LANG`、`BuildTagsLinux`、`systemd --user` 托管与 `ssh.go` 放开 `Platform == "linux"`。
