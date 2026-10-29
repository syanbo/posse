// Package projicon 是项目自定义的图标：窗口里设，存在 $POSSE_HOME/project-icons，
// 常驻进程给手机列工作区时读它，手机显示和桌面侧栏一样的图标。
package projicon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Icon 是一个项目的图标。Text 和 Image 只有一个不空；都空就是默认的文件夹。
type Icon struct {
	Text  string `json:"text,omitempty"`  // 文字或 emoji
	Image string `json:"image,omitempty"` // Dir 下的 PNG 文件名，每次导入都不同，可以按名字缓存
}

// Dir 放导入的图片和 icons.json。
func Dir(home string) string { return filepath.Join(home, "project-icons") }

func listPath(home string) string { return filepath.Join(Dir(home), "icons.json") }

// Load 读出各项目的图标，键是项目 ID；没有或读不了时为空。
func Load(home string) map[string]Icon {
	var m map[string]Icon
	if b, err := os.ReadFile(listPath(home)); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// Save 写下各项目的图标：先写进各自的临时文件再改名，常驻进程不会读到半截，
// 两个窗口同时写也不会写进同一个临时文件。
func Save(home string, icons map[string]Icon) error {
	b, err := json.Marshal(icons)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(home), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(Dir(home), ".icons-*.json")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), listPath(home))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// Cache 记着上次读到的图标，文件的修改时间和大小没变就不再读：手机每 3 秒列一次工作区。
type Cache struct {
	mu    sync.Mutex
	stamp [2]int64
	icons map[string]Icon
}

// Load 同包级的 Load，文件没变时用上次读到的。
func (c *Cache) Load(home string) map[string]Icon {
	fi, err := os.Stat(listPath(home))
	if err != nil {
		return nil
	}
	stamp := [2]int64{fi.ModTime().UnixNano(), fi.Size()}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.icons == nil || stamp != c.stamp {
		c.icons, c.stamp = Load(home), stamp
	}
	return c.icons
}

// ReadImage 读一张导入的图片。name 来自手机，只认 Dir 下的文件名。
func ReadImage(home, name string) ([]byte, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".png" {
		return nil, errors.New("bad icon name")
	}
	return os.ReadFile(filepath.Join(Dir(home), name))
}
