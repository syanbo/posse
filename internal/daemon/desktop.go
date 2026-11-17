package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"posse/internal/rpc"
)

const desktopLease = 15 * time.Second

type desktopControl struct {
	mu                  sync.Mutex
	owner, label        string
	epoch               uint64
	expires             time.Time
	timer               *time.Timer
	changed             chan struct{}
	active              int
	blocked             bool
	closed              bool
	workspaceOperations map[*workspaceOperation]struct{}
	operations          map[rpc.DesktopOperationRef]*desktopOperation
	operationBytes      int
	saveInputBytes      int
	sequences           desktopSequences
	// 控制转移时同步关闭应答订阅，让内核及时接回应答权。
	writers map[*desktopWriter]struct{}
}

type desktopWriter struct{ close func() }

func (c *desktopControl) initLocked() {
	if c.changed == nil {
		c.changed = make(chan struct{})
		c.epoch = 1
	}
}

func (c *desktopControl) revokeLocked() {
	c.initLocked()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	for w := range c.writers {
		w.close()
		delete(c.writers, w)
	}
	c.owner, c.label = "", ""
	c.expires = time.Time{}
	c.epoch++
	c.sequences = desktopSequences{}
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *desktopControl) expireLocked() {
	c.initLocked()
	if c.owner != "" && !time.Now().Before(c.expires) {
		c.revokeLocked()
	}
}

func (c *desktopControl) renewLocked() {
	c.expires = time.Now().Add(desktopLease)
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(desktopLease, func() { c.mu.Lock(); defer c.mu.Unlock(); c.expireLocked() })
}

func (d *Daemon) desktopStateLocked() rpc.DesktopState {
	c := &d.desktop
	st := rpc.DesktopState{InstanceID: d.instanceID, Epoch: c.epoch, Owner: c.owner, Label: c.label}
	if !c.expires.IsZero() {
		st.ExpiresAt = c.expires.UnixMilli()
	}
	return st
}

func (d *Daemon) desktopStatus(context.Context, json.RawMessage) (any, error) {
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	return d.desktopStateLocked(), nil
}

func (d *Daemon) desktopTake(_ context.Context, raw json.RawMessage) (any, error) {
	var p rpc.DesktopRequest
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if p.ClientID == "" || len(p.ClientID) > 128 || len(p.Label) > 128 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop identity")
	}
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	if c.blocked {
		return nil, rpc.Errorf(rpc.CodeBusy, "desktop control is unavailable during handoff")
	}
	if p.InstanceID != d.instanceID || p.Epoch != c.epoch {
		return nil, rpc.Errorf(rpc.CodeConflict, "desktop control changed; refresh before taking control")
	}
	if p.Automatic && c.owner != "" && c.owner != p.ClientID {
		return d.desktopStateLocked(), nil
	}
	// 每次明确接管都换代，旧确认回调及旧连接不能沿用控制资格。
	c.revokeLocked()
	c.owner, c.label = p.ClientID, p.Label
	c.renewLocked()
	return d.desktopStateLocked(), nil
}

func (d *Daemon) desktopValidLocked(id *rpc.DesktopIdentity) bool {
	c := &d.desktop
	return id != nil && id.ClientID != "" && id.ClientID == c.owner && id.Epoch == c.epoch && id.InstanceID == d.instanceID
}

func (d *Daemon) desktopHeartbeat(_ context.Context, raw json.RawMessage) (any, error) {
	var id rpc.DesktopIdentity
	if err := rpc.Decode(raw, &id); err != nil {
		return nil, err
	}
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	if !d.desktopValidLocked(&id) {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
	}
	c.renewLocked()
	return d.desktopStateLocked(), nil
}

func (d *Daemon) desktopRelease(_ context.Context, raw json.RawMessage) (any, error) {
	var id rpc.DesktopIdentity
	if err := rpc.Decode(raw, &id); err != nil {
		return nil, err
	}
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	if !d.desktopValidLocked(&id) {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
	}
	c.revokeLocked()
	return d.desktopStateLocked(), nil
}

// desktopInput 把输入接受与接管放在同一个边界；无身份的旧版接入只在没有控制者时允许。
func (d *Daemon) desktopInput(id *rpc.DesktopIdentity, legacyEpoch uint64, fn func()) bool {
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	valid := d.desktopValidLocked(id)
	if id == nil {
		valid = c.owner == "" && c.epoch == legacyEpoch
	}
	if !valid {
		return false
	}
	fn()
	return true
}

// beginDesktopRestore 只登记接受，恢复期间允许另一端接管，但升级须等恢复结束。
func (d *Daemon) beginDesktopRestore(id *rpc.DesktopIdentity) (func(), error) {
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	if id == nil && c.owner != "" || id != nil && !d.desktopValidLocked(id) {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "take desktop control before restoring a terminal")
	}
	if c.blocked {
		return nil, rpc.Errorf(rpc.CodeBusy, "workspace writes are blocked during handoff")
	}
	c.active++
	return func() { c.mu.Lock(); c.active--; c.mu.Unlock() }, nil
}

func (c *desktopControl) blockHandoff() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != 0 || c.blocked {
		return rpc.Errorf(rpc.CodeBusy, "workspace operations are still running; retry after they finish")
	}
	c.blocked = true
	return nil
}

func (c *desktopControl) unblock() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.blocked = false
	}
}

func (c *desktopControl) shutdown() {
	c.mu.Lock()
	c.blocked, c.closed = true, true
	c.revokeLocked()
	var pending []<-chan struct{}
	for op := range c.workspaceOperations {
		op.cancel()
		pending = append(pending, op.done)
	}
	for _, op := range c.operations {
		if op.value.FinishedAt == 0 {
			op.cancel()
			pending = append(pending, op.done)
		}
	}
	c.mu.Unlock()
	// 存储与终端在调用方随后关闭，必须先收拢已接受的工作区修改。
	for _, done := range pending {
		<-done
	}
}

func (d *Daemon) registerDesktop(srv *rpc.Server) {
	srv.Handle("desktop.status", d.desktopStatus)
	srv.Handle("desktop.take", d.desktopTake)
	srv.Handle("desktop.heartbeat", d.desktopHeartbeat)
	srv.Handle("desktop.release", d.desktopRelease)
	srv.Handle("desktop.call", func(ctx context.Context, raw json.RawMessage) (any, error) {
		return d.desktopCall(srv, ctx, raw)
	})
	srv.Handle("desktop.operation", d.desktopOperationStatus)
	srv.Handle("desktop.operation.cancel", d.desktopOperationCancel)
	srv.Handle("desktop.operations", d.desktopOperations)
}
