package gitx

import (
	"context"
	"errors"
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
	// 扫描只用四分之三的预算，读满时按没找全交回已找到的仓库；剩下的留给下面逐个核对身份
	scan, settle := carveReadBudget(ctx, 3, 4)
	d := discover(scan, dir, multi, true)
	out := d.result()
	settle()
	if ctx.Err() != nil {
		return Discovery{}, nil, ctx.Err()
	}
	if d.err != nil {
		return Discovery{}, nil, d.err
	}
	// 核对身份读满预算时停下来：已核对过的照常交回，不算失败，免得侧栏把同一条提示反复弹出来
	kept := make([]Repo, 0, len(out.Repos))
	ids := make(map[string]RepositoryIdentity, len(out.Repos))
	for _, repo := range out.Repos {
		id, ok := d.identities[repo.Real]
		if !ok {
			out.Partial = true
			break
		}
		limited := false
		var checkErr error
		for _, path := range append([]string{repo.Root}, repo.Aliases...) {
			if err := CheckRepositoryIdentity(ctx, path, id); err != nil {
				if errors.Is(err, ErrReadLimit) {
					limited = true
					break
				}
				checkErr = err
				break
			}
		}
		if checkErr != nil {
			return Discovery{}, nil, checkErr
		}
		if limited {
			out.Partial = true
			break
		}
		kept = append(kept, repo)
		ids[repo.Real] = id
	}
	out.Repos = kept
	after, afterDev, afterIno, err := directoryIdentity(original)
	if err != nil {
		return Discovery{}, nil, err
	}
	if real != after || dev != afterDev || ino != afterIno {
		return Discovery{}, nil, ErrRepositoryChanged
	}
	out.RealPath = real
	return out, ids, ctx.Err()
}

func (d *discoverer) failed(err error) bool {
	if d.checked && err != nil {
		// 目录多、嵌套仓库大的文件夹会读满预算：停下不再找，不算失败
		if errors.Is(err, ErrReadLimit) {
			d.partial = true
			return true
		}
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

// carveReadBudget 从 ctx 的读取预算里划出 num/den 给一段读取单独用，settle 把实际用掉的记回原预算。
// ctx 没有预算时原样返回。
func carveReadBudget(ctx context.Context, num, den int) (context.Context, func()) {
	parent, ok := ctx.Value(readLimitKey{}).(*readBudget)
	if !ok {
		return ctx, func() {}
	}
	parent.Lock()
	n := parent.remaining / den * num
	parent.Unlock()
	child := &readBudget{remaining: n}
	return context.WithValue(ctx, readLimitKey{}, child), func() {
		child.Lock()
		used := n - child.remaining
		child.Unlock()
		parent.Lock()
		parent.remaining = max(0, parent.remaining-used)
		parent.Unlock()
	}
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
