package collaboration

// 邀请、成员凭据和本机的局域网地址。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InvitePrefix 是邀请文本的开头：用户经聊天软件转发时前后可能夹着别的字，解析时在文本里找它。
const InvitePrefix = "posse-invite:v1:"

// InviteTTL 是邀请的有效期（按房主时钟）。
const InviteTTL = 10 * time.Minute

// Invite 是房主生成、经用户自己的通信方式交给对方的一次性邀请。
type Invite struct {
	V        int      `json:"v"`
	Host     string   `json:"host"` // 房主安装身份的指纹：TLS 按它固定
	HostName string   `json:"hostName,omitempty"`
	Room     string   `json:"room"`
	Title    string   `json:"title,omitempty"`
	Addrs    []string `json:"addrs"` // 房主当前的局域网候选地址 host:port
	Secret   string   `json:"secret"`
	Expires  int64    `json:"exp"` // 房主时钟的毫秒时间戳，只作提示；有效期由房主判断
}

// Encode 写成一行可复制的文本。
func (i Invite) Encode() string {
	b, _ := json.Marshal(i)
	return InvitePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ErrBadInvite 是认不出的邀请文本。
var ErrBadInvite = errors.New("not a Posse invitation")

// ErrInviteVersion 是更新版本的 Posse 生成的邀请。
var ErrInviteVersion = errors.New("this invitation needs a newer Posse; update Posse and try again")

// ParseInvite 从粘贴的文本里找出邀请并校验格式（房主指纹和房间键一致、地址非空）。
func ParseInvite(text string) (Invite, error) {
	var inv Invite
	i := strings.Index(text, "posse-invite:")
	if i < 0 {
		return inv, ErrBadInvite
	}
	rest := text[i:]
	if !strings.HasPrefix(rest, InvitePrefix) {
		return inv, ErrInviteVersion
	}
	rest = rest[len(InvitePrefix):]
	end := strings.IndexFunc(rest, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	})
	if end >= 0 {
		rest = rest[:end]
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || json.Unmarshal(b, &inv) != nil {
		return inv, ErrBadInvite
	}
	if inv.V != ProtocolVersion {
		return inv, ErrInviteVersion
	}
	host, _, ok := SplitRoomKey(inv.Room)
	if !ok || host != inv.Host || len(inv.Host) != 64 || inv.Secret == "" || len(inv.Addrs) == 0 {
		return inv, ErrBadInvite
	}
	if _, err := hex.DecodeString(inv.Host); err != nil {
		return inv, ErrBadInvite
	}
	return inv, nil
}

// NewSecret 是 256 位的随机串（邀请凭据、申请 ID）。
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashSecret 是邀请凭据在房主库里存的样子：只存哈希。
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte("posse-invite-v1:" + secret))
	return hex.EncodeToString(sum[:])
}

// Credential 是房主给一位成员的房间凭据，绑定房间、成员指纹和成员代际。由房主的身份私钥派生，
// 不另存一份：同一申请重取凭据时重新算出同一个，代际变了旧凭据自然失效。凭据不写进日志。
func Credential(id Identity, room, fp string, gen int64) string {
	key := sha256.Sum256(append([]byte("posse-collab-credential-v1:"), id.Private.Seed()...))
	m := hmac.New(sha256.New, key[:])
	fmt.Fprintf(m, "%s\n%s\n%d", room, fp, gen)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckCredential 用常量时间比较。
func CheckCredential(id Identity, room, fp string, gen int64, cred string) bool {
	return hmac.Equal([]byte(Credential(id, room, fp, gen)), []byte(cred))
}

// LocalAddrs 是本机的局域网候选地址（IPv4，非回环，网卡开着），有线、Wi‑Fi 在前。
// POSSE_COLLAB_ADDRS（逗号分隔的 host 或 host:port）可以覆盖：单机多进程测试用回环地址。
func LocalAddrs(port int) []string {
	if v := os.Getenv("POSSE_COLLAB_ADDRS"); v != "" {
		var out []string
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a == "" {
				continue
			}
			if _, _, err := net.SplitHostPort(a); err != nil {
				a = net.JoinHostPort(a, strconv.Itoa(port))
			}
			out = append(out, a)
		}
		return out
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	type cand struct {
		addr string
		rank int
	}
	var cs []cand
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			rank := 2
			switch {
			case strings.HasPrefix(ifc.Name, "en"):
				rank = 0
			case strings.HasPrefix(ifc.Name, "bridge"):
				rank = 1
			}
			cs = append(cs, cand{net.JoinHostPort(ipn.IP.String(), strconv.Itoa(port)), rank})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].rank < cs[j].rank })
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.addr
	}
	return out
}
