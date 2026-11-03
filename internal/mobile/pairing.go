package mobile

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/box"

	"posse/internal/deskrelay"
	"posse/internal/errtext"
)

// Device 是一个配对码换来的设备 token。LastSeen 为零表示二维码已生成、还没有手机用它连上来。
// 同一台手机每扫一次码就多一个 token（重新扫码、远程和局域网各存一份），Posse 客户端连上后报上
// 自己的安装 ID（Phone），列表按它合成一台手机；Orca App 不报，一个 token 算一台。
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Token    string    `json:"token,omitempty"`
	Phone    string    `json:"phone,omitempty"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
	// RelayToken 是这台手机经 UDP 中转连 QUIC 用的令牌（hex），第一次用到时生成。存盘是为了常驻进程
	// 重启、交接后不变：手机记着它，冷启动直接经中转拨号，不用先连 WebSocket
	RelayToken string `json:"relayToken,omitempty"`
	// 以下只在列表里给出，不存盘：合进来的 token 个数，和是否有连接开着
	Links  int  `json:"links,omitempty"`
	Online bool `json:"online,omitempty"`
}

// state 是 $POSSE_HOME/mobile.json：主机的长期密钥对和配对过的手机。手机把公钥记为这台主机的身份，
// 换密钥等于所有手机都要重新配对。
type state struct {
	PublicKey string   `json:"publicKey"`
	SecretKey string   `json:"secretKey"`
	Devices   []Device `json:"devices"`
	// Push 是手机的推送注册（push.go），一台手机一条
	Push []PushTarget `json:"push,omitempty"`
}

type pairing struct {
	path string
	mu   sync.Mutex
	st   state
	pub  [32]byte
	priv [32]byte
}

func openPairing(home string) (*pairing, error) {
	p := &pairing{path: filepath.Join(home, "mobile.json")}
	b, err := os.ReadFile(p.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &p.st); err != nil {
			return nil, fmt.Errorf("%s: %w", p.path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	pub, ok1 := decode32(p.st.PublicKey)
	priv, ok2 := decode32(p.st.SecretKey)
	if !ok1 || !ok2 {
		pk, sk, err := box.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		pub, priv = *pk, *sk
		p.st = state{PublicKey: b64(pub), SecretKey: b64(priv)}
		if err := p.save(); err != nil {
			return nil, err
		}
	}
	p.pub, p.priv = pub, priv
	return p, nil
}

func b64(b [32]byte) string { return base64.StdEncoding.EncodeToString(b[:]) }

// save 在持有 mu 时调用（或在创建时）。
func (p *pairing) save() error {
	b, _ := json.MarshalIndent(p.st, "", "  ")
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// pending 返回等待手机扫码的设备，没有就新建一条；rotate 时作废旧的待配对设备。
func (p *pairing) pending(rotate bool) (Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.IndexFunc(p.st.Devices, func(d Device) bool { return d.LastSeen.IsZero() })
	if i >= 0 && !rotate {
		return p.st.Devices[i], nil
	}
	if i >= 0 {
		p.st.Devices = slices.Delete(p.st.Devices, i, i+1)
	}
	d := Device{ID: newUUID(), Token: randHex(24), PairedAt: time.Now()}
	p.st.Devices = append(p.st.Devices, d)
	return d, p.save()
}

// auth 按 token 找设备并记下连接时间；第一次连上的设备从这时起算配对完成。
func (p *pairing) auth(token string) (Device, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.st.Devices {
		d := &p.st.Devices[i]
		if subtle.ConstantTimeCompare([]byte(d.Token), []byte(token)) != 1 {
			continue
		}
		now := time.Now()
		if d.LastSeen.IsZero() {
			d.PairedAt = now
			// 默认名字存下来给窗口和手机看，这时还不知道手机的语言，按用户在 Posse 里选的语言写
			d.Name = errtext.MessageIn(errtext.SystemLang(), "Phone") + " " + now.Format("01-02 15:04")
		}
		// 只在第一次和每隔一阵写盘，免得每次重连都写
		if d.LastSeen.IsZero() || now.Sub(d.LastSeen) > time.Hour {
			d.LastSeen = now
			p.save()
		} else {
			d.LastSeen = now
		}
		return *d, true
	}
	return Device{}, false
}

// valid 检查 token 是否仍然有效：设备被移除后，已经连着的手机下一个请求就会被拒。
func (p *pairing) valid(token string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.ContainsFunc(p.st.Devices, func(d Device) bool {
		return subtle.ConstantTimeCompare([]byte(d.Token), []byte(token)) == 1
	})
}

// identify 记下 token 属于哪台手机（Posse 客户端的安装 ID）。held 是这台手机还存着的、
// 这台电脑发的全部 token：同一台手机不再存着的 token 是重新扫码换掉或在手机上删掉的，一并作废；
// 还没认领的旧 token 在 held 里就归到这台手机。held 为空时不清理。
func (p *pairing) identify(id, phone, name string, held []string) (removed []string) {
	if phone == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	holds := func(token string) bool {
		return slices.ContainsFunc(held, func(h string) bool {
			return subtle.ConstantTimeCompare([]byte(h), []byte(token)) == 1
		})
	}
	changed := false
	claim := func(d *Device) {
		if d.Phone != phone || name != "" && d.Name != name {
			d.Phone = phone
			if name != "" {
				d.Name = name
			}
			changed = true
		}
	}
	keep := p.st.Devices[:0]
	for i := range p.st.Devices {
		d := p.st.Devices[i]
		switch {
		case d.ID == id:
			claim(&d)
		case d.LastSeen.IsZero():
		case d.Phone == phone && len(held) > 0 && !holds(d.Token):
			removed = append(removed, d.ID)
			changed = true
			continue
		case d.Phone == "" && holds(d.Token):
			claim(&d)
		}
		keep = append(keep, d)
	}
	p.st.Devices = keep
	if changed {
		p.save()
	}
	return removed
}

// devices 列出配对过的手机：同一台手机的 token 合成一项，ID 取最近连过的那个，配对时间取最早的。
func (p *pairing) devices() []Device {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Device
	at := map[string]int{}
	for _, d := range p.st.Devices {
		if d.LastSeen.IsZero() {
			continue
		}
		d.Token, d.RelayToken = "", ""
		d.Links = 1
		i, ok := at[d.Phone]
		if d.Phone == "" || !ok {
			if d.Phone != "" {
				at[d.Phone] = len(out)
			}
			out = append(out, d)
			continue
		}
		g := &out[i]
		g.Links++
		if d.PairedAt.Before(g.PairedAt) {
			g.PairedAt = d.PairedAt
		}
		if d.LastSeen.After(g.LastSeen) {
			g.ID, g.Name, g.LastSeen = d.ID, d.Name, d.LastSeen
		}
	}
	return out
}

func (p *pairing) paired() bool { return len(p.devices()) > 0 }

// relayDevices 是已经拿过中转令牌的设备。
func (p *pairing) relayDevices() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, d := range p.st.Devices {
		if d.RelayToken != "" {
			out = append(out, d.ID)
		}
	}
	return out
}

// relayToken 返回这个设备的中转令牌，还没有就生成并存盘。设备不在了返回 nil。
func (p *pairing) relayToken(id string) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.st.Devices {
		d := &p.st.Devices[i]
		if d.ID != id {
			continue
		}
		if b, err := hex.DecodeString(d.RelayToken); err == nil && len(b) == deskrelay.TokenLen {
			return b
		}
		d.RelayToken = randHex(deskrelay.TokenLen)
		if err := p.save(); err != nil {
			log.Printf("mobile: save relay token: %v", err)
		}
		b, _ := hex.DecodeString(d.RelayToken)
		return b
	}
	return nil
}

// remove 移除一台手机：报过安装 ID 的连同它的其他 token 一起移除。返回移除的设备 ID。
func (p *pairing) remove(id string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.IndexFunc(p.st.Devices, func(d Device) bool { return d.ID == id })
	if i < 0 {
		return nil
	}
	phone := p.st.Devices[i].Phone
	var removed []string
	p.st.Devices = slices.DeleteFunc(p.st.Devices, func(d Device) bool {
		if d.ID == id || phone != "" && d.Phone == phone {
			removed = append(removed, d.ID)
			return true
		}
		return false
	})
	p.prunePushLocked()
	p.save()
	return removed
}

// offerCode 是二维码里 orca://pair?code= 后面的部分：Orca 手机 App 的配对信息，base64url 不补位。
// endpoints 是给 Posse 自己的客户端并发握手用的地址列表，主 endpoint 放在第一位。
// Orca 的 PairingOfferSchema 不是 strict，多出来的字段会被忽略。
func offerCode(endpoint string, endpoints []string, token string, pub [32]byte, deviceID string) string {
	b, _ := json.Marshal(map[string]any{
		"v": 2, "endpoint": endpoint, "endpoints": uniqueEndpoints(endpoint, endpoints),
		"deviceToken": token, "publicKeyB64": b64(pub),
		"pairedDeviceId": deviceID, "scope": "mobile",
	})
	return base64.RawURLEncoding.EncodeToString(b)
}

// uniqueEndpoints 把主地址放在最前，去掉空串和重复。
func uniqueEndpoints(primary string, extra []string) []string {
	out := make([]string, 0, 1+len(extra))
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(primary)
	for _, s := range extra {
		add(s)
	}
	return out
}

var errAddress = errors.New("address must be a host, host:port, IP, or http(s)/ws(s) URL")

// ResolveEndpoint 照 Orca 桌面端（src/main/runtime/pairing-endpoint.ts）把地址换成配对码里的 endpoint：
// 带 :// 的是整条 URL，http、https 换成 ws、wss，路径保留，给隧道、反向代理用；
// 否则是主机或 主机:端口，端口缺省用本机监听的 port。
func ResolveEndpoint(addr string, port int) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" || strings.ContainsFunc(addr, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", errAddress
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", errAddress
		}
		switch u.Scheme = strings.ToLower(u.Scheme); u.Scheme {
		case "http":
			u.Scheme = "ws"
		case "https":
			u.Scheme = "wss"
		}
		if u.Scheme != "ws" && u.Scheme != "wss" || u.Hostname() == "" || u.User != nil ||
			u.Fragment != "" || u.Port() == "0" || wildcardHost(u.Hostname()) {
			return "", errAddress
		}
		if u.Path == "/" && u.RawQuery == "" {
			u.Path = ""
		}
		return u.String(), nil
	}
	host, p := addr, strconv.Itoa(port)
	if ip := net.ParseIP(addr); ip == nil && strings.Contains(addr, ":") || strings.HasPrefix(addr, "[") {
		h, pp, err := net.SplitHostPort(addr)
		if err != nil {
			return "", errAddress
		}
		host, p = h, pp
	}
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 || strings.HasPrefix(p, "0") {
		return "", errAddress
	}
	if host == "" || strings.ContainsAny(host, "/?#@[]") || wildcardHost(host) {
		return "", errAddress
	}
	return "ws://" + net.JoinHostPort(host, p), nil
}

func wildcardHost(h string) bool {
	h = strings.ToLower(strings.Trim(h, "[]"))
	return h == "*" || h == "0.0.0.0" || h == "::" || h == "::ffff:0:0"
}

// IsTailscale 报告 ip 是不是 Tailscale 的地址（100.64.0.0/10）。
func IsTailscale(ip string) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	p := net.ParseIP(ip)
	return p != nil && cgnat.Contains(p)
}

// LocalEndpoints 列出手机可能连得上的本机 IPv4：Tailscale（100.64.0.0/10）在前，其余私有地址在后。
func LocalEndpoints() []string {
	ifaces, _ := net.Interfaces()
	var ts, lan []string
	seen := map[string]bool{}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	add := func(dst *[]string, ip string) {
		if seen[ip] {
			return
		}
		seen[ip] = true
		*dst = append(*dst, ip)
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		// 虚拟网卡（docker、vmnet、utun 之外的桥接等）上的地址手机连不到
		if strings.HasPrefix(ifc.Name, "bridge") || strings.HasPrefix(ifc.Name, "vmnet") || strings.HasPrefix(ifc.Name, "docker") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip := ipn.IP.To4()
			if cgnat.Contains(ip) {
				add(&ts, ip.String())
			} else if ip.IsPrivate() {
				add(&lan, ip.String())
			}
		}
	}
	return append(ts, lan...)
}

// LocalAddress 选手机能连到的本机地址：优先 Tailscale，其次局域网的私有地址。
func LocalAddress() string {
	eps := LocalEndpoints()
	if len(eps) == 0 {
		return ""
	}
	return eps[0]
}
