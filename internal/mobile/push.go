package mobile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"posse/internal/deskrelay"
)

// 手机推送（posse.push.v1，方案见 docs/plans/push.md）：手机经 posse.push.register 交来 APNs token 和推送密钥，
// 常驻进程在 agent 等你处理、完成没看时调 Notify，这里加密内容、经中转交给 APNs。

// PushTarget 是一台手机的推送注册，按手机安装 ID 存在 mobile.json 里，这台手机的配对都移除时一起删。
type PushTarget struct {
	Phone   string    `json:"phone"`
	Token   string    `json:"token"` // APNs 设备 token，hex
	Key     string    `json:"key"`   // 加密通知内容的 AES-256 密钥，base64，手机生成
	Sandbox bool      `json:"sandbox,omitempty"`
	Waiting bool      `json:"waiting"`          // 推不推等你处理
	Done    bool      `json:"done"`             // 推不推完成没看
	Alerts  *bool     `json:"alerts,omitempty"` // nil 沿用旧版按类型推通知和角标
	Badge   *bool     `json:"badge,omitempty"`
	At      time.Time `json:"at"`
}

// 推送的种类，也是加密内容里的 k。
const (
	PushWaiting = "waiting"
	PushDone    = "done"
)

// presenceFresh 是多久内收到过手机的包、又报过在前台，算它正开着 App（手机 5 秒 ping 一次，挂起后就停）。
const presenceFresh = 12 * time.Second

func (p *pairing) setPush(t PushTarget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Push = slices.DeleteFunc(p.st.Push, func(o PushTarget) bool { return o.Phone == t.Phone || o.Token == t.Token })
	if t.Token != "" {
		p.st.Push = append(p.st.Push, t)
	}
	p.save()
}

func (p *pairing) dropPush(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.st.Push)
	p.st.Push = slices.DeleteFunc(p.st.Push, func(o PushTarget) bool { return o.Token == token })
	if len(p.st.Push) != n {
		p.save()
	}
}

func (p *pairing) pushTargets() []PushTarget {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.st.Push)
}

// prunePushLocked 删掉已经没有配对的手机的注册。调用时持有 mu。
func (p *pairing) prunePushLocked() {
	p.st.Push = slices.DeleteFunc(p.st.Push, func(t PushTarget) bool {
		return !slices.ContainsFunc(p.st.Devices, func(d Device) bool { return d.Phone == t.Phone })
	})
}

// registerPush 处理 posse.push.register。token 为空表示这台手机关掉了推送。
func (c *conn) registerPush(id string, raw json.RawMessage) error {
	var a struct {
		Token   string `json:"token"`
		Key     string `json:"key"`
		Sandbox bool   `json:"sandbox"`
		Waiting bool   `json:"waiting"`
		Done    bool   `json:"done"`
		Alerts  *bool  `json:"alerts"`
		Badge   *bool  `json:"badge"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return c.fail(id, "bad_params", "push registration")
	}
	s := c.srv
	s.mu.Lock()
	phone := c.device.Phone
	s.mu.Unlock()
	if phone == "" {
		return c.fail(id, "bad_params", "push needs the phone's install ID")
	}
	t := PushTarget{Phone: phone, Waiting: a.Waiting, Done: a.Done, Sandbox: a.Sandbox, At: time.Now(), Alerts: a.Alerts, Badge: a.Badge}
	if a.Token != "" {
		tok, err1 := hex.DecodeString(a.Token)
		key, err2 := base64.StdEncoding.DecodeString(a.Key)
		if err1 != nil || len(tok) == 0 || len(tok) > 100 || err2 != nil || len(key) != 32 {
			return c.fail(id, "bad_params", "push registration")
		}
		t.Token, t.Key = strings.ToLower(a.Token), a.Key
	}
	s.pair.setPush(t)
	relay := false
	if s.q.opts.Relay != nil {
		relay = s.q.opts.Relay().Relay != ""
	}
	badgeOnly := true
	if relay && t.Token != "" && t.Badge != nil && *t.Badge &&
		(t.Alerts != nil && !*t.Alerts || !t.Waiting || !t.Done) {
		badgeOnly = s.probeBadgeOnly(t)
	}
	return c.reply(id, map[string]any{"relay": relay, "badgeOnly": badgeOnly})
}

// phoneInFront 是这台手机此刻开着 App、连着这台电脑：不用推，首页自己会显示。
func (s *Server) phoneInFront(phone string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixNano()
	for c := range s.conns {
		if c.device.Phone == phone && !c.background.Load() && now-c.lastRead.Load() < int64(presenceFresh) {
			return true
		}
	}
	return false
}

// pushBody 是加密前的通知内容，手机上的通知扩展按自己的界面语言写成标题和正文。
type pushBody struct {
	V        int    `json:"v"`
	Kind     string `json:"k"`
	Agent    string `json:"a,omitempty"`
	Project  string `json:"p,omitempty"`
	Space    string `json:"w,omitempty"` // 工作区显示的名字，和项目名相同时不给
	Tab      string `json:"t,omitempty"`
	Message  string `json:"m,omitempty"`
	Computer string `json:"c"` // 这台电脑的公钥，手机按它找到存着的电脑
	Name     string `json:"n,omitempty"`
	Worktree string `json:"wt"`
	Terminal string `json:"tab"` // 终端 ID，手机会话列表里的 terminal
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Notify 给注册过的手机推一条通知：kind 是 PushWaiting 或 PushDone，term 是常驻进程的终端 ID。
// 在后台发，不等结果；手机开着 App、关了这一类、电脑没配中转时不推。返回有没有发给哪台手机。
func (s *Server) Notify(term, kind string) bool {
	targets := s.pair.pushTargets()
	if len(targets) == 0 {
		return false
	}
	var cfg deskrelay.Config
	if s.q.opts.Relay != nil {
		cfg = s.q.opts.Relay()
	}
	if cfg.Relay == "" {
		return false
	}
	st := s.host.State()
	body, badge, ok := s.pushContent(st, term, kind)
	if !ok {
		return false
	}
	plain := pushPlain(body)
	sum := sha256.Sum256([]byte("posse-push:" + b64(s.pair.pub) + ":" + term))
	sent := false
	for _, t := range targets {
		selected := kind == PushWaiting && t.Waiting || kind == PushDone && t.Done
		alerts := selected && (t.Alerts == nil || *t.Alerts)
		showBadge := t.Badge != nil && *t.Badge || t.Badge == nil && selected
		if !alerts && !showBadge || s.phoneInFront(t.Phone) {
			continue
		}
		p := deskrelay.Push{Sandbox: t.Sandbox, NoAlert: !alerts}
		if showBadge {
			p.Badge = uint16(min(badge, 0xFFFF))
		}
		copy(p.Collapse[:], sum[:])
		p.Token, _ = hex.DecodeString(t.Token)
		key, _ := base64.StdEncoding.DecodeString(t.Key)
		var err error
		if p.Cipher, err = sealPush(key, plain); err != nil {
			log.Printf("mobile: push: %v", err)
			continue
		}
		go s.sendPush(cfg, t, p)
		sent = true
	}
	return sent
}

// pushSealOverhead 是 sealPush 在明文外加的 nonce 和标签
const pushSealOverhead = 12 + 16

// pushPlain 是通知内容的 JSON，加密后超过中转的上限时依次缩短消息、标签名、工作区名和项目名：
// 各段按字数截过，但中文一个字三个字节，全是长中文时仍会超
func pushPlain(b pushBody) []byte {
	for {
		plain, _ := json.Marshal(b)
		over := len(plain) + pushSealOverhead - deskrelay.MaxPushCipher
		if over <= 0 {
			return plain
		}
		switch {
		case b.Message != "":
			b.Message = shorten(b.Message, over)
		case b.Tab != "":
			b.Tab = shorten(b.Tab, over)
		case b.Space != "":
			b.Space = shorten(b.Space, over)
		case b.Project != "":
			b.Project = shorten(b.Project, over)
		default:
			return plain
		}
	}
}

// shorten 去掉末尾至少 over 字节对应的字（按一字三字节估，不够时下一轮再去），留下的加省略号。
func shorten(s string, over int) string {
	r := []rune(strings.TrimSuffix(s, "…"))
	n := len(r) - max(1, (over+2)/3) - 1
	if n <= 0 {
		return ""
	}
	return string(r[:n]) + "…"
}

// pushContent 找到终端所在的工作区，拼出通知内容；角标是待处理和完成没看的会话数，口径同桌面的 Dock 角标。
func (s *Server) pushContent(st State, term, kind string) (pushBody, int, bool) {
	name, _ := os.Hostname()
	body := pushBody{V: 1, Kind: kind, Computer: b64(s.pair.pub), Name: strings.TrimSuffix(name, ".local")}
	found, badge := false, 0
	for _, w := range st.Workspaces {
		for _, t := range w.Terminals {
			if t.Agent != "" && !t.Lost && !t.Tasks && (t.Status == "waiting" || t.Unread) {
				badge++
			}
			if t.ID != term {
				continue
			}
			// 派了任务、停下来等结果的不算等你处理，桌面侧栏也不写待处理
			found = !t.Tasks
			body.Agent = t.Agent
			body.Project = clip(projectName(st, w.ProjectID), 60)
			if d := displayName(st, w); d != projectName(st, w.ProjectID) {
				body.Space = clip(d, 60)
			}
			body.Tab = clip(tabTitle(t), 80)
			body.Message = clip(t.Message, 240)
			body.Worktree = worktreeID(w)
			body.Terminal = t.ID
		}
	}
	return body, badge, found
}

// sealPush 用 AES-256-GCM 加密，输出 12 字节 nonce、密文和 16 字节标签（CryptoKit 的 SealedBox.combined）。
func sealPush(key, plain []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("push key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	out := gcm.Seal(nonce, nonce, plain, nil)
	if len(out) > deskrelay.MaxPushCipher {
		return nil, errors.New("push body too large")
	}
	return out, nil
}

// pushRetry 是等中转回执多久就重发，最多 pushTries 次。中转按 nonce 去重，重发不会推两次。
// 发完最后一次接着等到 pushWait：中转等 APNs 最多 10 秒，回执晚到也要收，BadToken 才删得掉。
const (
	pushRetry = 1500 * time.Millisecond
	pushTries = 3
	pushWait  = 12 * time.Second
)

// probeBadgeOnly 用不投递的探测确认中转认得新格式，旧中转不会把它误当通知。
func (s *Server) probeBadgeOnly(t PushTarget) bool {
	p := deskrelay.Push{Sandbox: t.Sandbox, NoAlert: true, Probe: true}
	p.Token, _ = hex.DecodeString(t.Token)
	return s.sendPush(s.q.opts.Relay(), t, p) == deskrelay.PushOK
}

func (s *Server) sendPush(cfg deskrelay.Config, t PushTarget, p deskrelay.Push) byte {
	rand.Read(p.Nonce[:])
	addr, err := net.ResolveUDPAddr("udp4", cfg.Relay)
	if err != nil {
		log.Printf("mobile: push: relay %s: %v", cfg.Relay, err)
		return deskrelay.PushFailed
	}
	var uc *net.UDPConn
	if s.q.opts.ListenOut != nil {
		uc, _ = s.q.opts.ListenOut()
	}
	if uc == nil {
		if uc, err = net.ListenUDP("udp4", nil); err != nil {
			log.Printf("mobile: push: %v", err)
			return deskrelay.PushFailed
		}
	}
	defer uc.Close()
	cred := cfg.Credential()
	buf := make([]byte, 64)
	tries, retry, wait := pushTries, pushRetry, pushWait
	if p.Probe {
		tries, retry, wait = 2, 700*time.Millisecond, 1400*time.Millisecond
	}
	end := time.Now().Add(wait)
	for i := range tries {
		pkt, err := deskrelay.EncodePush(p, cred, time.Now())
		if err != nil {
			log.Printf("mobile: push: %v", err)
			return deskrelay.PushFailed
		}
		if _, err := uc.WriteToUDP(pkt, addr); err != nil {
			log.Printf("mobile: push: %v", err)
			return deskrelay.PushFailed
		}
		deadline := time.Now().Add(retry)
		if i == tries-1 {
			deadline = end
		}
		for {
			uc.SetReadDeadline(deadline)
			n, err := uc.Read(buf)
			if err != nil {
				break
			}
			nonce, r, ok := deskrelay.ParsePushReply(buf[:n])
			if !ok || nonce != p.Nonce {
				continue
			}
			switch r {
			case deskrelay.PushOK:
			case deskrelay.PushBadToken:
				log.Printf("mobile: push: token of phone %s no longer valid, dropped", t.Phone)
				if !p.Probe {
					s.pair.dropPush(t.Token)
				}
			default:
				log.Printf("mobile: push: relay refused (%d)", r)
			}
			return r
		}
	}
	log.Printf("mobile: push: relay %s did not answer", cfg.Relay)
	return deskrelay.PushFailed
}
