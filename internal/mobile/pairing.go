package mobile

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
)

// Device 是一台配对过的手机。LastSeen 为零表示二维码已生成、还没有手机用它连上来。
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Token    string    `json:"token"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
}

// state 是 $HIVE_HOME/mobile.json：主机的长期密钥对和配对过的手机。手机把公钥记为这台主机的身份，
// 换密钥等于所有手机都要重新配对。
type state struct {
	PublicKey string   `json:"publicKey"`
	SecretKey string   `json:"secretKey"`
	Devices   []Device `json:"devices"`
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
			d.Name = "手机 " + now.Format("01-02 15:04")
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

func (p *pairing) devices() []Device {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Device
	for _, d := range p.st.Devices {
		if !d.LastSeen.IsZero() {
			d.Token = ""
			out = append(out, d)
		}
	}
	return out
}

func (p *pairing) paired() bool { return len(p.devices()) > 0 }

func (p *pairing) remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.st.Devices)
	p.st.Devices = slices.DeleteFunc(p.st.Devices, func(d Device) bool { return d.ID == id })
	if len(p.st.Devices) == n {
		return false
	}
	p.save()
	return true
}

// offerCode 是二维码里 orca://pair?code= 后面的部分：Orca 手机 App 的配对信息，base64url 不补位。
func offerCode(endpoint, token string, pub [32]byte, deviceID string) string {
	b, _ := json.Marshal(map[string]any{
		"v": 2, "endpoint": endpoint, "deviceToken": token, "publicKeyB64": b64(pub),
		"pairedDeviceId": deviceID, "scope": "mobile",
	})
	return base64.RawURLEncoding.EncodeToString(b)
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

// LocalAddress 选手机能连到的本机地址：优先 Tailscale（100.64.0.0/10），其次局域网的私有地址。
func LocalAddress() string {
	ifaces, _ := net.Interfaces()
	var lan string
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
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
				return ip.String()
			}
			if lan == "" && ip.IsPrivate() {
				lan = ip.String()
			}
		}
	}
	return lan
}
