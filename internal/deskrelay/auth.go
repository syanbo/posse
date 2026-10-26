package deskrelay

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"time"
)

// 鉴权：服务器配了主密钥时，只有带签名的 Mac 登记能开会话，手机的登记只能加入已有的会话。
// 令牌是 Mac 随机生成、只交给配对过的手机的，手机不用知道密钥。
//
//	签名登记：'H' 'R' 'Y' '1'、u8 身份（1 Mac）、16 字节令牌、u64 时间（Unix 秒）、u8 名字长度、名字、16 字节签名。
//	检查：    'H' 'R' 'Y' '1'、0x06、u64 时间、u8 名字长度、名字、16 字节签名。不开会话，只问密钥能不能用。
//	          服务器回 'H' 'R' 'Y' '1'、0x86、u8 结果（Check*）。
//	拒绝：    服务器对通不过的 Mac 登记回 'H' 'R' 'Y' '1'、0xC0|身份。
//
// 签名是用户密钥对签名前所有字节的 HMAC-SHA256 取前 16 字节；用户密钥由主密钥和名字算出（UserKey），
// 管理员的 Mac 存主密钥，可以自己给别人算密钥。时间和服务器差 5 分钟以上的不认，抓到的包只能重放这么久。

const (
	KeyLen  = 32
	sigLen  = 16
	maxName = 32

	// AdminName 是存主密钥的那台 Mac 登记用的名字，不能拿来邀请别人
	AdminName = "admin"

	kindCheck      byte = 0x06
	kindCheckReply byte = 0x86
	deniedBit      byte = 0xC0

	clockSkew = 5 * time.Minute
)

// 检查的结果。
const (
	CheckOK      byte = 0
	CheckBadKey  byte = 1 // 名字不认识或签名不对
	CheckRevoked byte = 2
	CheckClock   byte = 3 // 这台电脑的时间和服务器差太多
)

// Credential 是一台 Mac 登记用的名字和密钥。
type Credential struct {
	Name string
	Key  []byte
}

// UserKey 由主密钥和名字算出这个人的密钥。
func UserKey(master []byte, name string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("posse-relay/v1/user/" + name))
	return m.Sum(nil)
}

// ValidName 是名字的规则：1 到 32 个小写字母、数字、点、横线、下划线。
func ValidName(name string) bool {
	if name == "" || len(name) > maxName {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func sign(key, b []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return m.Sum(nil)[:sigLen]
}

func appendSigned(out []byte, c Credential, now time.Time) []byte {
	out = binary.BigEndian.AppendUint64(out, uint64(now.Unix()))
	out = append(out, byte(len(c.Name)))
	out = append(out, c.Name...)
	return append(out, sign(c.Key, out)...)
}

// SignedRegister 是 Mac 带签名的登记包。每次发都要重新生成：里面有时间。
func SignedRegister(token []byte, c Credential, now time.Time) []byte {
	return appendSigned(Register(RoleMac, token), c, now)
}

// MacRegister 是 Mac 的登记包：有密钥时带签名，没有就是旧的不带签名的登记（没开鉴权的服务器）。
func MacRegister(token []byte, c *Credential, now time.Time) []byte {
	if c == nil {
		return Register(RoleMac, token)
	}
	return SignedRegister(token, *c, now)
}

// Check 是检查包：问服务器这份密钥能不能用，不开会话。
func Check(c Credential, now time.Time) []byte {
	return appendSigned(append(append([]byte{}, magic...), kindCheck), c, now)
}

// ParseCheckReply 解开检查的回包。
func ParseCheckReply(b []byte) (result byte, ok bool) {
	if len(b) != 6 || !bytes.Equal(b[:4], magic) || b[4] != kindCheckReply {
		return 0, false
	}
	return b[5], true
}

// IsDenied 判断是不是服务器拒绝了这个身份的登记。
func IsDenied(b []byte, role byte) bool {
	return len(b) == 5 && bytes.Equal(b[:4], magic) && b[4] == deniedBit|role
}

// signed 是解开的签名部分。
type signed struct {
	name string
	at   time.Time
	body []byte // 签名覆盖的字节
	sig  []byte
}

// parseSigned 从 off 开始解时间、名字和签名，要正好用完 b。
func parseSigned(b []byte, off int) (signed, bool) {
	if len(b) < off+8+1+1+sigLen {
		return signed{}, false
	}
	at := int64(binary.BigEndian.Uint64(b[off:]))
	n := int(b[off+8])
	end := off + 9 + n
	if n == 0 || n > maxName || len(b) != end+sigLen {
		return signed{}, false
	}
	return signed{name: string(b[off+9 : end]), at: time.Unix(at, 0), body: b[:end], sig: b[end:]}, true
}

// verify 用主密钥核对签名，返回检查结果。
func verify(master []byte, s signed, revoked func(string) bool, now time.Time) byte {
	if !ValidName(s.name) || !hmac.Equal(sign(UserKey(master, s.name), s.body), s.sig) {
		return CheckBadKey
	}
	if revoked != nil && revoked(s.name) {
		return CheckRevoked
	}
	if d := now.Sub(s.at); d > clockSkew || d < -clockSkew {
		return CheckClock
	}
	return CheckOK
}
