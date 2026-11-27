// Package tslink 封装 Posse 双端共用的内置 Tailscale（tsnet）节点生命周期、
// 交互式/AuthKey 登录、对端直连/DERP 路由查询、网络切换唤醒以及裁剪构建标签。
package tslink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/netns"
	"tailscale.com/tsnet"
)

// 构建裁剪标签：由 `go run tailscale.com/cmd/featuretags --min --add=...` 生成，
// 保留 netstack、nattraversal（NAT 打洞）、cachenetmap、useproxy（Android 另保留 androidbin），
// 并显式排除 logtail（不向 Tailscale 上传日志）和 ipnbus（统一走 LocalClient.Status）。
const (
	// BuildTagsDarwin 用于 macOS 与 iOS 的最小裁剪标签。
	BuildTagsDarwin = "ts_omit_ace,ts_omit_acme,ts_omit_advertiseexitnode,ts_omit_advertiseroutes,ts_omit_androidbin,ts_omit_androiddns,ts_omit_appconnectors,ts_omit_aws,ts_omit_bakedroots,ts_omit_bird,ts_omit_c2n,ts_omit_captiveportal,ts_omit_capture,ts_omit_cliconndiag,ts_omit_clientmetrics,ts_omit_clientupdate,ts_omit_cloud,ts_omit_colorable,ts_omit_completion,ts_omit_completion_scripts,ts_omit_conn25,ts_omit_connreject,ts_omit_dbus,ts_omit_debug,ts_omit_debugeventbus,ts_omit_debugportmapper,ts_omit_desktop_sessions,ts_omit_dns,ts_omit_dnsresolvecache,ts_omit_doctor,ts_omit_drive,ts_omit_exitnodehealth,ts_omit_favorites,ts_omit_flashappliance,ts_omit_gro,ts_omit_health,ts_omit_hujsonconf,ts_omit_identityfederation,ts_omit_ipnbus,ts_omit_iptables,ts_omit_kube,ts_omit_linkspeed,ts_omit_linuxdnsfight,ts_omit_listenrawdisco,ts_omit_logtail,ts_omit_netlog,ts_omit_networkmanager,ts_omit_oauthkey,ts_omit_osrouter,ts_omit_outboundproxy,ts_omit_peerapiclient,ts_omit_peerapiserver,ts_omit_portlist,ts_omit_portmapper,ts_omit_posture,ts_omit_qrcodes,ts_omit_relayserver,ts_omit_remoteconfig,ts_omit_resolved,ts_omit_routecheck,ts_omit_runtimemetrics,ts_omit_sdnotify,ts_omit_serve,ts_omit_serviceclientprefs,ts_omit_ssh,ts_omit_synology,ts_omit_syslog,ts_omit_syspolicy,ts_omit_systray,ts_omit_taildrop,ts_omit_tailnetlock,ts_omit_tap,ts_omit_tpm,ts_omit_tundevstats,ts_omit_unixsocketidentity,ts_omit_useexitnode,ts_omit_usermetrics,ts_omit_useroutes,ts_omit_wakeonlan,ts_omit_webbrowser,ts_omit_webclient"

	// BuildTagsLinux 用于 Linux 无头服务端的最小裁剪标签（与 Darwin 同样走 userspace netstack）。
	BuildTagsLinux = "ts_omit_ace,ts_omit_acme,ts_omit_advertiseexitnode,ts_omit_advertiseroutes,ts_omit_androidbin,ts_omit_androiddns,ts_omit_appconnectors,ts_omit_aws,ts_omit_bakedroots,ts_omit_bird,ts_omit_c2n,ts_omit_captiveportal,ts_omit_capture,ts_omit_cliconndiag,ts_omit_clientmetrics,ts_omit_clientupdate,ts_omit_cloud,ts_omit_colorable,ts_omit_completion,ts_omit_completion_scripts,ts_omit_conn25,ts_omit_connreject,ts_omit_dbus,ts_omit_debug,ts_omit_debugeventbus,ts_omit_debugportmapper,ts_omit_desktop_sessions,ts_omit_dns,ts_omit_dnsresolvecache,ts_omit_doctor,ts_omit_drive,ts_omit_exitnodehealth,ts_omit_favorites,ts_omit_flashappliance,ts_omit_gro,ts_omit_health,ts_omit_hujsonconf,ts_omit_identityfederation,ts_omit_ipnbus,ts_omit_iptables,ts_omit_kube,ts_omit_linkspeed,ts_omit_linuxdnsfight,ts_omit_listenrawdisco,ts_omit_logtail,ts_omit_netlog,ts_omit_networkmanager,ts_omit_oauthkey,ts_omit_osrouter,ts_omit_outboundproxy,ts_omit_peerapiclient,ts_omit_peerapiserver,ts_omit_portlist,ts_omit_portmapper,ts_omit_posture,ts_omit_qrcodes,ts_omit_relayserver,ts_omit_remoteconfig,ts_omit_resolved,ts_omit_routecheck,ts_omit_runtimemetrics,ts_omit_sdnotify,ts_omit_serve,ts_omit_serviceclientprefs,ts_omit_ssh,ts_omit_synology,ts_omit_syslog,ts_omit_syspolicy,ts_omit_systray,ts_omit_taildrop,ts_omit_tailnetlock,ts_omit_tap,ts_omit_tpm,ts_omit_tundevstats,ts_omit_unixsocketidentity,ts_omit_useexitnode,ts_omit_usermetrics,ts_omit_useroutes,ts_omit_wakeonlan,ts_omit_webbrowser,ts_omit_webclient"

	// BuildTagsAndroid 用于 Android 的最小裁剪标签（保留 androidbin 与其依赖的 androiddns）。
	BuildTagsAndroid = "ts_omit_ace,ts_omit_acme,ts_omit_advertiseexitnode,ts_omit_advertiseroutes,ts_omit_appconnectors,ts_omit_aws,ts_omit_bakedroots,ts_omit_bird,ts_omit_c2n,ts_omit_captiveportal,ts_omit_capture,ts_omit_cliconndiag,ts_omit_clientmetrics,ts_omit_clientupdate,ts_omit_cloud,ts_omit_colorable,ts_omit_completion,ts_omit_completion_scripts,ts_omit_conn25,ts_omit_connreject,ts_omit_dbus,ts_omit_debug,ts_omit_debugeventbus,ts_omit_debugportmapper,ts_omit_desktop_sessions,ts_omit_dns,ts_omit_dnsresolvecache,ts_omit_doctor,ts_omit_drive,ts_omit_exitnodehealth,ts_omit_favorites,ts_omit_flashappliance,ts_omit_gro,ts_omit_health,ts_omit_hujsonconf,ts_omit_identityfederation,ts_omit_ipnbus,ts_omit_iptables,ts_omit_kube,ts_omit_linkspeed,ts_omit_linuxdnsfight,ts_omit_listenrawdisco,ts_omit_logtail,ts_omit_netlog,ts_omit_networkmanager,ts_omit_oauthkey,ts_omit_osrouter,ts_omit_outboundproxy,ts_omit_peerapiclient,ts_omit_peerapiserver,ts_omit_portlist,ts_omit_portmapper,ts_omit_posture,ts_omit_qrcodes,ts_omit_relayserver,ts_omit_remoteconfig,ts_omit_resolved,ts_omit_routecheck,ts_omit_runtimemetrics,ts_omit_sdnotify,ts_omit_serve,ts_omit_serviceclientprefs,ts_omit_ssh,ts_omit_synology,ts_omit_syslog,ts_omit_syspolicy,ts_omit_systray,ts_omit_taildrop,ts_omit_tailnetlock,ts_omit_tap,ts_omit_tpm,ts_omit_tundevstats,ts_omit_unixsocketidentity,ts_omit_useexitnode,ts_omit_usermetrics,ts_omit_useroutes,ts_omit_wakeonlan,ts_omit_webbrowser,ts_omit_webclient"
)

// State 表示内置 Tailscale 节点的高层状态。
type State string

const (
	StateDisabled         State = "disabled"
	StateStarting         State = "starting"
	StateNeedsLogin       State = "needs_login"
	StateNeedsMachineAuth State = "needs_machine_auth"
	StateRunning          State = "running"
	StateStopped          State = "stopped"
	StateError            State = "error"
)

// Status 是节点状态快照，可直接序列化给桌面 UI、CLI 和手机端。
// DisableBindInterface 表示最近提交给进程级 netns 的策略意图，而非单个节点上的底层实测结果。
type Status struct {
	Enabled              bool     `json:"enabled"`
	State                State    `json:"state"`
	IP                   string   `json:"ip,omitempty"`
	DNSName              string   `json:"dnsName,omitempty"`
	Hostname             string   `json:"hostname,omitempty"`
	ControlURL           string   `json:"controlUrl,omitempty"`
	AuthURL              string   `json:"authUrl,omitempty"`
	DisableBindInterface bool     `json:"disableBindInterface,omitempty"`
	Error                string   `json:"error,omitempty"`
	Health               []string `json:"health,omitempty"`
}

// Equal 比较两个状态快照是否实质相同（用于变化通知去重）。
func (s Status) Equal(o Status) bool {
	if s.Enabled != o.Enabled || s.State != o.State || s.IP != o.IP ||
		s.DNSName != o.DNSName || s.Hostname != o.Hostname || s.ControlURL != o.ControlURL ||
		s.AuthURL != o.AuthURL || s.DisableBindInterface != o.DisableBindInterface ||
		s.Error != o.Error || len(s.Health) != len(o.Health) {
		return false
	}
	for i := range s.Health {
		if s.Health[i] != o.Health[i] {
			return false
		}
	}
	return true
}

// RouteKind 描述当前到某个对端节点的底层传输路径。
type RouteKind string

const (
	RouteUnknown   RouteKind = "unknown"
	RouteDirect    RouteKind = "direct"
	RoutePeerRelay RouteKind = "peer_relay"
	RouteDERP      RouteKind = "derp"
)

// PeerRoute 描述到指定 Tailscale 对端的实际路径信息。
type PeerRoute struct {
	Kind      RouteKind `json:"kind"`
	Addr      string    `json:"addr,omitempty"`      // 直连时的对端公网/局域网端点
	PeerRelay string    `json:"peerRelay,omitempty"` // 经中继节点转发的 UDP 地址
	Relay     string    `json:"relay,omitempty"`     // DERP 区域短名（如 hkg、sin、tok）
}

// IsDERP 表示当前活跃连接明确走 DERP 中继（非直连也非 PeerRelay）。
func (r PeerRoute) IsDERP() bool { return r.Kind == RouteDERP }

// ResetResult 分别报告 Reset 时的远端登出结果与本地状态清除结果。
type ResetResult struct {
	LoggedOut    bool
	LogoutErr    error
	LocalCleared bool
}

// Options 配置一个 Node 实例。
// 注意：Posse 不落盘 AuthKey 且启动后清除 Node/Server 上的引用；上游 controlclient 在本节点生命周期内仍会
// 保留传给它的 AuthKey 字段用于注册，且 AuthKey 为空时上游会回退检查 TS_AUTHKEY/TS_AUTH_KEY 环境变量。
type Options struct {
	StateDir             string
	Hostname             string
	ControlURL           string
	AuthKey              string
	DisableBindInterface bool
	Logf                 func(format string, args ...any)
	OnChange             func(Status)
	PollInterval         time.Duration // 状态轮询间隔，默认 1s
}

var (
	// ErrNotRunning 在节点尚未进入 Running 状态时由拨号/监听返回。
	ErrNotRunning = errors.New("embedded Tailscale is not running")
	// ErrNoTailscaleIP 在节点未分配 IPv4 地址时返回。
	ErrNoTailscaleIP = errors.New("embedded Tailscale has no IPv4 address")
	// ErrInvalidPort 在 ListenPacket / ListenTCP 收到越界端口时返回。
	ErrInvalidPort = errors.New("invalid port")
	// ErrInvalidTarget 在 DialUDP 收到非 Tailscale IPv4 或非法端口地址时返回。
	ErrInvalidTarget = errors.New("invalid Tailscale target address")
)

// Node 管理单个嵌入式 tsnet.Server 的完整生命周期（懒启动、状态跟踪、安全关闭与重置）。
//
// 锁序与并发约定：
//   - opMu 串行化 Start / Stop / Reset 三个生命周期事务，持 opMu 期间绝不获取 cbMu；
//   - mu 保护运行时字段（srv、lc、status、gen、seq、onChange 指针）与通知槽，只做极短内存操作；
//   - 外部回调 OnChange 仅在 drainNotifications 协程内持 cbMu 执行（绝不持 opMu/mu，也不在 pollLoop 内执行），
//     因此回调内可安全重入 Start/Stop/Reset；
//   - SetOnChange(nil) 仅用于外部撤销并等待在途回调退出（不可在回调自身内调用）。
type Node struct {
	opMu sync.Mutex

	mu         sync.Mutex
	opts       Options
	ownedDir   string // 经 ValidateStateDir 校验通过的专用状态目录
	srv        *tsnet.Server
	lc         *local.Client
	status     Status
	gen        uint64 // 每次启动/停止递增，阻止旧实例覆盖新状态
	nextSeq    uint64 // 同一代次内的刷新序号，阻止晚返回的旧 Status 回退新状态
	appliedSeq uint64
	pollStop   chan struct{}
	pollDone   chan struct{}
	logf       func(string, ...any)

	// 独立异步通知派发器：cbMu 仅由 drainNotifications 和外部 SetOnChange 获取，绝不与 opMu 嵌套。
	cbMu          sync.Mutex
	onChange      func(Status)
	notifyPending bool
	notifyStatus  Status
	notifyActive  bool
}

// New 创建一个尚未启动的 Node（未调用 Start 前零内存、零网络开销）。
// 不会保留 opts.AuthKey；若 opts.StateDir 不合法则不认领为 ownedDir。
func New(opts Options) *Node {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ownedDir, err := ValidateStateDir(opts.StateDir)
	if err != nil {
		ownedDir = ""
	}
	cleanOpts := Options{
		StateDir:             ownedDir,
		Hostname:             strings.TrimSpace(opts.Hostname),
		ControlURL:           strings.TrimSpace(opts.ControlURL),
		DisableBindInterface: opts.DisableBindInterface,
		PollInterval:         opts.PollInterval,
	}
	return &Node{
		opts:     cleanOpts,
		ownedDir: ownedDir,
		onChange: opts.OnChange,
		logf:     logf,
		status: Status{
			Enabled:              false,
			State:                StateDisabled,
			Hostname:             cleanOpts.Hostname,
			ControlURL:           cleanOpts.ControlURL,
			DisableBindInterface: cleanOpts.DisableBindInterface,
		},
	}
}

// SetOnChange 更新状态变化回调（非 nil 与 nil 均会通过 cbMu 等待正在执行的回调返回后再返回，
// 因此任何时候都只能从回调外部调用）。
// 传 nil 可保证调用方（如 Android Flutter 引擎销毁时的 hq_reset）释放回调资源后不会再被调用。
func (n *Node) SetOnChange(fn func(Status)) {
	n.cbMu.Lock()
	n.mu.Lock()
	n.onChange = fn
	if fn == nil {
		n.notifyPending = false
	}
	n.mu.Unlock()
	n.cbMu.Unlock()
}

// ApplyBindPolicy 设置进程级的 netns 网卡绑定开关（Darwin / Android），作用于此后创建的 socket。
func ApplyBindPolicy(disable bool, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	switch runtime.GOOS {
	case "darwin", "ios":
		netns.SetDisableBindConnToInterface(logf, disable)
	case "android":
		netns.SetDisableAndroidBindToActiveNetwork(logf, disable)
	}
}

// Start 启动内置 Tailscale 节点。
// 幂等条件：节点已启动且 StateDir、Hostname、ControlURL、DisableBindInterface 均一致，且本次未传入新 AuthKey 时直接返回。
// 否则先完整停掉旧实例再启动新实例。启动完全成功前不会将 srv 暴露给数据面，也不会改写 ownedDir。
// 注意：若 opts.OnChange != nil，仅更新回调指针而不等待在途回调，因此可在回调内安全重入 Start。
func (n *Node) Start(opts Options) error {
	if err := ValidateControlURL(opts.ControlURL); err != nil {
		return err
	}
	stateDir, err := ValidateStateDir(opts.StateDir)
	if err != nil {
		return err
	}

	hostname := SanitizeHostname(opts.Hostname, DefaultHostname("posse"))
	controlURL := strings.TrimSpace(opts.ControlURL)
	authKey := strings.TrimSpace(opts.AuthKey)
	cleanOpts := Options{
		StateDir:             stateDir,
		Hostname:             hostname,
		ControlURL:           controlURL,
		DisableBindInterface: opts.DisableBindInterface,
		PollInterval:         opts.PollInterval,
	}

	n.opMu.Lock()
	defer n.opMu.Unlock()

	// 只在 mu 下替换回调指针，绝不获取 cbMu，避免与回调内重入 Start/Stop 产生 opMu <-> cbMu 死锁。
	n.mu.Lock()
	if opts.OnChange != nil {
		n.onChange = opts.OnChange
	}
	if n.srv != nil && authKey == "" &&
		n.opts.StateDir == cleanOpts.StateDir &&
		n.opts.Hostname == cleanOpts.Hostname &&
		n.opts.ControlURL == cleanOpts.ControlURL &&
		n.opts.DisableBindInterface == cleanOpts.DisableBindInterface {
		n.mu.Unlock()
		return nil
	}
	logf := opts.Logf
	if logf == nil {
		logf = n.logf
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	n.logf = logf
	n.mu.Unlock()

	// 先完整停止旧实例（仍在 opMu 下串行执行）
	_ = n.stopLocked(false)

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		n.mu.Lock()
		n.gen++
		gen := n.gen
		n.mu.Unlock()
		n.commitError(gen, cleanOpts, err)
		return err
	}

	ApplyBindPolicy(cleanOpts.DisableBindInterface, logf)

	// 显式指定默认控制面，避免宿主环境变量 TS_CONTROL_URL 静默劫持控制服务器。
	effectiveControlURL := controlURL
	if effectiveControlURL == "" {
		effectiveControlURL = ipn.DefaultControlURL
	}

	srv := &tsnet.Server{
		Dir:        stateDir,
		Hostname:   hostname,
		ControlURL: effectiveControlURL,
		AuthKey:    authKey,
		Ephemeral:  false,
		Logf:       func(string, ...any) {},
		UserLogf: func(format string, args ...any) {
			logf("tslink: "+format, args...)
		},
	}
	authKey = ""

	n.mu.Lock()
	n.gen++
	gen := n.gen
	n.nextSeq = 0
	n.appliedSeq = 0
	n.mu.Unlock()

	n.commitStatus(gen, 0, Status{
		Enabled:              true,
		State:                StateStarting,
		Hostname:             hostname,
		ControlURL:           controlURL,
		DisableBindInterface: cleanOpts.DisableBindInterface,
	})

	// 在发布给数据面和更新 ownedDir 之前完成全部启动，避免未初始化的 srv 被并发调用 IPv4()/Close()
	if err := srv.Start(); err != nil {
		srv.AuthKey = ""
		_ = srv.Close()
		n.commitError(gen, cleanOpts, err)
		return err
	}
	srv.AuthKey = ""

	lc, err := srv.LocalClient()
	if err != nil {
		_ = srv.Close()
		n.commitError(gen, cleanOpts, err)
		return err
	}

	pollStop := make(chan struct{})
	pollDone := make(chan struct{})
	interval := cleanOpts.PollInterval
	if interval <= 0 {
		interval = time.Second
	}

	n.mu.Lock()
	n.opts = cleanOpts
	n.ownedDir = stateDir
	n.srv = srv
	n.lc = lc
	n.pollStop = pollStop
	n.pollDone = pollDone
	n.mu.Unlock()

	// 先拉一次初始状态，再启动后台轮询；所有回调经独立派发器异步投递，不阻塞 Start 或轮询。
	n.refreshOnce(gen, srv, lc)
	go n.pollLoop(gen, srv, lc, interval, pollStop, pollDone)
	return nil
}

// Stop 关闭正在运行的 tsnet.Server 并释放资源；可重复调用，且可在状态回调内安全重入。
func (n *Node) Stop() error {
	n.opMu.Lock()
	defer n.opMu.Unlock()
	return n.stopLocked(true)
}

func (n *Node) stopLocked(emitDisabled bool) error {
	n.mu.Lock()
	srv := n.srv
	pollStop := n.pollStop
	pollDone := n.pollDone
	opts := n.opts
	n.srv = nil
	n.lc = nil
	n.pollStop = nil
	n.pollDone = nil
	n.gen++
	gen := n.gen
	n.nextSeq = 0
	n.appliedSeq = 0
	n.mu.Unlock()

	if pollStop != nil {
		close(pollStop)
		<-pollDone
	}
	var err error
	if srv != nil {
		err = srv.Close()
	}
	if emitDisabled {
		n.commitStatus(gen, 0, Status{
			Enabled:              false,
			State:                StateDisabled,
			Hostname:             opts.Hostname,
			ControlURL:           opts.ControlURL,
			DisableBindInterface: opts.DisableBindInterface,
		})
	}
	return err
}

// Reset 登出当前节点（若运行中）、停止节点并仅清理本节点拥有的状态目录中的已知 Tailscale 状态文件。
// 不会删除目录里的无关文件；仅当目录变空时移除该目录本身。
// 与 Start/Stop 串行执行，并分别报告远端登出结果与本地清理结果。
func (n *Node) Reset() (ResetResult, error) {
	n.opMu.Lock()
	defer n.opMu.Unlock()

	n.mu.Lock()
	lc := n.lc
	dir := n.ownedDir
	n.mu.Unlock()

	var res ResetResult
	if lc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := lc.Logout(ctx)
		cancel()
		if err != nil {
			res.LogoutErr = err
		} else {
			res.LoggedOut = true
		}
	}

	stopErr := n.stopLocked(true)

	if dir == "" {
		return res, errors.New("Tailscale state directory is not set")
	}
	if err := removeTailscaleState(dir); err != nil {
		return res, err
	}
	res.LocalCleared = true
	return res, stopErr
}

// removeTailscaleState 仅清理 Tailscale/tsnet 自身产生的状态文件与子目录，绝不误删同目录内的无关文件；
// 清理完若目录已空则顺带移除空目录。
func removeTailscaleState(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !isTailscaleStateEntry(name) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	// 仅在目录已空时尝试删除目录本身，非空（有无关文件）时保留
	_ = os.Remove(dir)
	return nil
}

func isTailscaleStateEntry(name string) bool {
	switch name {
	case "tailscaled.state", "tailscaled.log.conf", "tailscaled.sock", "profile-data", "certs":
		return true
	}
	return strings.HasPrefix(name, "tailscaled.state.") ||
		strings.HasPrefix(name, "tailscaled.log.conf.")
}

// StartLoginInteractive 异步向控制面请求交互式登录（返回 nil 仅表示请求已被上游接受）。
// 注意：上游可能复用仍有效的 AuthURL，也可能需要异步向控制面拉取新链接；
// 本方法不会主动覆写或回退当前状态，最新 AuthURL 通过后续状态轮询/回调获取。
func (n *Node) StartLoginInteractive(ctx context.Context) error {
	n.mu.Lock()
	srv := n.srv
	lc := n.lc
	gen := n.gen
	n.mu.Unlock()
	if lc == nil || srv == nil {
		return ErrNotRunning
	}
	if err := lc.StartLoginInteractive(ctx); err != nil {
		return err
	}
	n.refreshOnce(gen, srv, lc)
	return nil
}

// Status 返回当前缓存的状态快照（不阻塞）。
func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	return cloneStatus(n.status)
}

// IsRunning 返回节点是否已处于 Running 且拿到 IPv4 地址。
func (n *Node) IsRunning() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.srv != nil && n.status.State == StateRunning && n.status.IP != ""
}

// Generation 返回当前运行中节点的代次；未运行时返回 0。
// 即使重启后拿到相同 Tailscale IP，代次也会递增，方便监听方识别旧监听已失效并重建。
func (n *Node) Generation() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.srv == nil || n.status.State != StateRunning {
		return 0
	}
	return n.gen
}

// IPv4 返回当前节点分配的 Tailscale IPv4 地址（仅在初始化完成且可用时非零）。
func (n *Node) IPv4() (netip.Addr, bool) {
	n.mu.Lock()
	srv := n.srv
	n.mu.Unlock()
	if srv == nil {
		return netip.Addr{}, false
	}
	v4, _ := srv.TailscaleIPs()
	return pickIPv4([]netip.Addr{v4})
}

// ListenPacket 在当前节点的 Tailscale IPv4 上监听指定 UDP 端口（port 须在 0..65535，0 为随机端口）。
func (n *Node) ListenPacket(port int) (net.PacketConn, error) {
	if port < 0 || port > 65535 {
		return nil, ErrInvalidPort
	}
	n.mu.Lock()
	srv := n.srv
	gen := n.gen
	st := n.status.State
	n.mu.Unlock()
	if srv == nil || st != StateRunning {
		return nil, ErrNotRunning
	}
	v4, _ := srv.TailscaleIPs()
	ip, ok := pickIPv4([]netip.Addr{v4})
	if !ok {
		return nil, ErrNoTailscaleIP
	}
	addr := netip.AddrPortFrom(ip, uint16(port)).String()
	pc, err := srv.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	// 复核代次：若注册期间并发 Stop/Start 已切代，立即关闭陈旧监听，避免泄漏不可退出的监听器。
	n.mu.Lock()
	stale := n.gen != gen || n.srv != srv
	n.mu.Unlock()
	if stale {
		_ = pc.Close()
		return nil, ErrNotRunning
	}
	return pc, nil
}

// ListenTCP 在当前节点的 Tailscale IPv4 上监听指定 TCP 端口（port 须在 0..65535）。
// 供 Mac 端为官方 Tailscale 手机首次扫码配对提供 WebSocket 入口。
func (n *Node) ListenTCP(port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, ErrInvalidPort
	}
	n.mu.Lock()
	srv := n.srv
	gen := n.gen
	st := n.status.State
	n.mu.Unlock()
	if srv == nil || st != StateRunning {
		return nil, ErrNotRunning
	}
	v4, _ := srv.TailscaleIPs()
	ip, ok := pickIPv4([]netip.Addr{v4})
	if !ok {
		return nil, ErrNoTailscaleIP
	}
	addr := netip.AddrPortFrom(ip, uint16(port)).String()
	ln, err := srv.Listen("tcp4", addr)
	if err != nil {
		return nil, err
	}
	// 复核代次：若注册期间并发 Stop/Start 已切代，立即关闭陈旧监听器，防止 Accept 永久阻塞。
	n.mu.Lock()
	stale := n.gen != gen || n.srv != srv
	n.mu.Unlock()
	if stale {
		_ = ln.Close()
		return nil, ErrNotRunning
	}
	return ln, nil
}

// DialUDP 通过 tsnet 的纯用户态 netstack 建立到对端 Tailscale IPv4:port 的定向 UDP 连接。
// 底层基于 ListenPacket 包装，物理上只经过 tsnet netstack，绝不会回退到宿主系统 UDP。
func (n *Node) DialUDP(ctx context.Context, addr string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ap, err := netip.ParseAddrPort(strings.TrimSpace(addr))
	if err != nil || ap.Port() == 0 || !IsTailscaleIP(ap.Addr()) {
		return nil, ErrInvalidTarget
	}
	pc, err := n.ListenPacket(0)
	if err != nil {
		return nil, err
	}
	target := netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	return &directedPacketConn{
		pc:     pc,
		remote: net.UDPAddrFromAddrPort(target),
		target: target,
	}, nil
}

// directedPacketConn 把 tsnet netstack 的 net.PacketConn 适配为定向 net.Conn：
// 只向 target 发包、只接受来自 target 的回包，确保绝不触发 tsdial 的宿主 UDP 回退路径。
type directedPacketConn struct {
	pc     net.PacketConn
	remote *net.UDPAddr
	target netip.AddrPort
}

func (c *directedPacketConn) Read(b []byte) (int, error) {
	for {
		n, from, err := c.pc.ReadFrom(b)
		if err != nil {
			return 0, err
		}
		if sameUDPTarget(from, c.target) {
			return n, nil
		}
	}
}

func (c *directedPacketConn) Write(b []byte) (int, error) {
	return c.pc.WriteTo(b, c.remote)
}

func (c *directedPacketConn) Close() error                       { return c.pc.Close() }
func (c *directedPacketConn) LocalAddr() net.Addr                { return c.pc.LocalAddr() }
func (c *directedPacketConn) RemoteAddr() net.Addr               { return c.remote }
func (c *directedPacketConn) SetDeadline(t time.Time) error      { return c.pc.SetDeadline(t) }
func (c *directedPacketConn) SetReadDeadline(t time.Time) error  { return c.pc.SetReadDeadline(t) }
func (c *directedPacketConn) SetWriteDeadline(t time.Time) error { return c.pc.SetWriteDeadline(t) }

func sameUDPTarget(from net.Addr, want netip.AddrPort) bool {
	if from == nil {
		return false
	}
	if u, ok := from.(*net.UDPAddr); ok && u != nil {
		ip, ok := netip.AddrFromSlice(u.IP)
		return ok && ip.Unmap() == want.Addr() && uint16(u.Port) == want.Port()
	}
	ap, err := netip.ParseAddrPort(from.String())
	return err == nil && ap.Addr().Unmap() == want.Addr() && ap.Port() == want.Port()
}

// InjectLinkChange 向底层 netmon 注入网络变化事件，请求上游重新检查接口并在需要时触发 Rebind/ReSTUN。
// 用于 iOS 从后台冻结唤醒或物理网络切换后加速恢复连接。
func (n *Node) InjectLinkChange() {
	n.mu.Lock()
	srv := n.srv
	n.mu.Unlock()
	if srv == nil {
		return
	}
	sys := srv.Sys()
	if sys == nil {
		return
	}
	if nm, ok := sys.NetMon.GetOK(); ok && nm != nil {
		nm.InjectEvent()
	}
}

// PeerRoute 查询到指定对端 Tailscale IP 的当前路由状态（直连、PeerRelay、DERP 或未知）。
func (n *Node) PeerRoute(ctx context.Context, peerIP netip.Addr) PeerRoute {
	peerIP = peerIP.Unmap()
	if !peerIP.IsValid() {
		return PeerRoute{Kind: RouteUnknown}
	}
	n.mu.Lock()
	lc := n.lc
	n.mu.Unlock()
	if lc == nil {
		return PeerRoute{Kind: RouteUnknown}
	}
	st, err := lc.Status(ctx)
	if err != nil || st == nil {
		return PeerRoute{Kind: RouteUnknown}
	}
	return ClassifyPeerRoute(st, peerIP)
}

// PeerRouteByAddr 从 net.Addr（如 *net.UDPAddr）中提取 IP 并查询其路由状态。
func (n *Node) PeerRouteByAddr(ctx context.Context, addr net.Addr) PeerRoute {
	if addr == nil {
		return PeerRoute{Kind: RouteUnknown}
	}
	if u, ok := addr.(*net.UDPAddr); ok && u != nil {
		if ip, ok := netip.AddrFromSlice(u.IP); ok {
			return n.PeerRoute(ctx, ip)
		}
	}
	if ap, err := netip.ParseAddrPort(addr.String()); err == nil {
		return n.PeerRoute(ctx, ap.Addr())
	}
	return PeerRoute{Kind: RouteUnknown}
}

// ClassifyPeerRoute 从 ipnstate.Status 中提取指定对端 IP 的当前路径分类：
//   - CurAddr 非空 -> RouteDirect（P2P 直连）；
//   - PeerRelay 非空 -> RoutePeerRelay（经其他 Tailscale 节点的 UDP 中继）；
//   - Active 为真且 Relay 非空 -> RouteDERP（当前活跃通信走 DERP 中继）；
//   - 否则返回 RouteUnknown（避免把仅预填 home DERP 区域或仅有历史流量的空闲对端误判为当前 DERP）。
func ClassifyPeerRoute(st *ipnstate.Status, peerIP netip.Addr) PeerRoute {
	if st == nil || !peerIP.IsValid() {
		return PeerRoute{Kind: RouteUnknown}
	}
	peerIP = peerIP.Unmap()
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		matched := false
		for _, ip := range p.TailscaleIPs {
			if ip.Unmap() == peerIP {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		relay := strings.TrimSpace(p.Relay)
		if cur := strings.TrimSpace(p.CurAddr); cur != "" {
			return PeerRoute{Kind: RouteDirect, Addr: cur, Relay: relay}
		}
		if pr := strings.TrimSpace(p.PeerRelay); pr != "" {
			return PeerRoute{Kind: RoutePeerRelay, PeerRelay: pr, Relay: relay}
		}
		if p.Active && relay != "" {
			return PeerRoute{Kind: RouteDERP, Relay: relay}
		}
		return PeerRoute{Kind: RouteUnknown, Relay: relay}
	}
	return PeerRoute{Kind: RouteUnknown}
}

// MapBackendState 把 Tailscale ipnstate.Status 里的 BackendState 映射为 State。
func MapBackendState(backendState string) State {
	switch backendState {
	case "Running":
		return StateRunning
	case "NeedsLogin":
		return StateNeedsLogin
	case "NeedsMachineAuth":
		return StateNeedsMachineAuth
	case "Starting", "NoState", "":
		return StateStarting
	case "Stopped":
		return StateStopped
	default:
		return StateStarting
	}
}

func pickIPv4(ips []netip.Addr) (netip.Addr, bool) {
	var fallback netip.Addr
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsValid() || !ip.Is4() {
			continue
		}
		if IsTailscaleIP(ip) {
			return ip, true
		}
		if !fallback.IsValid() {
			fallback = ip
		}
	}
	return fallback, fallback.IsValid()
}

func (n *Node) pollLoop(gen uint64, srv *tsnet.Server, lc *local.Client, interval time.Duration, stop, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			n.refreshOnce(gen, srv, lc)
		}
	}
}

func (n *Node) refreshOnce(gen uint64, srv *tsnet.Server, lc *local.Client) {
	n.mu.Lock()
	if n.gen != gen || n.srv != srv {
		n.mu.Unlock()
		return
	}
	n.nextSeq++
	seq := n.nextSeq
	opts := n.opts
	n.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	st, err := lc.StatusWithoutPeers(ctx)
	cancel()
	if err != nil || st == nil {
		return
	}

	next := Status{
		Enabled:              true,
		State:                MapBackendState(st.BackendState),
		Hostname:             opts.Hostname,
		ControlURL:           opts.ControlURL,
		AuthURL:              strings.TrimSpace(st.AuthURL),
		DisableBindInterface: opts.DisableBindInterface,
	}
	if ip, ok := pickIPv4(st.TailscaleIPs); ok {
		next.IP = ip.String()
	}
	if st.Self != nil {
		next.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		if next.State == StateRunning && st.Self.HostName != "" {
			next.Hostname = st.Self.HostName
		}
	}
	if len(st.Health) > 0 {
		next.Health = append([]string(nil), st.Health...)
	}
	n.commitStatus(gen, seq, next)
}

func (n *Node) commitError(gen uint64, opts Options, err error) {
	n.commitStatus(gen, 0, Status{
		Enabled:              true,
		State:                StateError,
		Hostname:             opts.Hostname,
		ControlURL:           opts.ControlURL,
		DisableBindInterface: opts.DisableBindInterface,
		Error:                err.Error(),
	})
}

// commitStatus 在同一把锁内校验代次（gen）与同代刷新序号（seq > 0 时必须单调递增），
// 通过后原子提交状态并异步唤醒独立派发器，绝不在锁内或轮询 goroutine 内执行外部回调。
func (n *Node) commitStatus(gen, seq uint64, next Status) {
	n.mu.Lock()
	if n.gen != gen {
		n.mu.Unlock()
		return
	}
	if seq > 0 {
		if seq < n.appliedSeq {
			n.mu.Unlock()
			return
		}
		n.appliedSeq = seq
	}
	if n.status.Equal(next) {
		n.mu.Unlock()
		return
	}
	snap := cloneStatus(next)
	n.status = snap
	if n.onChange == nil {
		n.mu.Unlock()
		return
	}
	n.notifyStatus = cloneStatus(snap)
	n.notifyPending = true
	if !n.notifyActive {
		n.notifyActive = true
		go n.drainNotifications()
	}
	n.mu.Unlock()
}

func (n *Node) drainNotifications() {
	for {
		n.cbMu.Lock()
		n.mu.Lock()
		if !n.notifyPending || n.onChange == nil {
			n.notifyPending = false
			n.notifyActive = false
			n.mu.Unlock()
			n.cbMu.Unlock()
			return
		}
		cb := n.onChange
		snap := cloneStatus(n.notifyStatus)
		n.notifyPending = false
		n.mu.Unlock()

		// 仅持 cbMu，绝不持 mu/opMu：
		// - 回调可安全重入 Start(含 OnChange)/Stop/Reset；
		// - 外部 SetOnChange(nil) 获取 cbMu 时会等待正在执行的这一次回调返回后再返回。
		cb(snap)
		n.cbMu.Unlock()
	}
}

func cloneStatus(s Status) Status {
	if len(s.Health) > 0 {
		s.Health = append([]string(nil), s.Health...)
	}
	return s
}

// String 返回便于日志输出的简短状态摘要。
func (s Status) String() string {
	if s.IP != "" {
		return fmt.Sprintf("%s (%s)", s.State, s.IP)
	}
	if s.Error != "" {
		return fmt.Sprintf("%s: %s", s.State, s.Error)
	}
	return string(s.State)
}
