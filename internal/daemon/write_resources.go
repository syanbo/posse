package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/text/unicode/norm"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/writecoord"
)

// resourcePath 解析已有前缀的软链，保留尚未创建的后缀供目录删除与子路径写入协调。
func resourcePath(path string) (string, error) {
	return resolveResourcePath(path, 0)
}

func resolveResourcePath(path string, links int) (string, error) {
	if links > 40 {
		return "", &os.PathError{Op: "readlink", Path: path, Err: syscall.ELOOP}
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, string(filepath.Separator))
		if path == "" {
			path = string(filepath.Separator)
		}
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = cwd + string(filepath.Separator) + path
	}
	real, err := filepath.EvalSymlinks(path)
	if err == nil || !os.IsNotExist(err) {
		return real, err
	}
	if fi, linkErr := os.Lstat(path); linkErr == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return "", err
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			dir, _ := filepath.Split(path)
			target = dir + target
		}
		// 资源身份仍指向丢失的目标，重定位和清理不必先修好旧软链。
		return resolveResourcePath(target, links+1)
	} else if !os.IsNotExist(linkErr) {
		return "", linkErr
	}
	dir, name := filepath.Split(strings.TrimSuffix(path, string(filepath.Separator)))
	if name == ".." || name == "." || dir == path || dir == "" {
		return "", err
	}
	parent, err := resolveResourcePath(dir, links)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, name), nil
}

// resourceLinks 保留路径途经的链接条目，包括链接目标内部的链接。
// 这些条目只需共享占用；锁住父目录会使 /var 这类系统链接下的无关写入也互相等待。
func resourceLinks(ctx context.Context, path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		path = cwd + string(filepath.Separator) + path
	}
	pending := []string{path}
	seen := map[string]bool{}
	var links []string
	for len(pending) > 0 {
		path = pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for path = strings.TrimRight(path, string(filepath.Separator)); path != ""; {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			dir, name := filepath.Split(path)
			fi, err := os.Lstat(path)
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			if err == nil && fi.Mode()&os.ModeSymlink != 0 {
				parent, err := resourcePath(dir)
				if err != nil {
					return nil, err
				}
				entry := filepath.Join(parent, name)
				if !seen[entry] {
					if len(seen) >= 40 {
						return nil, &os.PathError{Op: "readlink", Path: path, Err: syscall.ELOOP}
					}
					seen[entry] = true
					links = append(links, resourceKey(entry))
					target, err := os.Readlink(path)
					if err != nil {
						return nil, err
					}
					if !filepath.IsAbs(target) {
						target = parent + string(filepath.Separator) + target
					}
					pending = append(pending, target)
				}
			}
			// 先按原路径查询，再逐段退回；提前 Clean 会改变 link/.. 的含义。
			path = strings.TrimRight(dir, string(filepath.Separator))
		}
	}
	return links, nil
}

func resourceKey(path string) string {
	if runtime.GOOS == "darwin" {
		// 默认 APFS 忽略大小写与 Unicode 组合形式；大小写敏感卷上至多多串行一些请求。
		return strings.ToLower(norm.NFC.String(path))
	}
	return path
}

func writeScope(ctx context.Context, paths []string, keys []string, allowGoneRepo bool) (writecoord.Scope, error) {
	scope := writecoord.Scope{Keys: slices.Clone(keys)}
	seen := map[string]bool{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return scope, err
		}
		real, err := resourcePath(path)
		if err != nil {
			return scope, err
		}
		links, err := resourceLinks(ctx, path)
		if err != nil {
			return scope, err
		}
		scope.SharedPaths = append(scope.SharedPaths, links...)
		key := resourceKey(real)
		if seen[key] {
			continue
		}
		seen[key] = true
		scope.Paths = append(scope.Paths, key)
		dir := real
		for {
			fi, err := os.Stat(dir)
			if err == nil {
				if !fi.IsDir() {
					dir = filepath.Dir(dir)
				}
				break
			}
			if !os.IsNotExist(err) || filepath.Dir(dir) == dir {
				return scope, err
			}
			dir = filepath.Dir(dir)
		}
		common, err := gitx.CommonDirChecked(ctx, dir)
		if err != nil {
			// 已丢失主仓库时，残留 worktree 的 .git 仍指向失效位置；只能按路径协调清理。
			if !allowGoneRepo || !strings.HasPrefix(err.Error(), "fatal: not a git repository:") {
				return scope, err
			}
		} else if common != "" {
			scope.Keys = append(scope.Keys, "git:"+resourceKey(common))
		}
	}
	slices.Sort(scope.Paths)
	scope.Paths = slices.Compact(scope.Paths)
	slices.Sort(scope.SharedPaths)
	scope.SharedPaths = slices.Compact(scope.SharedPaths)
	slices.Sort(scope.Keys)
	scope.Keys = slices.Compact(scope.Keys)
	return scope, nil
}

type projectWriteState struct {
	project    store.Project
	workspaces []store.Workspace
}

func (d *Daemon) projectWriteState(id string) (projectWriteState, error) {
	p, err := d.store.FindProject(id)
	if err != nil {
		return projectWriteState{}, err
	}
	ws, err := d.store.Workspaces(p.ID)
	if err != nil {
		return projectWriteState{}, err
	}
	slices.SortFunc(ws, func(a, b store.Workspace) int { return strings.Compare(a.ID, b.ID) })
	return projectWriteState{p, ws}, nil
}

func (s projectWriteState) paths(extra []string) []string {
	paths := append([]string{s.project.Path}, extra...)
	for _, w := range s.workspaces {
		paths = append(paths, w.Path)
		for _, r := range w.Repos {
			paths = append(paths, r.Source, repoDir(w.Path, r.Path))
		}
	}
	return paths
}

func (d *Daemon) lockProjectSettings(ctx context.Context, id string) (store.Project, func(), error) {
	release, err := d.writes.Acquire(ctx, writecoord.Scope{Keys: []string{"project:" + id}})
	if err != nil {
		return store.Project{}, nil, err
	}
	project, err := d.store.FindProject(id)
	if err != nil {
		release()
		return project, nil, err
	}
	return project, release, nil
}

func (d *Daemon) lockWritePaths(ctx context.Context, paths []string) (func(), error) {
	scope, err := writeScope(ctx, paths, nil, false)
	if err != nil {
		return nil, err
	}
	release, err := d.writes.Acquire(ctx, scope)
	if err != nil {
		return nil, err
	}
	current, err := writeScope(ctx, paths, nil, false)
	if err != nil || !reflect.DeepEqual(scope, current) {
		release()
		if err != nil {
			return nil, err
		}
		return nil, workspaceWriteChanged()
	}
	return release, nil
}

func workspaceWriteChanged() error {
	return rpc.Errorf(rpc.CodeConflict, "workspace resources changed; refresh before trying again")
}

// 项目登记与路径变更共用项目键；磁盘路径与 common-dir 同时覆盖其他项目登记的同仓库 worktree。
// 资源一次取齐，拿到后复查登记与实际路径，不能把排队前解析出的目标用于新的工作区布局。
func (d *Daemon) lockProjectWrite(ctx context.Context, id string, extra []string, allowGoneRepo bool) (projectWriteState, func(), error) {
	before, err := d.projectWriteState(id)
	if err != nil {
		return before, nil, err
	}
	scope, err := writeScope(ctx, before.paths(extra), []string{"project:" + before.project.ID}, allowGoneRepo)
	if err != nil {
		return before, nil, err
	}
	release, err := d.writes.Acquire(ctx, scope)
	if err != nil {
		return before, nil, err
	}
	after, err := d.projectWriteState(id)
	if err != nil || !reflect.DeepEqual(before, after) {
		release()
		return after, nil, workspaceWriteChanged()
	}
	current, err := writeScope(ctx, after.paths(extra), []string{"project:" + after.project.ID}, allowGoneRepo)
	if err != nil || !reflect.DeepEqual(scope, current) {
		release()
		if err != nil {
			return after, nil, err
		}
		return after, nil, workspaceWriteChanged()
	}
	return after, release, nil
}

func (s projectWriteState) workspace(id string) (store.Workspace, error) {
	for _, w := range s.workspaces {
		if w.ID == id {
			return w, nil
		}
	}
	return store.Workspace{}, workspaceWriteChanged()
}
