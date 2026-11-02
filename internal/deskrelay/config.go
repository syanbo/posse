package deskrelay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config 是 Mac 上 $POSSE_HOME/desk.json 里的中转设置。Key 和 Master 至多有一个：
// 都没有时登记不带签名（没开鉴权的服务器）。
type Config struct {
	Relay  string `json:"relay"`            // UDP host:port
	Name   string `json:"name,omitempty"`   // 被邀请时的名字
	Key    string `json:"key,omitempty"`    // 这个名字的密钥，base64
	Master string `json:"master,omitempty"` // 管理员的主密钥，base64
}

// ConfigFile 是中转设置所在的文件。
func ConfigFile(home string) string { return filepath.Join(home, "desk.json") }

// LoadConfig 读中转设置，没有文件时返回空的。
func LoadConfig(home string) (Config, error) {
	var c Config
	data, err := os.ReadFile(ConfigFile(home))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ConfigFile(home), err)
	}
	c.Relay = strings.TrimSpace(c.Relay)
	return c, nil
}

// SaveConfig 写中转设置（只给自己读，里面有密钥）；c.Relay 为空时删掉文件。
func SaveConfig(home string, c Config) error {
	path := ConfigFile(home)
	if c.Relay == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 先删掉可能留下的临时文件：WriteFile 写已有的文件时沿用它原来的权限
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Admin 是这台 Mac 存着主密钥，可以邀请别人。
func (c Config) Admin() bool { return c.Master != "" }

// Credential 是登记用的名字和密钥，没有密钥时为 nil。
func (c Config) Credential() *Credential {
	if master, err := base64.StdEncoding.DecodeString(c.Master); err == nil && len(master) == KeyLen {
		return &Credential{Name: AdminName, Key: UserKey(master, AdminName)}
	}
	if key, err := base64.StdEncoding.DecodeString(c.Key); err == nil && len(key) == KeyLen && ValidName(c.Name) {
		return &Credential{Name: c.Name, Key: key}
	}
	return nil
}

// NewMaster 生成一把主密钥。
func NewMaster() []byte {
	b := make([]byte, KeyLen)
	rand.Read(b)
	return b
}

// 邀请码和管理员码：codePrefix 加 base64url 的
// u8 版本、u8 种类、u8 地址长度、地址、u8 名字长度、名字、32 字节密钥。
// 只是把地址和密钥打成一串好复制，不加密：拿到码的人本来就要往这个地址发包。
const (
	codePrefix  = "posse-relay:"
	codeVersion = 1
	codeUser    = 1
	codeAdmin   = 2
)

func encodeCode(kind byte, addr, name string, key []byte) string {
	b := []byte{codeVersion, kind, byte(len(addr))}
	b = append(b, addr...)
	b = append(b, byte(len(name)))
	b = append(b, name...)
	b = append(b, key...)
	return codePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// AdminCode 是管理员码：中转地址加主密钥。贴进管理员自己的 Mac。
func AdminCode(addr string, master []byte) string {
	return encodeCode(codeAdmin, addr, "", master)
}

// Invite 给 name 生成邀请码，只有管理员能生成。
func (c Config) Invite(name string) (string, error) {
	master, err := base64.StdEncoding.DecodeString(c.Master)
	if err != nil || len(master) != KeyLen || c.Relay == "" {
		return "", errors.New("this computer is not the relay’s admin")
	}
	if !ValidName(name) {
		return "", errors.New("the name may use only lowercase letters, digits, dots, hyphens and underscores, up to 32 characters")
	}
	if name == AdminName {
		return "", errors.New("admin is the name the admin uses")
	}
	return encodeCode(codeUser, c.Relay, name, UserKey(master, name)), nil
}

var errBadCode = errors.New("not a valid relay invite code")

// ParseCode 解开邀请码或管理员码，得到要写进 desk.json 的设置。
func ParseCode(s string) (Config, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, codePrefix) {
		return Config{}, errBadCode
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, codePrefix))
	if err != nil || len(b) < 4 || b[0] != codeVersion {
		return Config{}, errBadCode
	}
	kind, rest := b[1], b[2:]
	field := func() (string, bool) {
		if len(rest) < 1 || len(rest) < 1+int(rest[0]) {
			return "", false
		}
		v := string(rest[1 : 1+rest[0]])
		rest = rest[1+rest[0]:]
		return v, true
	}
	addr, ok1 := field()
	name, ok2 := field()
	if !ok1 || !ok2 || addr == "" || len(rest) != KeyLen {
		return Config{}, errBadCode
	}
	key := base64.StdEncoding.EncodeToString(rest)
	switch {
	case kind == codeAdmin && name == "":
		return Config{Relay: addr, Master: key}, nil
	case kind == codeUser && ValidName(name):
		return Config{Relay: addr, Name: name, Key: key}, nil
	}
	return Config{}, errBadCode
}
