package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"
)

// 手机看 Mac 桌面（Hive 扩展 hive.desk.v1，方案见 docs/plans/phone-desktop.md）。
// 画面和输入走 UDP，这里只管订阅：desk.subscribe 是一个流，第一条是 ready（端口、编码、nonce），
// 之后推 state，结束时推 end。手机退订、连接断开都会停掉采集。

// DeskRequest 是一次订阅交给常驻进程的参数。
type DeskRequest struct {
	Device string // 手机的名字，桌面手机页显示"谁在看"
	// Secret 和 Nonce 派生 UDP 两个方向的密钥（desk.DeriveKeys）
	Secret   []byte
	Nonce    []byte
	LongEdge int      // 手机屏幕长边的像素，画面不比它大
	Codecs   []string // 手机能硬解的编码，按偏好排
}

// DeskReady 是采集已经开始、UDP 已经在听。
type DeskReady struct {
	Port  int    `json:"port"`
	Codec string `json:"codec"`
	// Relay 是 UDP 中转的地址，RelayToken 是这次观看在中转上的令牌；没配中转时为空
	Relay      string `json:"-"`
	RelayToken []byte `json:"-"`
	// Punch 是打洞的 Mac 一侧（desk.punch）：手机报来它接收 socket 的公网地址，
	// 返回 Mac 出口 socket 的公网地址并开始向手机发打洞包。没配中转时为空
	Punch func(ctx context.Context, phone string) (string, error) `json:"-"`
}

// DeskEvent 是订阅期间推给手机的事件。Type 是 state 或 end；State、Reason 是固定的词，见 docs/features/mobile.md。
type DeskEvent struct {
	Type   string `json:"type"`
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// DeskError 是订阅失败的理由，原样作为错误码交给手机（busy、locked、permission_screen 等）。
func DeskError(code, msg string) error { return &rpcError{code, msg} }

// deskSecret 是派生 UDP 密钥的材料：这条连接两个方向的会话密钥，主机发出的在前。
// 它们从不出现在线上，连接一断就作废。会话 ID 不能用：它明文写在每一帧的头里。
func (s *session) deskSecret() []byte {
	out := make([]byte, 0, 64)
	out = append(out, s.sendKey[:]...)
	return append(out, s.recvKey[:]...)
}

func (c *conn) deskSubscribe(id string, raw json.RawMessage) error {
	if !c.hasCapability("hive.desk.v1") {
		return c.fail(id, "unsupported", "client did not declare hive.desk.v1")
	}
	var p struct {
		Width  int      `json:"width"`
		Height int      `json:"height"`
		Codecs []string `json:"codecs"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Width <= 0 || p.Height <= 0 {
		return c.fail(id, "bad_params", "width and height are required")
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	req := DeskRequest{
		Device:   c.device.Name,
		Secret:   c.sess.deskSecret(),
		Nonce:    nonce,
		LongEdge: max(p.Width, p.Height),
		Codecs:   p.Codecs,
	}
	// 一条连接同时只看一次，新的订阅换掉旧的
	ctx, gen := c.startKeyedStream("desk")
	// 拉起采集要几百毫秒，不能占着这条连接串行处理请求的循环
	go func() {
		defer c.endStreamGen("desk", gen)
		ended := make(chan struct{})
		notify := func(e DeskEvent) {
			if ctx.Err() == nil || e.Type == "end" {
				c.stream(id, e)
			}
			if e.Type == "end" {
				close(ended)
			}
		}
		ready, err := c.srv.host.Desk(ctx, req, notify)
		if err != nil {
			c.replyOrFail(id, nil, err)
			return
		}
		frame := map[string]any{
			"type": "ready", "port": ready.Port, "codec": ready.Codec,
			"nonceB64": base64.StdEncoding.EncodeToString(nonce),
		}
		if ready.Relay != "" {
			frame["relay"] = ready.Relay
			frame["relayTokenB64"] = base64.StdEncoding.EncodeToString(ready.RelayToken)
		}
		c.smu.Lock()
		c.deskPunch, c.deskPunchGen = ready.Punch, gen
		c.smu.Unlock()
		defer func() {
			c.smu.Lock()
			if c.deskPunchGen == gen {
				c.deskPunch = nil
			}
			c.smu.Unlock()
		}()
		c.stream(id, frame)
		select {
		case <-ended:
		case <-c.ctx.Done():
		}
	}()
	return nil
}

// deskPunchRPC 是 desk.punch：这条连接正在看桌面、经中转收画面时，打洞改成直连。
func (c *conn) deskPunchRPC(id string, raw json.RawMessage) error {
	var p struct {
		Addr string `json:"addr"`
	}
	json.Unmarshal(raw, &p)
	c.smu.Lock()
	punch := c.deskPunch
	c.smu.Unlock()
	if punch == nil {
		return c.fail(id, "unavailable", "not watching the desktop through a relay")
	}
	// 探测要等中转回一趟，不占住这条连接的请求队列
	go func() {
		ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
		defer cancel()
		addr, err := punch(ctx, p.Addr)
		c.replyOrFail(id, map[string]string{"addr": addr}, err)
	}()
	return nil
}

func (c *conn) deskUnsubscribe(id string) error {
	c.stopStream("desk")
	return c.reply(id, map[string]any{"unsubscribed": true})
}
