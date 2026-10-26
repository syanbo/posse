package daemon

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"posse/internal/desk"
	"posse/internal/deskrelay"
	"posse/internal/rpc"
)

// relayStatus 是手机页「外网中转」一栏要的：配没配、是不是管理员、现在能不能用。
// 不带中转地址：界面不显示它。
type relayStatus struct {
	Configured bool   `json:"configured"`
	Admin      bool   `json:"admin"`
	Name       string `json:"name,omitempty"`
	// State：none 没配、ready 能用、denied 密钥不对、revoked 被停用、clock 本机时间不准、unreachable 中转不应答
	State string `json:"state"`
}

func (d *Daemon) relayStatus(ctx context.Context, _ json.RawMessage) (any, error) {
	cfg, err := deskrelay.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	return checkRelay(ctx, cfg), nil
}

// checkRelay 向中转问一次这份设置能不能用，最多等 1.5 秒。
func checkRelay(ctx context.Context, cfg deskrelay.Config) relayStatus {
	st := relayStatus{Configured: cfg.Relay != "", Admin: cfg.Admin(), Name: cfg.Name, State: "none"}
	if !st.Configured {
		return st
	}
	// 和真正登记走同一块网卡，Surge 这类代理接管默认路由时也一样
	conn, err := desk.ListenPhysical()
	if err != nil {
		conn, err = net.ListenUDP("udp4", nil)
	}
	if err != nil {
		st.State = "unreachable"
		return st
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	r, err := deskrelay.CheckRelay(ctx, conn, cfg.Relay, cfg.Credential())
	switch {
	case err != nil:
		st.State = "unreachable"
	case r == deskrelay.CheckOK:
		st.State = "ready"
	case r == deskrelay.CheckRevoked:
		st.State = "revoked"
	case r == deskrelay.CheckClock:
		st.State = "clock"
	default:
		st.State = "denied"
	}
	return st
}

type relaySetParams struct {
	Code string `json:"code"`
}

// relaySet 把邀请码或管理员码写进 desk.json，马上按新设置向中转登记。
func (d *Daemon) relaySet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p relaySetParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	cfg, err := deskrelay.ParseCode(p.Code)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	if err := deskrelay.SaveConfig(d.cfg.Home, cfg); err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	if d.mobile != nil {
		d.mobile.RelayChanged()
	}
	return checkRelay(ctx, cfg), nil
}

func (d *Daemon) relayClear(ctx context.Context, _ json.RawMessage) (any, error) {
	if err := deskrelay.SaveConfig(d.cfg.Home, deskrelay.Config{}); err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	if d.mobile != nil {
		d.mobile.RelayChanged()
	}
	return relayStatus{State: "none"}, nil
}

type relayInviteParams struct {
	Name string `json:"name"`
}

// relayInvite 给别人生成邀请码，只有存着主密钥的 Mac 能生成。
func (d *Daemon) relayInvite(ctx context.Context, raw json.RawMessage) (any, error) {
	var p relayInviteParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	cfg, err := deskrelay.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	code, err := cfg.Invite(p.Name)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	return map[string]string{"code": code}, nil
}
