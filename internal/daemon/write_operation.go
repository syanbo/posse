package daemon

import (
	"context"
	"sync"

	"posse/internal/rpc"
)

type workspaceOperationContext struct{}

type workspaceOperation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// beginWorkspaceOperation 让独立 CLI/手机写入也参与交接与退出门禁。
// 桌面执行器已登记接受，内部嵌套调用沿用同一次生命周期，不重复接受。
func (d *Daemon) beginWorkspaceOperation(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if owner, _ := ctx.Value(workspaceOperationContext{}).(*Daemon); owner == d {
		return ctx, func() {}, nil
	}
	if request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext); ok {
		if request.readOnly {
			return nil, nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
		}
		return context.WithValue(ctx, workspaceOperationContext{}, d), func() {}, nil
	}
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocked || c.closed {
		return nil, nil, rpc.Errorf(rpc.CodeBusy, "workspace writes are blocked during handoff")
	}
	ctx, cancel := context.WithCancel(ctx)
	op := &workspaceOperation{cancel: cancel, done: make(chan struct{})}
	if c.workspaceOperations == nil {
		c.workspaceOperations = make(map[*workspaceOperation]struct{})
	}
	c.workspaceOperations[op] = struct{}{}
	c.active++
	var once sync.Once
	finish := func() {
		once.Do(func() {
			cancel()
			c.mu.Lock()
			defer c.mu.Unlock()
			delete(c.workspaceOperations, op)
			c.active--
			close(op.done)
		})
	}
	return context.WithValue(ctx, workspaceOperationContext{}, d), finish, nil
}
