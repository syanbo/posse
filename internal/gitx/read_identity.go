package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrRepositoryChanged = errors.New("Git repository identity changed during the query")

// RepositoryIdentity 区分工作区、每个 worktree 的 gitdir，以及共享对象和引用的目录。
type RepositoryIdentity struct {
	Root         string `json:"root"`
	GitDir       string `json:"gitDir"`
	CommonDir    string `json:"commonDir"`
	RootDevice   uint64 `json:"rootDevice"`
	RootInode    uint64 `json:"rootInode"`
	GitDevice    uint64 `json:"gitDevice"`
	GitInode     uint64 `json:"gitInode"`
	CommonDevice uint64 `json:"commonDevice"`
	CommonInode  uint64 `json:"commonInode"`
}

func directoryIdentity(path string) (real string, dev, ino uint64, err error) {
	real, err = filepath.EvalSymlinks(path)
	if err != nil {
		return
	}
	var fi os.FileInfo
	fi, err = os.Stat(real)
	if err != nil {
		return
	}
	if !fi.IsDir() {
		err = errors.New("Git query path is not a directory")
		return
	}
	st := fi.Sys().(*syscall.Stat_t)
	return real, uint64(st.Dev), uint64(st.Ino), nil
}

func RepositoryIdentityChecked(ctx context.Context, path string) (RepositoryIdentity, error) {
	var id RepositoryIdentity
	if err := ctx.Err(); err != nil {
		return id, err
	}
	// 路径可能经过软链；在第一条 Git 命令之前保存目录身份，避免读取中途被替换。
	real, dev, ino, err := directoryIdentity(path)
	if err != nil {
		return id, err
	}
	values := []*string{&id.Root, &id.GitDir, &id.CommonDir}
	for i, arg := range []string{"--show-toplevel", "--absolute-git-dir", "--git-common-dir"} {
		out, e := runRaw(ctx, path, nil, nil, "rev-parse", "--path-format=absolute", arg)
		if e != nil {
			return id, e
		}
		p := strings.TrimSuffix(string(out), "\n")
		if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) || len(p) > 4096 {
			return id, errors.New("invalid Git repository identity")
		}
		*values[i] = p
	}
	id.Root, id.RootDevice, id.RootInode, err = directoryIdentity(id.Root)
	if err != nil {
		return id, err
	}
	id.GitDir, id.GitDevice, id.GitInode, err = directoryIdentity(id.GitDir)
	if err != nil {
		return id, err
	}
	id.CommonDir, id.CommonDevice, id.CommonInode, err = directoryIdentity(id.CommonDir)
	if err != nil {
		return id, err
	}
	after, afterDev, afterIno, err := directoryIdentity(path)
	if err != nil {
		return id, err
	}
	if real != after || dev != afterDev || ino != afterIno {
		return id, ErrRepositoryChanged
	}
	return id, ctx.Err()
}

func CheckRepositoryIdentity(ctx context.Context, path string, expected RepositoryIdentity) error {
	actual, err := RepositoryIdentityChecked(ctx, path)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrRepositoryChanged
	}
	return nil
}

// ReadStatusChecked 保留状态解析，只把操作标记的查询失败也交还调用方。
func ReadStatusChecked(ctx context.Context, dir string) (*Status, error) {
	ctx = context.WithValue(ctx, indexIDKey{}, &indexFingerprint{})
	out, err := runRaw(ctx, dir, nil, nil, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	st := parseStatus(out)
	st.IndexID, err = IndexID(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, marker := range []struct{ name, operation string }{
		{"rebase-merge", "rebase"}, {"rebase-apply", "rebase"}, {"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"},
	} {
		b, err := runRaw(ctx, dir, nil, nil, "rev-parse", "--path-format=absolute", "--git-path", marker.name)
		if err != nil {
			return nil, err
		}
		_, err = os.Stat(strings.TrimSuffix(string(b), "\n"))
		if err == nil {
			st.Operation = marker.operation
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return st, ctx.Err()
}

// NumStatHEADChecked 不把 HEAD 损坏、取消或输出超限降级成另一种 diff。
func NumStatHEADChecked(ctx context.Context, dir string) (map[string]LineStats, error) {
	out, err := runRaw(ctx, dir, nil, nil, "diff", "--no-ext-diff", "HEAD", "--numstat", "-z", "--find-renames")
	if err != nil {
		return nil, err
	}
	return parseNumStat(out), nil
}
