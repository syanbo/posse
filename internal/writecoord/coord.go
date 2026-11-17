package writecoord

import (
	"container/list"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

type Scope struct {
	Paths       []string
	SharedPaths []string
	Keys        []string
	SharedKeys  []string
}

type Coordinator struct {
	mu      sync.Mutex
	active  list.List
	waiting list.List
}

type request struct {
	ctx     context.Context
	scope   Scope
	ready   chan struct{}
	element *list.Element
	granted bool
}

// 路径身份由调用方统一（包括软链、大小写和硬链接）；这里不访问文件系统。
// ctx 只取消申请，不撤销已经返回的占用；成功后必须调用 release。
// 同一操作应一次申请全部资源，持有期间再次申请冲突资源会等待自身。
func (c *Coordinator) Acquire(ctx context.Context, scope Scope) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized := Scope{
		Paths:       slices.Clone(scope.Paths),
		SharedPaths: slices.Clone(scope.SharedPaths),
		Keys:        slices.Clone(scope.Keys),
		SharedKeys:  slices.Clone(scope.SharedKeys),
	}
	for _, paths := range [][]string{normalized.Paths, normalized.SharedPaths} {
		for i, path := range paths {
			if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
				return nil, fmt.Errorf("invalid coordination path %q: expected an absolute path without NUL", path)
			}
			paths[i] = filepath.Clean(path)
		}
	}
	r := &request{ctx: ctx, scope: normalized, ready: make(chan struct{})}
	c.mu.Lock()
	r.element = c.waiting.PushBack(r)
	c.dispatch()
	c.mu.Unlock()

	select {
	case <-r.ready:
	case <-ctx.Done():
	}

	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.remove(r)
		c.dispatch()
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	return sync.OnceFunc(func() {
		c.mu.Lock()
		c.remove(r)
		c.dispatch()
		c.mu.Unlock()
	}), nil
}

func (c *Coordinator) remove(r *request) {
	if r.element == nil {
		return
	}
	if r.granted {
		c.active.Remove(r.element)
	} else {
		c.waiting.Remove(r.element)
	}
	r.element = nil
}

func (c *Coordinator) dispatch() {
	for e := c.waiting.Front(); e != nil; {
		next := e.Next()
		r := e.Value.(*request)
		if r.ctx.Err() != nil {
			c.remove(r)
			close(r.ready)
		} else if !c.blocked(r) {
			c.remove(r)
			r.granted = true
			r.element = c.active.PushBack(r)
			close(r.ready)
		}
		e = next
	}
}

func (c *Coordinator) blocked(r *request) bool {
	for e := c.active.Front(); e != nil; e = e.Next() {
		if conflicts(r.scope, e.Value.(*request).scope) {
			return true
		}
	}
	// 尚未占用资源的旧请求也参与冲突检查，避免后来者反复抢走它所需的资源。
	for e := c.waiting.Front(); e != r.element; e = e.Next() {
		if conflicts(r.scope, e.Value.(*request).scope) {
			return true
		}
	}
	return false
}

func conflicts(a, b Scope) bool {
	if pathsOverlap(a.Paths, b.Paths) || pathsOverlap(a.Paths, b.SharedPaths) || pathsOverlap(a.SharedPaths, b.Paths) {
		return true
	}
	for _, key := range a.Keys {
		if slices.Contains(b.Keys, key) || slices.Contains(b.SharedKeys, key) {
			return true
		}
	}
	for _, key := range a.SharedKeys {
		if slices.Contains(b.Keys, key) {
			return true
		}
	}
	return false
}

func pathsOverlap(a, b []string) bool {
	for _, pathA := range a {
		for _, pathB := range b {
			if containsPath(pathA, pathB) || containsPath(pathB, pathA) {
				return true
			}
		}
	}
	return false
}

func containsPath(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
}
