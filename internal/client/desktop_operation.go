package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"posse/internal/rpc"
)

// DesktopOperation 固定目标连接与请求；复制句柄也共享一次发送限制。
type DesktopOperation struct{ state *preparedDesktopOperation }

type preparedDesktopOperation struct {
	socket string
	ref    rpc.DesktopOperationRef
	method string
	params json.RawMessage
	used   atomic.Bool
}

func (c *Client) PrepareDesktopOperation(method string, params any) (*DesktopOperation, error) {
	if c.Desktop == nil {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for desktop operations")
	}
	if write, allowed := rpc.DesktopMethod(method); !write || !allowed {
		return nil, rpc.Errorf(rpc.CodeUnknown, "method %q is not a desktop write operation", method)
	}
	id := *c.Desktop
	if !rpc.ValidDesktopOperationRef(rpc.DesktopOperationRef{Desktop: id, Sequence: 1}) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop identity")
	}
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ref := rpc.DesktopOperationRef{Desktop: id, Sequence: desktopSequence.Add(1)}
	return &DesktopOperation{state: &preparedDesktopOperation{socket: c.Socket, ref: ref, method: method, params: payload}}, nil
}

func (op *DesktopOperation) Ref() rpc.DesktopOperationRef {
	if op == nil || op.state == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.state.ref
}

func (op *DesktopOperation) Execute(ctx context.Context, out any) error {
	if op == nil || op.state == nil {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	s := op.state
	if !s.used.CompareAndSwap(false, true) {
		return rpc.Errorf(rpc.CodeConflict, "desktop operation request was already used; inspect its status before trying again")
	}
	p := rpc.DesktopCall{Desktop: s.ref.Desktop, Sequence: s.ref.Sequence, Method: s.method, Params: s.params}
	s.params = nil
	// 执行前取消明确不发送；句柄仍消耗掉，调用方不能在异步回调中意外重发。
	if err := ctx.Err(); err != nil {
		return err
	}
	return callDesktop(ctx, s.socket, p, true, out)
}

func callDesktop(ctx context.Context, socket string, p rpc.DesktopCall, write bool, out any) error {
	err := rpc.Call(ctx, socket, "desktop.call", p, out)
	var re *rpc.Error
	if err != nil && write {
		// 不把断线变成可重试错误；保留来源身份供重连后查询，绝不自动重发正文。
		if !errors.As(err, &re) {
			re = rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
		}
		if re.Code == rpc.CodeOutcomeUnknown {
			re.Data, _ = json.Marshal(rpc.DesktopOperationRef{Desktop: p.Desktop, Sequence: p.Sequence})
		}
		return re
	}
	return err
}

func (c *Client) DesktopOperationStatus(ctx context.Context, ref rpc.DesktopOperationRef) (rpc.DesktopOperation, error) {
	return c.desktopOperationRequest(ctx, "desktop.operation", ref, ref)
}

func (c *Client) CancelDesktopOperation(ctx context.Context, ref rpc.DesktopOperationRef) (rpc.DesktopOperation, error) {
	if c.Desktop == nil {
		return rpc.DesktopOperation{}, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for desktop operations")
	}
	return c.desktopOperationRequest(ctx, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: *c.Desktop, Operation: ref}, ref)
}

func (c *Client) desktopOperationRequest(ctx context.Context, method string, params any, ref rpc.DesktopOperationRef) (rpc.DesktopOperation, error) {
	if !rpc.ValidDesktopOperationRef(ref) {
		return rpc.DesktopOperation{}, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	var out rpc.DesktopOperation
	if err := rpc.Call(ctx, c.Socket, method, params, &out); err != nil {
		return rpc.DesktopOperation{}, err
	}
	if !validDesktopOperation(out, ref) {
		return rpc.DesktopOperation{}, rpc.Errorf(rpc.CodeInternal, "invalid desktop operation response")
	}
	return out, nil
}

func validDesktopOperation(out rpc.DesktopOperation, ref rpc.DesktopOperationRef) bool {
	if out.DesktopOperationRef != ref {
		return false
	}
	if out.State == "unknown" {
		return out.Method == "" && out.StartedAt == 0 && out.FinishedAt == 0 && len(out.Result) == 0 && out.Error == nil
	}
	write, allowed := rpc.DesktopMethod(out.Method)
	if !write || !allowed || out.StartedAt <= 0 {
		return false
	}
	switch out.State {
	case "running":
		return out.FinishedAt == 0 && len(out.Result) == 0 && out.Error == nil
	case "done":
		if out.FinishedAt <= 0 {
			return false
		}
		if out.Error != nil {
			return out.Error.Code != "" && len(out.Result) == 0
		}
		return len(out.Result) != 0 && json.Valid(out.Result)
	}
	return false
}
