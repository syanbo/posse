// Package collaboration 是讨论的安装身份：一份 Ed25519 密钥，指纹给讨论命名（roomKey）。
// 第一阶段只用它稳定命名、不开网络服务；第二阶段的局域网协作沿用同一份身份。
// 身份文件与手机配对的 mobile.json 完全独立。
package collaboration

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileName 是身份文件在 POSSE_HOME 下的名字。
const FileName = "collab.json"

// Identity 是这个 POSSE_HOME 的安装身份。ID 是公钥 SPKI 的 SHA-256 指纹（小写十六进制），
// 更新、重装应用不变，只有身份文件丢失才变。
type Identity struct {
	ID      string
	Name    string
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

type identityFile struct {
	Version    int    `json:"version"`
	PrivateKey string `json:"privateKey"` // Ed25519 种子，十六进制
	Name       string `json:"name,omitempty"`
}

var mu sync.Mutex

// Load 读已有的身份；文件不存在时返回 fs.ErrNotExist，不创建。
func Load(home string) (Identity, error) {
	mu.Lock()
	defer mu.Unlock()
	return load(home)
}

// LoadOrCreate 读身份，没有就生成一份并原子写入（0600）。首次建讨论、首次加入讨论时调用。
func LoadOrCreate(home string) (Identity, error) {
	mu.Lock()
	defer mu.Unlock()
	id, err := load(home)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	name, _ := os.Hostname()
	name = strings.TrimSuffix(name, ".local")
	f := identityFile{Version: 1, PrivateKey: hex.EncodeToString(priv.Seed()), Name: name}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return Identity{}, err
	}
	if err := writeAtomic(filepath.Join(home, FileName), b); err != nil {
		return Identity{}, err
	}
	// 另一个进程抢先写下的以它为准
	return load(home)
}

func load(home string) (Identity, error) {
	b, err := os.ReadFile(filepath.Join(home, FileName))
	if err != nil {
		return Identity{}, err
	}
	var f identityFile
	if err := json.Unmarshal(b, &f); err != nil {
		return Identity{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	return fromFile(f)
}

func fromFile(f identityFile) (Identity, error) {
	seed, err := hex.DecodeString(f.PrivateKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return Identity{}, fmt.Errorf("read %s: bad private key", FileName)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	fp, err := Fingerprint(pub)
	if err != nil {
		return Identity{}, err
	}
	return Identity{ID: fp, Name: f.Name, Public: pub, Private: priv}, nil
}

// Fingerprint 是公钥 SPKI（DER）的 SHA-256，小写十六进制。第二阶段 TLS 按它固定对端身份。
func Fingerprint(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// RoomKey 是讨论在各处（SQLite、tasks.discussion_id）的主键：房主指纹/讨论 ID。
// 共享后也不换，免得跨表迁移。
func RoomKey(hostID, discussionID string) string { return hostID + "/" + discussionID }

// SplitRoomKey 拆出房主指纹和讨论 ID。
func SplitRoomKey(key string) (host, id string, ok bool) {
	host, id, ok = strings.Cut(key, "/")
	return host, id, ok && host != "" && id != ""
}

// NewDiscussionID 是 128 位安全随机数，跨机器也不会撞；调用方仍用唯一约束检测碰撞。
func NewDiscussionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "d_" + hex.EncodeToString(b)
}

// writeAtomic 先写同目录的临时文件、fsync，再改名，读的一方不会读到写了一半的文件。
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// 已有文件（另一个进程刚建好）不覆盖：身份一旦写下就不能换
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	return nil
}

// SetName 改显示名（其他人看到的名字）；私钥和指纹不变。改名是覆盖写：先写临时文件再改名。
func SetName(home, name string) (Identity, error) {
	mu.Lock()
	defer mu.Unlock()
	path := filepath.Join(home, FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, err
	}
	var f identityFile
	if err := json.Unmarshal(b, &f); err != nil {
		return Identity{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	f.Name = name
	if b, err = json.MarshalIndent(f, "", "  "); err != nil {
		return Identity{}, err
	}
	if err := WriteFile(path, b); err != nil {
		return Identity{}, err
	}
	return fromFile(f)
}

// WriteFile 原子地覆盖写一个私有文件（0600）：临时文件、fsync、改名。房间凭据也用它。
func WriteFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
