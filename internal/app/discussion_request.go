package app

import (
	"context"
	"reflect"
	"strings"
	"time"

	"posse/internal/errtext"
)

// 讨论属于窗口所在的本机，不能借用所选工作区的 Client 或控制身份。
type discussionRequest struct {
	appRequest
	gen int64
}

func (a *app) discussionRequest() discussionRequest {
	a = a.windowOwner()
	return discussionRequest{appRequest: appRequest{app: a, client: a.c, viewGeneration: a.viewGeneration.Load()}, gen: a.discGen()}
}

func (r discussionRequest) invoke(ctx context.Context, method string, params, out any) error {
	if !r.bound() || r.client == nil || r.client.Remote || !strings.HasPrefix(method, "discussion.") {
		return context.Canceled
	}
	if method == "discussion.show" && !r.current() {
		return context.Canceled
	}
	return r.client.Call(ctx, method, params, out)
}

func (r discussionRequest) update(fn func()) {
	r.appRequest.update(func() {
		if r.gen == r.app.discGen() {
			fn()
		}
	})
}

func (r discussionRequest) call(method string, params, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(r.app.windowContext(), timeout)
	defer cancel()
	err := r.invoke(ctx, method, params, out)
	if err != nil {
		r.update(func() { r.app.err = errtext.Text(err) })
	}
	return err
}

func (r discussionRequest) refresh() {
	if r.current() {
		r.app.refreshRequest(r.appRequest)
	} else {
		r.refreshDiscussions()
	}
}

func (r discussionRequest) discCall(method string, params map[string]any) {
	r.call(method, params, nil, 30*time.Second)
	r.refresh()
}

func (r discussionRequest) refreshDiscussions() {
	a := r.app
	a.discRefreshMu.Lock()
	defer a.discRefreshMu.Unlock()
	if !r.bound() {
		return
	}
	ctx, cancel := context.WithTimeout(a.windowContext(), 5*time.Second)
	defer cancel()
	p := a.pollDiscussions(func(method string, params, out any) error {
		if method != "discussion.list" && !r.current() {
			return context.Canceled
		}
		return r.invoke(ctx, method, params, out)
	})
	if !r.bound() || p == nil {
		return
	}
	if a.lastDiscRequest.viewGeneration == r.viewGeneration && a.lastDiscRequest.client == r.client && reflect.DeepEqual(a.lastDiscPoll, p) {
		return
	}
	a.lastDiscPoll, a.lastDiscRequest = p, r
	a.update(func() {
		if !r.bound() {
			return
		}
		// 隐藏时只更新本机列表；详情仍受当前页面和讨论意图约束。
		result := *p
		if !r.current() {
			result.view = nil
		}
		a.applyDiscussions(&result)
	})
}
