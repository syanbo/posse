package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"posse/internal/rpc"
)

const (
	desktopResultLimit    = 256 << 10
	desktopCacheLimit     = 4 << 20
	desktopOperationLimit = 128
	desktopResultTTL      = 10 * time.Minute
	desktopSequenceWindow = 1024
)

type desktopCallContext struct{}
type desktopRequestContext struct {
	identity rpc.DesktopIdentity
	readOnly bool
}

type desktopOperation struct {
	value          rpc.DesktopOperation
	digest         [32]byte
	done           chan struct{}
	cancel         context.CancelFunc
	bytes          int
	saveInputBytes int
}

// 只记当前控制代次的滑动窗口，回收结果后也不能把旧序号当作新操作执行。
type desktopSequences struct {
	high uint64
	bits [desktopSequenceWindow / 64]uint64
}

func (s *desktopSequences) seen(n uint64) bool {
	if n > s.high {
		return false
	}
	return s.high-n >= desktopSequenceWindow || s.bits[n%desktopSequenceWindow/64]&(uint64(1)<<(n%64)) != 0
}

func (s *desktopSequences) add(n uint64) {
	if n > s.high {
		delta := n - s.high
		if delta >= desktopSequenceWindow {
			s.bits = [desktopSequenceWindow / 64]uint64{}
		} else {
			for i := uint64(1); i <= delta; i++ {
				v := s.high + i
				s.bits[v%desktopSequenceWindow/64] &^= uint64(1) << (v % 64)
			}
		}
		s.high = n
	}
	s.bits[n%desktopSequenceWindow/64] |= uint64(1) << (n % 64)
}

func (d *Daemon) desktopCall(srv *rpc.Server, ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.DesktopCall
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Desktop.ClientID == "" || len(p.Desktop.ClientID) > 128 || p.Desktop.InstanceID != d.instanceID {
		return nil, rpc.Errorf(rpc.CodeConflict, "daemon instance changed; reconnect before making desktop requests")
	}
	write, allowed := rpc.DesktopMethod(p.Method)
	h, exists := srv.Handler(p.Method)
	if !allowed || !exists {
		return nil, rpc.Errorf(rpc.CodeUnknown, "method %q is not available through the desktop connection", p.Method)
	}
	if !write {
		ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{identity: p.Desktop, readOnly: true})
		return h(ctx, p.Params)
	}
	if p.Sequence == 0 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "desktop write sequence required")
	}
	if p.Method == "git.fetch" && len(p.Params) > rpc.GitFetchParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git fetch parameters")
	}
	if p.Method == "git.push" && len(p.Params) > rpc.GitPushParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git push parameters")
	}
	if p.Method == "git.update" && len(p.Params) > rpc.GitUpdateParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git update parameters")
	}
	if p.Method == "git.branch" && len(p.Params) > rpc.GitBranchParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git branch parameters")
	}
	if p.Method == "git.paths" && len(p.Params) > rpc.GitPathsParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git paths parameters")
	}
	if p.Method == "git.commit" && len(p.Params) > rpc.GitCommitParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git commit parameters")
	}
	if p.Method == "git.history" && len(p.Params) > rpc.GitHistoryParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git history parameters")
	}
	if p.Method == "git.stash" && len(p.Params) > rpc.GitStashParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git stash parameters")
	}
	if p.Method == "git.integrate" && len(p.Params) > rpc.GitIntegrateParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git integration parameters")
	}
	inputLimit := rpc.FileSaveRequestLimit
	if p.Method == "git.history" {
		inputLimit = rpc.GitHistoryParamsLimit
	}
	if p.Method == "git.commit" {
		inputLimit = rpc.GitCommitParamsLimit
	}
	inputBytes := 0
	if p.Method == "files.save" || p.Method == "files.pasteImage" || p.Method == "files.pasteFile" || p.Method == "git.paths" || p.Method == "git.commit" || p.Method == "git.history" {
		inputBytes = len(p.Params)
		if inputBytes > inputLimit {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "file write request exceeds the maximum size")
		}
	}
	// 只去掉空白，保留字段顺序：结构体解码时，后出现的大小写别名会覆盖前值。
	canonical, err := json.Marshal(struct {
		Method string
		Params json.RawMessage
	}{p.Method, p.Params})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	ref := rpc.DesktopOperationRef{Desktop: p.Desktop, Sequence: p.Sequence}
	c := &d.desktop
	c.mu.Lock()
	c.expireLocked()
	c.pruneOperationsLocked()
	if op := c.operations[ref]; op != nil {
		c.mu.Unlock()
		if op.digest != digest {
			return nil, rpc.Errorf(rpc.CodeConflict, "desktop request sequence was already used with different parameters")
		}
		return waitDesktopOperation(ctx, op)
	}
	if !d.desktopValidLocked(&p.Desktop) {
		c.mu.Unlock()
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
	}
	if c.blocked {
		c.mu.Unlock()
		return nil, rpc.Errorf(rpc.CodeBusy, "workspace writes are blocked during handoff")
	}
	if c.sequences.seen(p.Sequence) {
		c.mu.Unlock()
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation result is no longer available; inspect the current state before trying again")
	}
	// 文件保存和图片粘贴共用 JSON 预算，最多容纳一份最大文件；不能只按小回复计算排队开销。
	// 在解码正文及启动执行前预留，控制转移、断线不会提前释放这份占用。
	if inputBytes > inputLimit-c.saveInputBytes {
		c.mu.Unlock()
		return nil, rpc.Errorf(rpc.CodeBusy, "file writes are using the request budget; retry after they finish")
	}
	if !c.reserveOperationLocked() {
		c.mu.Unlock()
		return nil, rpc.Errorf(rpc.CodeBusy, "too many desktop operations are running; retry after they finish")
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	opCtx = context.WithValue(opCtx, desktopCallContext{}, desktopRequestContext{identity: p.Desktop})
	op := &desktopOperation{
		value:  rpc.DesktopOperation{DesktopOperationRef: ref, Method: p.Method, State: "running", StartedAt: time.Now().UnixMilli()},
		digest: digest, done: make(chan struct{}), cancel: cancel, bytes: desktopResultLimit, saveInputBytes: inputBytes,
	}
	if c.operations == nil {
		c.operations = make(map[rpc.DesktopOperationRef]*desktopOperation)
	}
	c.operations[ref] = op
	c.operationBytes += op.bytes
	c.saveInputBytes += op.saveInputBytes
	c.sequences.add(p.Sequence)
	c.active++
	go d.runDesktopOperation(opCtx, op, h, p.Params)
	c.mu.Unlock()
	return waitDesktopOperation(ctx, op)
}

func waitDesktopOperation(ctx context.Context, op *desktopOperation) (any, error) {
	select {
	case <-op.done:
		if op.value.Error != nil {
			return nil, op.value.Error
		}
		return op.value.Result, nil
	case <-ctx.Done():
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
		err.Data, _ = json.Marshal(op.value.DesktopOperationRef)
		return nil, err
	}
}

func (d *Daemon) runDesktopOperation(ctx context.Context, op *desktopOperation, h rpc.Handler, params json.RawMessage) {
	defer op.cancel()
	result, err := h(ctx, params)
	var encoded json.RawMessage
	var failure *rpc.Error
	if err == nil {
		encoded, err = json.Marshal(result)
	}
	if err != nil {
		if !errors.As(err, &failure) {
			failure = rpc.Errorf(rpc.CodeInternal, "%s", err)
		}
	}
	errorBytes, _ := json.Marshal(failure)
	if len(encoded)+len(errorBytes) > desktopResultLimit {
		encoded = nil
		failure = rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation result is too large to retain; inspect the current state")
		errorBytes, _ = json.Marshal(failure)
	}
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveInputBytes -= op.saveInputBytes
	op.saveInputBytes = 0
	c.operationBytes -= op.bytes
	op.bytes = len(encoded) + len(errorBytes)
	c.operationBytes += op.bytes
	op.value.Result, op.value.Error = encoded, failure
	op.value.State, op.value.FinishedAt = "done", time.Now().UnixMilli()
	c.active--
	close(op.done)
}

func (c *desktopControl) removeOperationLocked(ref rpc.DesktopOperationRef, op *desktopOperation) {
	delete(c.operations, ref)
	c.operationBytes -= op.bytes
}

func (c *desktopControl) pruneOperationsLocked() {
	before := time.Now().Add(-desktopResultTTL).UnixMilli()
	for ref, op := range c.operations {
		if op.value.FinishedAt != 0 && op.value.FinishedAt <= before {
			c.removeOperationLocked(ref, op)
		}
	}
}

func (c *desktopControl) reserveOperationLocked() bool {
	for len(c.operations) >= desktopOperationLimit || c.operationBytes+desktopResultLimit > desktopCacheLimit {
		var oldest *desktopOperation
		for _, op := range c.operations {
			if op.value.FinishedAt != 0 && (oldest == nil || op.value.FinishedAt < oldest.value.FinishedAt) {
				oldest = op
			}
		}
		if oldest == nil {
			return false
		}
		c.removeOperationLocked(oldest.value.DesktopOperationRef, oldest)
	}
	return true
}

func (d *Daemon) desktopOperationStatus(_ context.Context, raw json.RawMessage) (any, error) {
	var p rpc.DesktopOperationRef
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneOperationsLocked()
	if p.Desktop.InstanceID == d.instanceID {
		if op := c.operations[p]; op != nil {
			return op.value, nil
		}
	}
	return rpc.DesktopOperation{DesktopOperationRef: p, State: "unknown"}, nil
}

func (d *Daemon) desktopOperations(context.Context, json.RawMessage) (any, error) {
	c := &d.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneOperationsLocked()
	result := make([]rpc.DesktopOperation, 0, len(c.operations))
	for _, op := range c.operations {
		v := op.value
		v.Result, v.Error = nil, nil
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt < result[j].StartedAt })
	return result, nil
}

func isDesktopRequest(ctx context.Context) bool {
	_, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	return ok
}

func (d *Daemon) sessionForRequest(ctx context.Context, id string) (*session, error) {
	if p, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext); ok && p.readOnly {
		if s := d.liveSession(id); s != nil {
			return s, nil
		}
		return nil, rpc.Errorf(rpc.CodeReadOnly, "terminal is not running; open a controlled terminal view to restore it")
	}
	return d.session(id)
}
