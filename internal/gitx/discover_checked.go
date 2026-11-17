package gitx

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DiscoverChecked 沿用本机的别名、嵌套仓库与忽略规则，额外保留读取失败和查询期间的身份。
func DiscoverChecked(ctx context.Context, dir string, multi bool) (Discovery, map[string]RepositoryIdentity, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, nil, err
	}
	original := dir
	real, dev, ino, err := directoryIdentity(original)
	if err != nil {
		return Discovery{}, nil, err
	}
	// 先按文件系统解析 link/..，再清理展示路径；普通软链别名仍保留。
	if strings.Contains("/"+dir+"/", "/../") {
		dir = real
	}
	d := discover(ctx, dir, multi, true)
	out := d.result()
	if ctx.Err() != nil {
		return Discovery{}, nil, ctx.Err()
	}
	if d.err != nil {
		return Discovery{}, nil, d.err
	}
	for _, repo := range out.Repos {
		id := d.identities[repo.Real]
		for _, path := range append([]string{repo.Root}, repo.Aliases...) {
			if err := CheckRepositoryIdentity(ctx, path, id); err != nil {
				return Discovery{}, nil, err
			}
		}
	}
	after, afterDev, afterIno, err := directoryIdentity(original)
	if err != nil {
		return Discovery{}, nil, err
	}
	if real != after || dev != afterDev || ino != afterIno {
		return Discovery{}, nil, ErrRepositoryChanged
	}
	out.RealPath = real
	return out, d.identities, ctx.Err()
}

func (d *discoverer) failed(err error) bool {
	if d.checked && err != nil {
		if d.err == nil {
			d.err = err
		}
		return true
	}
	return false
}

func (d *discoverer) run(root string, args ...string) ([]byte, error) {
	if d.err != nil {
		return nil, d.err
	}
	out, err := runRaw(d.ctx, root, nil, nil, args...)
	d.failed(err)
	return out, err
}

func (d *discoverer) topLevel(root string) string {
	if !d.checked {
		return TopLevel(d.ctx, root)
	}
	out, err := d.run(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(string(out), "\n")
}

func (d *discoverer) hasDotGit(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil && !os.IsNotExist(err) {
		d.failed(err)
	}
	return err == nil
}

func (d *discoverer) enclosingRoot(dir string) string {
	if !d.checked {
		return enclosingRoot(dir)
	}
	for {
		if d.hasDotGit(dir) {
			return dir
		}
		if d.err != nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func (d *discoverer) bareRepo(dir string) bool {
	if !d.checked {
		return bareRepo(dir)
	}
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := d.stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

func (d *discoverer) lstat(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		d.failed(err)
	}
	return fi, err
}
func (d *discoverer) stat(path string) (os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		d.failed(err)
	}
	return fi, err
}
func (d *discoverer) eval(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil && !os.IsNotExist(err) {
		d.failed(err)
	}
	return real, err
}

func consumeReadBudget(ctx context.Context, size int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b, ok := ctx.Value(readLimitKey{}).(*readBudget); ok {
		b.Lock()
		defer b.Unlock()
		if size > b.remaining {
			return ErrReadLimit
		}
		b.remaining -= size
	}
	return nil
}

func (d *discoverer) readDir(dir string) ([]os.DirEntry, error) {
	if !d.checked {
		return os.ReadDir(dir)
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []os.DirEntry
	for {
		if err := d.ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := f.ReadDir(128)
		for _, entry := range batch {
			if e := consumeReadBudget(d.ctx, len(entry.Name())+64); e != nil {
				return nil, e
			}
			out = append(out, entry)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return out, nil
}
