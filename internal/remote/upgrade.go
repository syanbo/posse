package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"posse/internal/daemon"
	"posse/internal/rpc"
)

const upgradeCheckCooldown = 30 * time.Second

// compareSemver 按 SemVer 2.0 规则比较版本号（含点分预发布数字段：beta.10 > beta.9，正式版 > 预发布版）。
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
	return comparePrerelease(apre, bpre)
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := min(len(as), len(bs))
	for i := 0; i < n; i++ {
		if as[i] == bs[i] {
			continue
		}
		ai, aErr := strconv.Atoi(as[i])
		bi, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if ai < bi {
				return -1
			}
			return 1
		case aErr == nil:
			return -1 // SemVer 2.0：数字标识符优先级低于非数字
		case bErr == nil:
			return 1
		case as[i] < bs[i]:
			return -1
		default:
			return 1
		}
	}
	if len(as) < len(bs) {
		return -1
	}
	if len(as) > len(bs) {
		return 1
	}
	return 0
}

// parseBuildInfo 从 daemon.info 的 Build（"<rev> <RFC3339>" 或 "<RFC3339>"）解析 commit 与构建时间。
func parseBuildInfo(build string) (commit string, builtAt time.Time) {
	s := strings.TrimSpace(build)
	if s == "" {
		return "", time.Time{}
	}
	first, rest, hasSpace := strings.Cut(s, " ")
	if hasSpace {
		if first != "" && first != "dev" {
			commit = first
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(rest)); err == nil {
			builtAt = t
		}
		return commit, builtAt
	}
	if t, err := time.Parse(time.RFC3339, first); err == nil {
		return "", t
	}
	if first != "dev" {
		return first, time.Time{}
	}
	return "", time.Time{}
}

// isManagedHeadless 仅认可由目标 daemon 自身核对过的受管无头版本目录（spawn.RuntimeDirFor + 受管标记），
// 避免自动升级接管用户手工放置的独立二进制。
func isManagedHeadless(info rpc.RemoteInfo) bool {
	return info.Edition == "headless" && info.Managed
}

// shouldAutoUpgradeHeadless 判断托管无头服务端是否需要单调向前热升级：
// 1. 仅限托管 headless 安装；
// 2. 目标版本严格更新时升级（绝不降级）；
// 3. 同版本时仅当目标包具备更新的构建时间戳且 commit 不同才升级（杜绝双机开发构建来回切换）。
func shouldAutoUpgradeHeadless(info rpc.RemoteInfo, targetVersion, targetCommit string, targetBuiltAt time.Time) bool {
	if !isManagedHeadless(info) || targetVersion == "" {
		return false
	}
	cmp := compareSemver(targetVersion, info.Version)
	if cmp > 0 {
		return true
	}
	if cmp < 0 {
		return false
	}
	rc, rTime := parseBuildInfo(info.Build)
	tc := strings.TrimSpace(targetCommit)
	if rc == "" || tc == "" || tc == "dev" || rc == tc {
		return false
	}
	return !targetBuiltAt.IsZero() && !rTime.IsZero() && targetBuiltAt.After(rTime)
}

func (s *Session) upgradeHeadless(ctx context.Context, opts Options, pkg ServerPackage) (newInfo rpc.RemoteInfo, handedOff bool, err error) {
	opts.report(StageUpload, 0)
	exePath, err := s.stageAndCommitServer(ctx, s.Info.Home, pkg, opts.Progress)
	if err != nil {
		return rpc.RemoteInfo{}, false, fmt.Errorf("auto-upgrade install failed: %w", err)
	}
	opts.report(StageUpgrade, 0)
	newInfo, err = handoffAndVerify(ctx, s.Socket, exePath, s.Info.InstanceID, "headless", opts.Required, s.Info.Home)
	if err != nil {
		return rpc.RemoteInfo{}, false, fmt.Errorf("auto-upgrade handoff failed: %w", err)
	}
	if err := s.activateServer(ctx, exePath, s.Info.Home, pkg.Tag); err != nil {
		// 热交接已切换实例，重试一次 activate；仍失败则返回错误，不静默留存过期 current
		if rerr := s.activateServer(ctx, exePath, s.Info.Home, pkg.Tag); rerr != nil {
			return newInfo, true, fmt.Errorf("remote server activate failed after handoff: %w", rerr)
		}
	}
	return newInfo, true, nil
}

// maybeUpgradeHeadless 在握手或健康复用通过后，检查托管无头服务端是否有更新包可用并做无损热交接。
// 优先查本地包（零网络开销）；无本地包时仅在主 Mac 版本高于远端版本时才检查 R2。
// 仅在尚未切换实例且原实例通过完整握手与能力复检时才降级保留原会话；已交接但激活失败会明确报错。
func (s *Session) maybeUpgradeHeadless(ctx context.Context, h Host, opts Options) error {
	if !opts.AutoInstall || h.Executable != "" || !isManagedHeadless(s.Info) {
		return nil
	}
	now := time.Now()
	if !s.lastUpgradeCheck.IsZero() && now.Sub(s.lastUpgradeCheck) < upgradeCheckCooldown {
		return nil
	}
	s.lastUpgradeCheck = now

	localOpts := opts
	localOpts.DisableRemoteDownload = true
	pkg, err := localOpts.resolveServer(ctx, s.Info.Platform, s.Info.Arch)
	if err != nil {
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

	if !shouldAutoUpgradeHeadless(s.Info, pkg.Version, pkg.Commit, pkg.BuiltAt) {
		return nil
	}

	newInfo, handedOff, err := s.upgradeHeadless(ctx, opts, pkg)
	if err != nil {
		if handedOff {
			// 新实例已接管但 server/current 未能切换：保留最新 Info 并明确报错，避免隐藏过期启动入口
			s.Info = newInfo
			return err
		}
		var alive rpc.RemoteInfo
		if checkErr := rpc.Call(ctx, s.Socket, "daemon.info", nil, &alive); checkErr == nil {
			if validateInfo(s.Probe, alive, opts.Required) == nil && filepath.Clean(alive.Home) == filepath.Clean(s.Info.Home) {
				s.Info = alive
				return nil
			}
		}
		return err
	}
	s.Info = newInfo
	return nil
}
