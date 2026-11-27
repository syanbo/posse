package remote

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"posse/internal/daemon"
	"posse/internal/rpc"
)

const (
	upgradeCheckCooldown = 30 * time.Second
	handoffBudgetTimeout = 60 * time.Second
)

// ErrServerUpToDate 表示远端托管无头服务端已是最新可用版本，无需执行热升级。
var ErrServerUpToDate = errors.New("remote server is already up to date")

// ServerUpgradeCheck 描述一次轻量更新检查的结果（不下载完整压缩包）。
type ServerUpgradeCheck struct {
	Available      bool
	UpToDate       bool
	DesktopTooOld  bool
	Source         string // "local" | "remote" | "none"
	CurrentVersion string
	CurrentCommit  string
	TargetVersion  string
	TargetCommit   string
	TargetBuiltAt  time.Time
	DesktopVersion string
	CheckedAt      time.Time
}

// CheckServerUpgrade 对托管无头服务端执行轻量更新检查：
// 1. 优先检查受信本地包目录（inspectAndSnapshot 后立即 Close，不留快照文件）；
// 2. 无本地包时仅拉取并验签 R2 上的 server-<os>-<arch>.json 清单（≤ 64 KiB，不下载压缩包）；
// 3. 远端清单只比较 Version 且受 compareSemver(daemon.Version, manifest.Version) >= 0 约束，不把远端升到比桌面更新。
func CheckServerUpgrade(ctx context.Context, info rpc.RemoteInfo, opts Options) (ServerUpgradeCheck, error) {
	if err := ctx.Err(); err != nil {
		return ServerUpgradeCheck{}, err
	}
	if !isManagedHeadless(info) {
		return ServerUpgradeCheck{}, errors.New("server upgrade is only supported for managed headless nodes")
	}
	goos, err := NormalizeOS(info.Platform)
	if err != nil {
		return ServerUpgradeCheck{}, err
	}
	goarch, err := NormalizeArch(info.Arch)
	if err != nil {
		return ServerUpgradeCheck{}, err
	}
	curCommit, _ := parseBuildInfo(info.Build)
	res := ServerUpgradeCheck{
		Source:         "none",
		CurrentVersion: info.Version,
		CurrentCommit:  curCommit,
		DesktopVersion: daemon.Version,
		CheckedAt:      time.Now(),
	}

	localOpts := opts
	localOpts.DisableRemoteDownload = true
	pkg, localErr := localOpts.resolveServer(ctx, goos, goarch)
	if localErr == nil {
		pkg.Close()
		res.Source = "local"
		res.TargetVersion = pkg.Version
		res.TargetCommit = pkg.Commit
		res.TargetBuiltAt = pkg.BuiltAt
		avail := shouldAutoUpgradeHeadless(info, pkg.Version, pkg.Commit, pkg.BuiltAt)
		res.Available = avail
		res.UpToDate = !avail
		return res, nil
	}
	if ctx.Err() != nil {
		return ServerUpgradeCheck{}, ctx.Err()
	}
	if opts.ServerArchive != "" || opts.DisableRemoteDownload {
		return res, localErr
	}

	m, _, err := fetchVerifiedServerManifest(ctx, ServerResolveOptions{
		TargetOS:      goos,
		TargetArch:    goarch,
		LocalHome:     opts.LocalHome,
		BaseURL:       opts.ServerBaseURL,
		PublicKey:     opts.ServerPubKey,
		DisableRemote: opts.DisableRemoteDownload,
	}, goos, goarch)
	if err != nil {
		return res, err
	}
	res.Source = "remote"
	res.TargetVersion = m.Version
	res.TargetCommit = strings.TrimSpace(m.Commit)
	newerThanRemote := compareSemver(m.Version, info.Version) > 0
	withinDesktop := compareSemver(daemon.Version, m.Version) >= 0
	switch {
	case newerThanRemote && withinDesktop:
		res.Available = true
	case newerThanRemote && !withinDesktop:
		res.DesktopTooOld = true
		res.UpToDate = compareSemver(info.Version, daemon.Version) >= 0
	default:
		res.UpToDate = true
	}
	return res, nil
}

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
	cur := s.CurrentInfo()
	opts.report(StageUpload, 0)
	stage := s.stageAndCommitServer
	if s.stageOverride != nil {
		stage = s.stageOverride
	}
	exePath, err := stage(ctx, cur.Home, pkg, opts.Progress)
	if err != nil {
		return rpc.RemoteInfo{}, false, fmt.Errorf("auto-upgrade install failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return rpc.RemoteInfo{}, false, err
	}
	if opts.BeforeHandoff != nil {
		if err := opts.BeforeHandoff(ctx); err != nil {
			return rpc.RemoteInfo{}, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return rpc.RemoteInfo{}, false, err
	}

	// 从 daemon.handoff 发出起改用 WithoutCancel + 60s 独立预算，避免窗口关闭或上层取消导致 handoff 与 activate 中断。
	handoffBudgetCtx, cancelBudget := context.WithTimeout(context.WithoutCancel(ctx), handoffBudgetTimeout)
	defer cancelBudget()

	opts.report(StageUpgrade, 0)
	newInfo, err = handoffAndVerify(handoffBudgetCtx, s.Socket, exePath, cur.InstanceID, "headless", opts.Required, cur.Home, pkg.OS, pkg.Arch, pkg.Version, pkg.Tag)
	if err != nil {
		// 若 handoff 已确认切换到不同版本，绝不激活本次旧包，直接保留新快照并报错。
		if errors.Is(err, errMismatchedHandoffTarget) {
			return newInfo, true, err
		}
		// 明确的交接前拒绝（如 CodeBusy/CodeReadOnly/CodeBadRequest）无需误判为已交接。
		var re *rpc.Error
		if errors.As(err, &re) && (re.Code == rpc.CodeBusy || re.Code == rpc.CodeReadOnly || re.Code == rpc.CodeBadRequest) {
			return rpc.RemoteInfo{}, false, fmt.Errorf("auto-upgrade handoff failed: %w", err)
		}
		// 传输瞬断或回复丢失时，在总预算内最多 3 次有限重试复查 daemon.info 并严格核对目标版本/tag。
		for attempt := 0; attempt < 3 && handoffBudgetCtx.Err() == nil; attempt++ {
			if attempt > 0 {
				select {
				case <-handoffBudgetCtx.Done():
				case <-time.After(500 * time.Millisecond):
				}
			}
			var alive rpc.RemoteInfo
			checkCtx, checkCancel := context.WithTimeout(handoffBudgetCtx, 5*time.Second)
			checkErr := rpc.Call(checkCtx, s.Socket, "daemon.info", nil, &alive)
			checkCancel()
			if checkErr != nil {
				continue
			}
			if alive.InstanceID == "" || alive.InstanceID == cur.InstanceID {
				break
			}
			if !matchesUpgradeTarget(alive, cur.InstanceID, cur.Home, pkg.OS, pkg.Arch, pkg.Version, pkg.Tag) {
				return alive, true, errMismatchedHandoffTarget
			}
			if capErr := checkRequiredCapabilities(alive, opts.Required); capErr != nil {
				return alive, true, capErr
			}
			newInfo = alive
			err = nil
			break
		}
	}
	if err != nil {
		return rpc.RemoteInfo{}, false, fmt.Errorf("auto-upgrade handoff failed: %w", err)
	}

	opts.report(StageActivate, 0)
	activate := s.activateServer
	if s.activateOverride != nil {
		activate = s.activateOverride
	}
	var actErr error
	for attempt := 0; attempt < 2; attempt++ {
		actCtx, actCancel := context.WithTimeout(handoffBudgetCtx, 15*time.Second)
		actErr = activate(actCtx, exePath, cur.Home, pkg.Tag)
		actCancel()
		if actErr == nil {
			break
		}
	}
	if actErr != nil {
		return newInfo, true, fmt.Errorf("remote server activate failed after handoff: %w", actErr)
	}
	return newInfo, true, nil
}

// maybeUpgradeHeadless 在握手或健康复用通过后，检查托管无头服务端是否有更新包可用并做无损热交接。
// 优先查本地包（零网络开销）；无本地包时仅在主 Mac 版本高于远端版本时才检查 R2。
// 仅在尚未切换实例且原实例（InstanceID 相同）通过完整握手与能力复检时才降级保留原会话；已交接但激活失败会明确报错。
func (s *Session) maybeUpgradeHeadless(ctx context.Context, h Host, opts Options) error {
	if !opts.AutoInstall || h.Executable != "" {
		return nil
	}
	// 与手动 UpgradeActive 互斥：若该会话已有热升级正在执行，自动检查直接让路而不并发重复升级。
	if !s.tryLockUpgrade() {
		return nil
	}
	defer s.unlockUpgrade()
	return s.maybeUpgradeHeadlessLocked(ctx, h, opts)
}

func (s *Session) maybeUpgradeHeadlessLocked(ctx context.Context, h Host, opts Options) error {
	if !opts.AutoInstall || h.Executable != "" {
		return nil
	}
	s.mu.Lock()
	cur := s.Info
	if !isManagedHeadless(cur) {
		s.mu.Unlock()
		return nil
	}
	now := time.Now()
	if !s.lastUpgradeCheck.IsZero() && now.Sub(s.lastUpgradeCheck) < upgradeCheckCooldown {
		s.mu.Unlock()
		return nil
	}
	s.lastUpgradeCheck = now
	s.mu.Unlock()

	localOpts := opts
	localOpts.DisableRemoteDownload = true
	pkg, err := localOpts.resolveServer(ctx, cur.Platform, cur.Arch)
	if err != nil {
		if opts.DisableRemoteDownload || compareSemver(daemon.Version, cur.Version) <= 0 {
			return nil
		}
		dlCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		pkg, err = opts.resolveServer(dlCtx, cur.Platform, cur.Arch)
		cancel()
		if err != nil {
			return nil
		}
	}
	defer pkg.Close()

	if !shouldAutoUpgradeHeadless(cur, pkg.Version, pkg.Commit, pkg.BuiltAt) {
		return nil
	}

	newInfo, handedOff, err := s.upgradeHeadless(ctx, opts, pkg)
	if err != nil {
		if handedOff {
			// 新实例已接管但 server/current 未能切换或版本失配：保留最新 Info 并明确报错，避免隐藏过期启动入口
			s.setInfo(newInfo)
			return err
		}
		if ctx.Err() == nil {
			var alive rpc.RemoteInfo
			checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
			checkErr := rpc.Call(checkCtx, s.Socket, "daemon.info", nil, &alive)
			checkCancel()
			if checkErr == nil && alive.InstanceID == cur.InstanceID {
				if validateInfo(s.Probe, alive, opts.Required) == nil && filepath.Clean(alive.Home) == filepath.Clean(cur.Home) {
					s.setInfo(alive)
					return nil
				}
			}
		}
		return err
	}
	s.setInfo(newInfo)
	return nil
}
