package spawn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CommitServerVersion 持有 versions/.lock 原子提交一个暂存目录到 <realHome>/server/versions/<tag>：
// 1. 校验暂存目录内的 posse 与 libName 是普通文件（拒绝符号链接）且 SHA-256 匹配；
// 2. 已存在同 tag 目录时保持不可变（绝不 rm -rf），只核对完整身份并刷新引用；
// 3. 新建目录写入受管标记与临时引用后原子改名进 versions/，不提前切换 server/current。
func CommitServerVersion(home, stageDir, tag, libName, posseSHA, libSHA string) (string, error) {
	if home == "" {
		return "", errors.New("server home required")
	}
	if !validServerTag(tag) {
		return "", fmt.Errorf("invalid server version tag %q", tag)
	}
	if libName == "" || strings.ContainsAny(libName, "/\\") || libName == "." || libName == ".." {
		return "", fmt.Errorf("invalid server library name %q", libName)
	}
	realHome, err := resolveOrCreateHome(home)
	if err != nil {
		return "", err
	}
	if err := verifyRegularFileSHA(filepath.Join(stageDir, "posse"), posseSHA); err != nil {
		return "", fmt.Errorf("staged posse invalid: %w", err)
	}
	if err := verifyRegularFileSHA(filepath.Join(stageDir, libName), libSHA); err != nil {
		return "", fmt.Errorf("staged %s invalid: %w", libName, err)
	}

	serverDir := filepath.Join(realHome, "server")
	versionsDir := filepath.Join(serverDir, "versions")
	if err := ensurePrivateDir(serverDir); err != nil {
		return "", err
	}
	if err := ensurePrivateDir(versionsDir); err != nil {
		return "", err
	}
	unlock, err := lockDir(versionsDir)
	if err != nil {
		return "", err
	}
	defer unlock()

	dest := filepath.Join(versionsDir, tag)
	if st, err := os.Lstat(dest); err == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("server version path %s is not a directory", dest)
		}
		if !hasManagedMarker(dest) {
			return "", fmt.Errorf("existing directory %s is not managed by posse", dest)
		}
		if err := verifyRegularFileSHA(filepath.Join(dest, "posse"), posseSHA); err != nil {
			return "", fmt.Errorf("existing version %s posse mismatch: %w", tag, err)
		}
		if err := verifyRegularFileSHA(filepath.Join(dest, libName), libSHA); err != nil {
			return "", fmt.Errorf("existing version %s %s mismatch: %w", tag, libName, err)
		}
		if err := addRef(dest, realHome); err != nil {
			return "", err
		}
		_ = os.RemoveAll(stageDir)
		return filepath.Join(dest, "posse"), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if err := os.Chmod(filepath.Join(stageDir, "posse"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stageDir, ServerManagedMarker), []byte(tag+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := addRef(stageDir, realHome); err != nil {
		return "", err
	}
	if err := os.Rename(stageDir, dest); err != nil {
		return "", fmt.Errorf("commit server version %s: %w", tag, err)
	}
	return filepath.Join(dest, "posse"), nil
}

// ActivateServerVersion 持有 versions/.lock 原子切换 <realHome>/server/current -> versions/<tag>。
// 若 current 已存在但不是符号链接（如实体目录），直接报错而非在其内部建链。
func ActivateServerVersion(home, tag string) (string, error) {
	return ActivateServerVersionContext(context.Background(), home, tag)
}

// ActivateServerVersionContext 带可取消 ctx 持有 versions/.lock 并原子切换 server/current。
// 等待锁和切换前均响应 ctx.Done()，防止已交接退出或被取消的进程阻塞或覆盖新版本。
func ActivateServerVersionContext(ctx context.Context, home, tag string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("server home required")
	}
	if !validServerTag(tag) {
		return "", fmt.Errorf("invalid server version tag %q", tag)
	}
	realHome, err := resolveOrCreateHome(home)
	if err != nil {
		return "", err
	}
	serverDir := filepath.Join(realHome, "server")
	versionsDir := filepath.Join(serverDir, "versions")
	unlock, err := lockDirCtx(ctx, versionsDir)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}

	dest := filepath.Join(versionsDir, tag)
	if !hasManagedMarker(dest) {
		return "", fmt.Errorf("server version %s is not installed", tag)
	}
	exe := filepath.Join(dest, "posse")
	if info, err := os.Lstat(exe); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("server version %s executable missing", tag)
	}

	link := filepath.Join(serverDir, "current")
	if info, err := os.Lstat(link); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("server current path %s exists and is not a symlink", link)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmpLink := filepath.Join(serverDir, fmt.Sprintf(".current-%d-%s", os.Getpid(), hex.EncodeToString(rnd[:])))
	_ = os.Remove(tmpLink)
	if err := os.Symlink(filepath.Join("versions", tag), tmpLink); err != nil {
		return "", err
	}
	if err := os.Rename(tmpLink, link); err != nil {
		_ = os.Remove(tmpLink)
		return "", fmt.Errorf("activate server version %s: %w", tag, err)
	}
	if err := addRef(dest, realHome); err != nil {
		return "", err
	}
	return exe, nil
}

func resolveOrCreateHome(home string) (string, error) {
	home = filepath.Clean(home)
	if info, err := os.Lstat(home); err == nil {
		// 若已存在符号链接则解析到真实目录，不主动 chmod 已存在的 home
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, rerr := filepath.EvalSymlinks(home)
			if rerr != nil {
				return "", rerr
			}
			home = resolved
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(home, 0o700); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		return resolved, nil
	}
	return home, nil
}

func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("server path %s must be a real directory", dir)
	}
	return nil
}

func verifyRegularFileSHA(path, wantSHA string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if wantSHA == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("%s sha256 mismatch: got %s, want %s", filepath.Base(path), got, wantSHA)
	}
	return nil
}
