// Package remote 通过系统 OpenSSH 连接另一台 Mac 的 Posse。
package remote

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

type Host struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Target     string `json:"target"`
	User       string `json:"user,omitempty"`
	Port       int    `json:"port,omitempty"`
	Executable string `json:"executable,omitempty"`
	Home       string `json:"home,omitempty"`
	Icon       string `json:"icon,omitempty"`
	IconImage  string `json:"iconImage,omitempty"`
}

// SameConnection 判断两条主机记录的连接传输参数是否一致（忽略仅用于界面展示的名称与自定义图标）。
func (h Host) SameConnection(other Host) bool {
	return h.ID == other.ID &&
		h.Target == other.Target &&
		h.User == other.User &&
		h.Port == other.Port &&
		h.Executable == other.Executable &&
		h.Home == other.Home
}

func (h Host) Validate() error {
	if strings.TrimSpace(h.Name) == "" {
		return errors.New("host name required")
	}
	if !validTarget(h.Target) {
		return errors.New("invalid SSH target")
	}
	if h.User != "" && (!validTarget(h.User) || strings.ContainsAny(h.User, "@:[]")) {
		return errors.New("invalid SSH user")
	}
	if h.User != "" && strings.Contains(h.Target, "@") {
		return errors.New("specify SSH user either in target or separately")
	}
	if h.Port < 0 || h.Port > 65535 {
		return errors.New("SSH port must be between 1 and 65535")
	}
	for _, p := range []string{h.Executable, h.Home} {
		if p != "" && ((!filepath.IsAbs(p) && !strings.HasPrefix(p, "~/")) || strings.ContainsAny(p, "\x00\r\n")) {
			return errors.New("remote paths must be absolute or start with ~/")
		}
	}
	if strings.ContainsAny(h.Icon, "\x00\r\n") || utf8.RuneCountInString(strings.TrimSpace(h.Icon)) > 16 {
		return errors.New("invalid host icon")
	}
	if h.IconImage != "" && (filepath.Base(h.IconImage) != h.IconImage || h.IconImage == "." || h.IconImage == ".." || strings.ContainsAny(h.IconImage, "/\\\x00\r\n")) {
		return errors.New("invalid host icon image")
	}
	return nil
}

func validTarget(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.Count(s, "@") > 1 {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-@[]:", r)) {
			return false
		}
	}
	if i := strings.IndexByte(s, '@'); i >= 0 && (i == 0 || i == len(s)-1 || s[i+1] == '-') {
		return false
	}
	return true
}

type hostFile struct {
	Version int    `json:"version"`
	Hosts   []Host `json:"hosts"`
}

func Load(home string) ([]Host, error) {
	b, err := os.ReadFile(filepath.Join(home, "hosts.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f hostFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("unsupported host configuration version %d", f.Version)
	}
	ids := map[string]bool{}
	for _, h := range f.Hosts {
		if err := h.Validate(); err != nil {
			return nil, err
		}
		if h.ID == "" || ids[h.ID] {
			return nil, errors.New("invalid or duplicate host ID")
		}
		ids[h.ID] = true
	}
	return f.Hosts, nil
}

// Put 保留编辑中的 ID；跨窗口的读改写由文件锁协调，避免各自覆盖另一台主机。
func Put(home string, h Host) (Host, error) {
	if err := h.Validate(); err != nil {
		return h, err
	}
	err := updateHosts(home, func(hosts []Host) ([]Host, error) {
		if h.ID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return nil, err
			}
			h.ID = "h_" + hex.EncodeToString(id[:])
			return append(hosts, h), nil
		}
		for i := range hosts {
			if hosts[i].ID == h.ID {
				hosts[i] = h
				return hosts, nil
			}
		}
		return nil, fmt.Errorf("host %q not found", h.ID)
	})
	return h, err
}

// PutUnique 在文件锁内同时校验名称唯一性与（编辑时的）原快照未变，供桌面弹窗安全读写。
func PutUnique(home string, h Host, expected *Host) (Host, error) {
	if err := h.Validate(); err != nil {
		return h, err
	}
	err := updateHosts(home, func(hosts []Host) ([]Host, error) {
		for _, other := range hosts {
			if other.ID != h.ID && other.Name == h.Name {
				return nil, errors.New("host name already exists")
			}
		}
		if h.ID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return nil, err
			}
			h.ID = "h_" + hex.EncodeToString(id[:])
			return append(hosts, h), nil
		}
		for i := range hosts {
			if hosts[i].ID == h.ID {
				if expected != nil && hosts[i] != *expected {
					return nil, errors.New("host configuration changed; reopen the dialog to edit")
				}
				hosts[i] = h
				return hosts, nil
			}
		}
		return nil, fmt.Errorf("host %q not found", h.ID)
	})
	return h, err
}

// RemoveIf 仅在锁内核对磁盘记录仍与 expected 一致时移除，避免删掉刚被他处修改的主机。
func RemoveIf(home string, expected Host) error {
	return updateHosts(home, func(hosts []Host) ([]Host, error) {
		i := slices.IndexFunc(hosts, func(h Host) bool { return h.ID == expected.ID })
		if i < 0 {
			return nil, fmt.Errorf("host %q not found", expected.ID)
		}
		if hosts[i] != expected {
			return nil, errors.New("host configuration changed; reopen the dialog to edit")
		}
		return slices.Delete(hosts, i, i+1), nil
	})
}

func Remove(home, id string) error {
	return updateHosts(home, func(hosts []Host) ([]Host, error) {
		i := slices.IndexFunc(hosts, func(h Host) bool { return h.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("host %q not found", id)
		}
		return slices.Delete(hosts, i, i+1), nil
	})
}

func Find(home, selector string) (Host, error) {
	hosts, err := Load(home)
	if err != nil {
		return Host{}, err
	}
	for _, h := range hosts {
		if h.ID == selector {
			return h, nil
		}
	}
	var matches []Host
	for _, h := range hosts {
		if h.Name == selector {
			matches = append(matches, h)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Host{}, fmt.Errorf("host %q is ambiguous; use its ID", selector)
	}
	return Host{}, fmt.Errorf("host %q not found", selector)
}

func updateHosts(home string, fn func([]Host) ([]Host, error)) error {
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(home, "hosts.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	hosts, err := Load(home)
	if err != nil {
		return err
	}
	hosts, err = fn(hosts)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(hostFile{Version: 1, Hosts: hosts}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(home, ".hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(home, "hosts.json"))
}
