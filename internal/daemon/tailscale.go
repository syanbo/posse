package daemon

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"strings"
	"time"

	"posse/internal/rpc"
	"posse/internal/tslink"
)

// daemonTSProvider 把常驻进程内置的 tslink.Node 适配为 mobile.TSProvider。
type daemonTSProvider struct{ d *Daemon }

func (p daemonTSProvider) IP() string {
	if p.d.ts == nil || !p.d.ts.IsRunning() {
		return ""
	}
	ip, ok := p.d.ts.IPv4()
	if !ok {
		return ""
	}
	return ip.String()
}

func (p daemonTSProvider) Generation() uint64 {
	if p.d.ts == nil {
		return 0
	}
	return p.d.ts.Generation()
}

func (p daemonTSProvider) ListenPacket(port int) (net.PacketConn, error) {
	if p.d.ts == nil {
		return nil, tslink.ErrNotRunning
	}
	return p.d.ts.ListenPacket(port)
}

func (p daemonTSProvider) ListenTCP(port int) (net.Listener, error) {
	if p.d.ts == nil {
		return nil, tslink.ErrNotRunning
	}
	return p.d.ts.ListenTCP(port)
}

// newTailscale 仅创建未启动的 tslink.Node（零内存、零网络）；
// 真正启动要等拿到 possed.lock 后在 startTailscale 中执行，避免交接时双实例共用 StateDir 互踢。
func (d *Daemon) newTailscale() {
	d.ts = tslink.New(tslink.Options{
		StateDir: tslink.StateDir(d.cfg.Home),
		Hostname: tslink.DefaultHostname("posse"),
		Logf:     log.Printf,
		OnChange: func(st tslink.Status) {
			log.Printf("tailscale: %s", st.String())
			if d.mobile != nil {
				d.mobile.TailscaleChanged()
			}
		},
	})
}

// startTailscale 在拿到 possed.lock 后调用：仅当未进入关停且 tailscale.json 中 enabled=true 时懒启动节点。
func (d *Daemon) startTailscale() {
	d.tsMu.Lock()
	defer d.tsMu.Unlock()
	if d.tsStopped {
		return
	}
	d.tsAllowed = true
	if d.ts == nil {
		return
	}
	cfg, err := tslink.LoadConfig(d.cfg.Home)
	if err != nil {
		log.Printf("tailscale: load config: %v", err)
		return
	}
	if !cfg.Enabled {
		return
	}
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = tslink.DefaultHostname("posse")
	}
	if err := d.ts.Start(tslink.Options{
		StateDir:             tslink.StateDir(d.cfg.Home),
		Hostname:             hostname,
		ControlURL:           cfg.ControlURL,
		DisableBindInterface: cfg.DisableBindInterface,
		Logf:                 log.Printf,
	}); err != nil {
		log.Printf("tailscale: start: %v", err)
	}
}

// stopTailscale 在退出或交接释放 possed.lock 之前关闭内置 Tailscale 节点，并永久关闭本次进程内的启动门禁。
func (d *Daemon) stopTailscale() {
	d.tsMu.Lock()
	d.tsStopped = true
	d.tsAllowed = false
	node := d.ts
	d.tsMu.Unlock()
	if node == nil {
		return
	}
	node.SetOnChange(nil)
	if err := node.Stop(); err != nil {
		log.Printf("tailscale: stop: %v", err)
	}
}

func (d *Daemon) tailscaleStatus(_ context.Context, _ json.RawMessage) (any, error) {
	cfg, err := tslink.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	d.tsMu.Lock()
	allowed := d.tsAllowed
	node := d.ts
	d.tsMu.Unlock()

	if node == nil {
		return tslink.Status{
			Enabled:              cfg.Enabled,
			State:                tslink.StateDisabled,
			Hostname:             cfg.Hostname,
			ControlURL:           cfg.ControlURL,
			DisableBindInterface: cfg.DisableBindInterface,
		}, nil
	}
	st := node.Status()
	if !allowed && cfg.Enabled && st.State == tslink.StateDisabled {
		st.Enabled = true
		st.State = tslink.StateStarting
		st.Hostname = cfg.Hostname
		st.ControlURL = cfg.ControlURL
		st.DisableBindInterface = cfg.DisableBindInterface
	}
	return mergeDisabledConfig(st, cfg), nil
}

// mergeDisabledConfig 在节点处于禁用态时，把磁盘中已保存的配置合并到返回快照，
// 避免未启动或刚关闭的节点回显旧的 runtime opts。
func mergeDisabledConfig(st tslink.Status, cfg tslink.Config) tslink.Status {
	if !cfg.Enabled && st.State == tslink.StateDisabled {
		st.Hostname = cfg.Hostname
		st.ControlURL = cfg.ControlURL
		st.DisableBindInterface = cfg.DisableBindInterface
	}
	return st
}

type tailscaleSetParams struct {
	Enabled              *bool   `json:"enabled"`
	Hostname             *string `json:"hostname"`
	ControlURL           *string `json:"controlUrl"`
	AuthKey              string  `json:"authKey"` // 一次性登录密钥，不写盘
	DisableBindInterface *bool   `json:"disableBindInterface"`
}

func (d *Daemon) tailscaleSet(_ context.Context, raw json.RawMessage) (any, error) {
	var p tailscaleSetParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	d.tsMu.Lock()
	defer d.tsMu.Unlock()
	if !d.tsAllowed || d.ts == nil {
		return nil, rpc.Errorf(rpc.CodeRestarting, "daemon is still starting; try again in a moment")
	}
	cfg, err := tslink.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	if p.Enabled != nil {
		cfg.Enabled = *p.Enabled
	}
	if p.Hostname != nil {
		cfg.Hostname = strings.TrimSpace(*p.Hostname)
	}
	if p.ControlURL != nil {
		cfg.ControlURL = strings.TrimSpace(*p.ControlURL)
	}
	if p.DisableBindInterface != nil {
		cfg.DisableBindInterface = *p.DisableBindInterface
	}
	if err := tslink.ValidateConfig(cfg); err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	if err := tslink.SaveConfig(d.cfg.Home, cfg); err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	if !cfg.Enabled {
		_ = d.ts.Stop()
		if d.mobile != nil {
			d.mobile.TailscaleChanged()
		}
		return mergeDisabledConfig(d.ts.Status(), cfg), nil
	}
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = tslink.DefaultHostname("posse")
	}
	if err := d.ts.Start(tslink.Options{
		StateDir:             tslink.StateDir(d.cfg.Home),
		Hostname:             hostname,
		ControlURL:           cfg.ControlURL,
		AuthKey:              strings.TrimSpace(p.AuthKey),
		DisableBindInterface: cfg.DisableBindInterface,
		Logf:                 log.Printf,
	}); err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	if d.mobile != nil {
		d.mobile.TailscaleChanged()
	}
	return d.ts.Status(), nil
}

func (d *Daemon) tailscaleLogin(ctx context.Context, _ json.RawMessage) (any, error) {
	d.tsMu.Lock()
	allowed := d.tsAllowed
	node := d.ts
	d.tsMu.Unlock()
	if !allowed || node == nil {
		return nil, rpc.Errorf(rpc.CodeRestarting, "daemon is still starting; try again in a moment")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := node.StartLoginInteractive(cctx); err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	return node.Status(), nil
}

type tailscaleResetResult struct {
	Status       tslink.Status `json:"status"`
	LoggedOut    bool          `json:"loggedOut"`
	LogoutError  string        `json:"logoutError,omitempty"`
	LocalCleared bool          `json:"localCleared"`
}

func (d *Daemon) tailscaleReset(_ context.Context, _ json.RawMessage) (any, error) {
	d.tsMu.Lock()
	defer d.tsMu.Unlock()
	if !d.tsAllowed || d.ts == nil {
		return nil, rpc.Errorf(rpc.CodeRestarting, "daemon is still starting; try again in a moment")
	}
	cfg, err := tslink.LoadConfig(d.cfg.Home)
	if err != nil {
		// 即使旧配置损坏也允许重置
		cfg = tslink.Config{}
	}
	cfg.Enabled = false
	if err := tslink.SaveConfig(d.cfg.Home, cfg); err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	res, resetErr := d.ts.Reset()
	if d.mobile != nil {
		d.mobile.TailscaleChanged()
	}
	if resetErr != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", resetErr)
	}
	out := tailscaleResetResult{
		Status:       mergeDisabledConfig(d.ts.Status(), cfg),
		LoggedOut:    res.LoggedOut,
		LocalCleared: res.LocalCleared,
	}
	if res.LogoutErr != nil {
		out.LogoutError = res.LogoutErr.Error()
	}
	return out, nil
}
