package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"posse/internal/files"
)

// fakeLister 替换 listRepos：每次调用排进 calls，由测试逐个放行，交回的清单带着第几次调用。
type fakeLister struct {
	mu    sync.Mutex
	calls []chan error
}

func (f *fakeLister) install(t *testing.T) {
	listRepos = func(ctx context.Context, root string, repos []string) (*files.List, error) {
		ch := make(chan error)
		f.mu.Lock()
		n := len(f.calls)
		f.calls = append(f.calls, ch)
		f.mu.Unlock()
		if err := <-ch; err != nil {
			return nil, err
		}
		return &files.List{Root: fmt.Sprint("call-", n)}, nil
	}
	t.Cleanup(func() { listRepos = files.ListRepos })
}

func (f *fakeLister) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// started 等到第 n 次调用已经开始（起的协程到这里有先后），再看一会儿没有多出来的。
func (f *fakeLister) started(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d listings started, want %d", f.count(), n)
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if f.count() != n {
		t.Fatalf("%d listings started, want %d", f.count(), n)
	}
}

// finish 放行第 i 次调用，等界面线程处理完它交回的结果。
func (f *fakeLister) finish(t *testing.T, a *app, i int, err error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.count() <= i {
		if time.Now().After(deadline) {
			t.Fatalf("call %d never started", i)
		}
		time.Sleep(time.Millisecond)
	}
	f.mu.Lock()
	ch := f.calls[i]
	f.mu.Unlock()
	ch <- err
	for range 200 {
		time.Sleep(2 * time.Millisecond)
		a.runPending()
	}
}

func listName(v *filesView) string {
	if v.list == nil {
		return ""
	}
	return v.list.Root
}

// 正在列文件时：带回调的请求（搜索、跳到定义）和后台的请求都不另起一遍，回调排到这一遍后面一起交回；
// 清单新鲜时直接回调。
func TestRefreshListJoinsRunning(t *testing.T) {
	var f fakeLister
	f.install(t)
	a := &app{}
	v := &filesView{root: t.TempDir()}
	var got []string
	cb := func(tag string) func() { return func() { got = append(got, tag+"="+listName(v)) } }
	a.refreshList(v, false, cb("a"))
	a.refreshList(v, false, cb("b"))
	a.refreshList(v, false, nil)
	a.refreshList(v, false, cb("c"))
	f.started(t, 1)
	f.finish(t, a, 0, nil)
	if f.count() != 1 || fmt.Sprint(got) != "[a=call-0 b=call-0 c=call-0]" || v.listing {
		t.Fatalf("calls %d, callbacks %v, listing %v", f.count(), got, v.listing)
	}
	got = nil
	a.refreshList(v, false, cb("fresh"))
	if f.count() != 1 || fmt.Sprint(got) != "[fresh=call-0]" {
		t.Fatalf("fresh list: calls %d, callbacks %v", f.count(), got)
	}
}

// 正在列时 force 带回调：另起一遍，之前那遍晚到也不用（不拿旧清单盖掉新的），排着的回调都拿新清单。
func TestRefreshListForceSupersedes(t *testing.T) {
	var f fakeLister
	f.install(t)
	a := &app{}
	v := &filesView{root: t.TempDir()}
	var got []string
	a.refreshList(v, false, func() { got = append(got, "a="+listName(v)) })
	f.started(t, 1)
	a.refreshList(v, true, func() { got = append(got, "b="+listName(v)) })
	f.started(t, 2)
	f.finish(t, a, 1, nil)
	if fmt.Sprint(got) != "[a=call-1 b=call-1]" || v.listing {
		t.Fatalf("after the forced listing: %v listing %v", got, v.listing)
	}
	f.finish(t, a, 0, nil)
	if listName(v) != "call-1" || len(got) != 2 {
		t.Fatalf("late older listing applied: list %s callbacks %v", listName(v), got)
	}
}

// 列的过程中清单失效（删了文件）：这一遍可能是删之前开始的，作废、马上重起；等着的回调拿重起那遍的清单。
func TestRefreshListInvalidatedWhileRunning(t *testing.T) {
	var f fakeLister
	f.install(t)
	a := &app{}
	v := &filesView{root: t.TempDir()}
	var got []string
	a.refreshList(v, false, func() { got = append(got, "a="+listName(v)) })
	f.started(t, 1)
	a.invalidateList(v)
	f.started(t, 2)
	f.finish(t, a, 0, nil)
	if len(got) != 0 || !v.listing || f.count() != 2 {
		t.Fatalf("stale listing delivered: %v listing %v calls %d", got, v.listing, f.count())
	}
	f.finish(t, a, 1, nil)
	if fmt.Sprint(got) != "[a=call-1]" {
		t.Fatalf("callbacks %v", got)
	}
	// 不在列时失效：只让下次重列，不自己起
	a.invalidateList(v)
	f.started(t, 2)
	if v.listing {
		t.Fatal("idle invalidate started a listing")
	}
	a.refreshList(v, false, func() { got = append(got, "b="+listName(v)) })
	f.started(t, 3)
	f.finish(t, a, 2, nil)
	if fmt.Sprint(got) != "[a=call-1 b=call-2]" {
		t.Fatalf("callbacks %v", got)
	}
}

// 列失败：排着的回调都交回（拿上一份清单），报错留给下一帧；后台的请求等一个周期再试，带回调的照常重试。
func TestRefreshListFailureDeliversWaiters(t *testing.T) {
	var f fakeLister
	f.install(t)
	a := &app{}
	v := &filesView{root: t.TempDir()}
	a.refreshList(v, false, nil)
	f.started(t, 1)
	f.finish(t, a, 0, nil)
	v.listAt = time.Now().Add(-2 * listMaxAge)
	var got []string
	a.refreshList(v, false, func() { got = append(got, "a="+listName(v)) })
	a.refreshList(v, false, func() { got = append(got, "b="+listName(v)) })
	f.started(t, 2)
	f.finish(t, a, 1, errors.New("boom"))
	if fmt.Sprint(got) != "[a=call-0 b=call-0]" || v.listErr == nil || v.listing {
		t.Fatalf("callbacks %v err %v listing %v", got, v.listErr, v.listing)
	}
	a.refreshList(v, false, nil)
	f.started(t, 2) // 后台的不重试
	a.refreshList(v, false, func() {})
	f.started(t, 3) // 带回调的重试
	f.finish(t, a, 2, nil)
}
