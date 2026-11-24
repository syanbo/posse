package tslink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 是内置 Tailscale 的持久化配置（Mac 上存 $POSSE_HOME/tailscale.json）。
// AuthKey 只作为注册时的一次性参数传入内存，绝不写进配置文件。
type Config struct {
	Enabled              bool   `json:"enabled"`
	Hostname             string `json:"hostname,omitempty"`
	ControlURL           string `json:"controlUrl,omitempty"`
	DisableBindInterface bool   `json:"disableBindInterface,omitempty"`
}

// ConfigFile 返回配置文件路径。
func ConfigFile(home string) string { return filepath.Join(home, "tailscale.json") }

// StateDir 返回 tsnet 节点状态目录（Mac 上为 $POSSE_HOME/tailscale）。
func StateDir(home string) string { return filepath.Join(home, "tailscale") }

// LoadConfig 读取并校验配置；文件不存在时返回零值（默认关闭）。
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
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.ControlURL = strings.TrimSpace(c.ControlURL)
	if err := ValidateConfig(c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ConfigFile(home), err)
	}
	return c, nil
}

// SaveConfig 并发安全地原子写入配置（0600 权限）。
func SaveConfig(home string, c Config) error {
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.ControlURL = strings.TrimSpace(c.ControlURL)
	if err := ValidateConfig(c); err != nil {
		return err
	}
	path := ConfigFile(home)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tailscale.json.tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// ValidateConfig 校验配置项是否合法。
func ValidateConfig(c Config) error {
	if c.ControlURL != "" {
		if err := ValidateControlURL(c.ControlURL); err != nil {
			return err
		}
	}
	return nil
}

// ValidateControlURL 校验自定义控制面地址（例如 Headscale）：
// 仅接受 http/https、非空主机名、有效端口（1..65535，不允许空端口）、无 userinfo/query/fragment（含空 ? 或 #）。
func ValidateControlURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.ContainsAny(raw, "?#") {
		return errors.New("invalid Tailscale control URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("invalid Tailscale control URL")
	}
	// 拒绝 "https://host:" 或 "https://[::1]:" 这种空端口写法
	if strings.HasSuffix(u.Host, ":") {
		return errors.New("invalid Tailscale control URL")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strings.HasPrefix(p, "0") {
			return errors.New("invalid Tailscale control URL")
		}
	}
	return nil
}

// ValidateStateDir 校验节点状态目录：必须是绝对路径、不包含 ".." 路径段且不能是根目录。
func ValidateStateDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("Tailscale state directory is required")
	}
	for _, seg := range strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "", errors.New("invalid Tailscale state directory")
		}
	}
	if !filepath.IsAbs(raw) {
		return "", errors.New("invalid Tailscale state directory")
	}
	cleaned := filepath.Clean(raw)
	if cleaned == "/" || cleaned == filepath.VolumeName(cleaned)+string(filepath.Separator) {
		return "", errors.New("invalid Tailscale state directory")
	}
	return cleaned, nil
}

// SanitizeHostname 把任意主机名规整为合法的 DNS 标签（小写字母、数字和连字符，最长 63 字节）。
func SanitizeHostname(name, fallback string) string {
	clean := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if i := strings.IndexByte(s, '.'); i > 0 {
			s = s[:i]
		}
		var b strings.Builder
		lastDash := false
		for i := 0; i < len(s) && b.Len() < 63; i++ {
			ch := s[i]
			switch {
			case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
				b.WriteByte(ch)
				lastDash = false
			case ch == '-' || ch == '_' || ch == ' ':
				if b.Len() > 0 && !lastDash {
					b.WriteByte('-')
					lastDash = true
				}
			}
		}
		return strings.Trim(b.String(), "-")
	}
	if out := clean(name); out != "" {
		return out
	}
	if out := clean(fallback); out != "" {
		return out
	}
	return "posse"
}

// DefaultHostname 返回当前机器的默认节点名（如 posse-macbook-pro）。
func DefaultHostname(prefix string) string {
	host, _ := os.Hostname()
	if host != "" && prefix != "" {
		return SanitizeHostname(prefix+"-"+host, prefix)
	}
	if host != "" {
		return SanitizeHostname(host, "posse")
	}
	return SanitizeHostname(prefix, "posse")
}

// cgnatPrefix 是 Tailscale IPv4 地址段 100.64.0.0/10。
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// IsTailscaleIP 判断地址是否属于 Tailscale IPv4 段（100.64.0.0/10）。
func IsTailscaleIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.Is4() && cgnatPrefix.Contains(ip)
}

// IsTailscaleIPString 判断字符串是否是 Tailscale IPv4 地址。
func IsTailscaleIPString(s string) bool {
	ip, err := netip.ParseAddr(strings.TrimSpace(s))
	return err == nil && IsTailscaleIP(ip)
}
