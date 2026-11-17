package app

import (
	"context"
	"errors"
	"time"

	"posse/internal/client"
	"posse/internal/errtext"
	"posse/internal/rpc"
)

// appRequest 在派发协程或打开确认框之前捕获；同一动作的后续请求不借用新的控制身份。
// app 和 host 的归属固定，切主机使原 host.detach，不能把旧 app 原地改绑到另一台主机。
type appRequest struct {
	app            *app
	client         *client.Client
	host           *hostSession
	snapshot       hostSnapshot
	viewGeneration uint64
}

func (a *app) request() appRequest {
	r := appRequest{app: a, client: a.c, host: a.host, viewGeneration: a.viewGeneration.Load()}
	if r.host != nil {
		r.snapshot = r.host.snapshot()
		r.client = r.snapshot.Client
	}
	return r
}

func (r appRequest) bound() bool {
	return r.app.viewGeneration.Load() == r.viewGeneration && (r.host == nil || r.host.matches(r.snapshot))
}

func (r appRequest) current() bool {
	return r.bound() && r.app.displayed() == r.app
}

func (r appRequest) mayWrite() bool {
	if !r.current() {
		return false
	}
	if r.host != nil && !r.snapshot.Controlled {
		r.update(func() { r.app.err = errtext.Text(rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")) })
		return false
	}
	return true
}

func (r appRequest) invoke(ctx context.Context, method string, params, out any) error {
	if !r.current() || r.client == nil {
		return context.Canceled
	}
	if r.host != nil {
		if write, allowed := rpc.DesktopMethod(method); allowed && write && !r.snapshot.Controlled {
			return rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
		}
	}
	return r.client.Call(ctx, method, params, out)
}

func (r appRequest) call(method string, params, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := r.invoke(ctx, method, params, out)
	if err != nil {
		r.update(func() { r.app.err = errtext.Text(err) })
	}
	return err
}

// 排进主线程之后还可能换代，必须在真正应用结果时再核对一次。
func (r appRequest) update(fn func()) {
	r.app.update(func() {
		if r.current() {
			fn()
		}
	})
}

func (r appRequest) refresh() {
	r.app.refreshRequest(r)
}

func (r appRequest) canChooseLocalDirectory() bool {
	if !r.current() || r.client == nil {
		return false
	}
	if r.client.Remote {
		err := errors.New("directory selection is not available for remote hosts")
		r.update(func() { r.app.err = errtext.Text(err) })
		return false
	}
	return true
}
