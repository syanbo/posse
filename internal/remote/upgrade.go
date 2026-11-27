package remote

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"posse/internal/daemon"
	"posse/internal/rpc"
)

// compareSemver 按数字段比较版本号（如 0.1.55 > 0.1.54，正式版 > 预发布版）。
func compareSemver(a, b string) int {
	a, b = strings.TrimPrefix(strings.TrimSpace(a), "v"), strings.TrimPrefix(strings.TrimSpace(b), "v")
	a, _, _ = strings.Cut(a, "+")
	b, _, _ = strings.Cut(b, "+")
	ar, apre, _ := strings.Cut(a, "-")
	br, bpre, _ := strings.Cut(b, "-")
	as, bs := strings.Split(ar, "."), strings.Split(br, ".")
	n := max(len(as), len(bs))
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// buildCommit 从 daemon.info 的 Build（格式 "<rev> <RFC3339>" 或 "<RFC3339>"）提取 commit 短哈希。
func buildCommit(build string) string {
	rev, _, ok := strings.Cut(strings.TrimSpace(build), " ")
	if !ok || rev == "" || rev == "dev" {
		return ""
	}
	return rev
}

// shouldAutoUpgradeHeadless 判断托管无头服务端是否需要自动热升级：
// 1. 仅限 edition == "headless"；
// 2. 目标版本严格更新时升级（绝不降级）；
// 3. 同版本且双方都有有效 commit 且不同时升级（便于开发迭代与同版本热修）。
func shouldAutoUpgradeHeadless(info rpc.RemoteInfo, targetVersion, targetCommit string) bool {
	if info.Edition != "headless" || targetVersion == "" {
		return false
	}
	cmp := compareSemver(targetVersion, info.Version)
	if cmp > 0 {
		return true
	}
	if cmp < 0 {
		return false
	}
	rc := buildCommit(info.Build)
	tc := strings.TrimSpace(targetCommit)
	return rc != "" && tc != "" && tc != "dev" && rc != tc
}

func (s *Session) upgradeHeadless(ctx context.Context, opts Options, pkg ServerPackage) (rpc.RemoteInfo, error) {
	opts.report(StageUpload, 0)
	exePath, err := s.stageAndCommitServer(ctx, s.Info.Home, pkg, opts.Progress)
	if err != nil {
		return rpc.RemoteInfo{}, fmt.Errorf("auto-upgrade install failed: %w", err)
	}
	opts.report(StageUpgrade, 0)
	newInfo, err := handoffAndVerify(ctx, s.Socket, exePath, s.Info.InstanceID, "headless", opts.Required, s.Info.Home)
	if err != nil {
		return rpc.RemoteInfo{}, fmt.Errorf("auto-upgrade handoff failed: %w", err)
	}
	if err := s.activateServer(ctx, exePath, s.Info.Home, pkg.Tag); err != nil {
		return rpc.RemoteInfo{}, fmt.Errorf("remote server activate failed after handoff: %w", err)
	}
	return newInfo, nil
}

// maybeUpgradeHeadless 在重连握手已通过后，检查托管无头服务端是否有新包可用并做无损热交接。
// 优先查本地包（零网络开销）；无本地包时仅在主 Mac 版本高于远端版本时才检查 R2。
// 任何非致命失败（如离线无法拉 R2）都会保留已建立的可用连接，不阻断用户重连。
func (s *Session) maybeUpgradeHeadless(ctx context.Context, h Host, opts Options) error {
	if !opts.AutoInstall || h.Executable != "" || s.Info.Edition != "headless" {
		return nil
	}
	// 先查本地已有压缩包（无网络开销，能识别本地新版本或同版本新 commit）
	localOpts := opts
	localOpts.DisableRemoteDownload = true
	pkg, err := localOpts.resolveServer(ctx, s.Info.Platform, s.Info.Arch)
	if err != nil {
		// 本地无包：仅当本机 Posse 版本严格高于远端时才尝试联网查 R2，避免常规重连多一次外网请求
		if opts.DisableRemoteDownload || compareSemver(daemon.Version, s.Info.Version) <= 0 {
			return nil
		}
		dlCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		pkg, err = opts.resolveServer(dlCtx, s.Info.Platform, s.Info.Arch)
		cancel()
		if err != nil {
			return nil
		}
	}
	defer pkg.Close()

	if !shouldAutoUpgradeHeadless(s.Info, pkg.Version, pkg.Commit) {
		return nil
	}

	newInfo, err := s.upgradeHeadless(ctx, opts, pkg)
	if err != nil {
		// 升级失败时复检原连接是否仍健康：原 daemon 仍可用则优雅降级保留当前会话，不阻断连接
		var alive rpc.RemoteInfo
		if checkErr := rpc.Call(ctx, s.Socket, "daemon.info", nil, &alive); checkErr == nil {
			s.Info = alive
			return nil
		}
		return err
	}
	s.Info = newInfo
	return nil
}
