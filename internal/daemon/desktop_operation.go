package daemon

import (
	"context"
	"encoding/json"

	"posse/internal/rpc"
)

func (d *Daemon) desktopOperationCancel(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.DesktopOperationCancel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if !rpc.ValidDesktopOperationRef(p.Operation) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	if p.Desktop.InstanceID != d.instanceID || p.Operation.Desktop.InstanceID != d.instanceID {
		return nil, rpc.Errorf(rpc.CodeConflict, "daemon instance changed; reconnect before making desktop requests")
	}
	if !d.desktopValidLocked(&p.Desktop) {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.pruneOperationsLocked()
	if op := c.operations[p.Operation]; op != nil {
		// 信号不表示操作已结束或回滚；handler 退出后仍由原执行器登记真实结果。
		if op.value.State == "running" {
			op.cancel()
		}
		return op.value, nil
	}
	return rpc.DesktopOperation{DesktopOperationRef: p.Operation, State: "unknown"}, nil
}
