package writecoord

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type observedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	// Acquire 在入队并完成调度后才求值 Done；用事件确认请求已参与协调。
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

type acquireResult struct {
	release func()
	err     error
}

type attempt struct {
	ctx    *observedContext
	cancel context.CancelFunc
	result chan acquireResult
}

func startAttempt(t *testing.T, c *Coordinator, scope Scope) *attempt {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	a := &attempt{
		ctx:    &observedContext{Context: ctx, observed: make(chan struct{})},
		cancel: cancel,
		result: make(chan acquireResult, 1),
	}
	t.Cleanup(cancel)
	go func() {
		release, err := c.Acquire(a.ctx, scope)
		a.result <- acquireResult{release, err}
	}()
	<-a.ctx.observed
	return a
}

func (a *attempt) acquired(t *testing.T) func() {
	t.Helper()
	r := <-a.result
	if r.err != nil || r.release == nil {
		t.Fatalf("Acquire = (%v, %v), want a release function", r.release != nil, r.err)
	}
	t.Cleanup(r.release)
	return r.release
}

func (a *attempt) canceled(t *testing.T) {
	t.Helper()
	r := <-a.result
	if !errors.Is(r.err, context.Canceled) || r.release != nil {
		t.Fatalf("Acquire = (%v, %v), want (nil, context.Canceled)", r.release != nil, r.err)
	}
}

func assertQueued(t *testing.T, c *Coordinator, a *attempt) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.waiting.Front(); e != nil; e = e.Next() {
		if e.Value.(*request).ctx == a.ctx {
			return
		}
	}
	t.Fatal("request did not remain queued")
}

func assertIdle(t *testing.T, c *Coordinator) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active.Len() != 0 || c.waiting.Len() != 0 {
		t.Fatalf("coordinator retains %d active and %d waiting requests", c.active.Len(), c.waiting.Len())
	}
}

func hold(t *testing.T, c *Coordinator, scope Scope) func() {
	t.Helper()
	release, err := c.Acquire(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return release
}

func TestConflictingScopes(t *testing.T) {
	tests := []struct {
		name   string
		first  Scope
		second Scope
	}{
		{"shared link first", Scope{SharedPaths: []string{"/links/project"}}, Scope{Paths: []string{"/links"}}},
		{"shared link second", Scope{Paths: []string{"/links"}}, Scope{SharedPaths: []string{"/links/project"}}},
		{"same shared link", Scope{Paths: []string{"/links/project"}}, Scope{SharedPaths: []string{"/links/project"}}},
		{"clean shared link", Scope{SharedPaths: []string{"/links/sub/../project/"}}, Scope{Paths: []string{"/links/project"}}},
		{"same path", Scope{Paths: []string{"/repo/file"}}, Scope{Paths: []string{"/repo/file"}}},
		{"parent first", Scope{Paths: []string{"/repo"}}, Scope{Paths: []string{"/repo/sub/file"}}},
		{"child first", Scope{Paths: []string{"/repo/sub/file"}}, Scope{Paths: []string{"/repo"}}},
		{"root", Scope{Paths: []string{"/"}}, Scope{Paths: []string{"/repo"}}},
		{"clean path", Scope{Paths: []string{"/repo/other/../file/"}}, Scope{Paths: []string{"/repo/file"}}},
		{"same key", Scope{Keys: []string{"git:/common"}}, Scope{Keys: []string{"git:/common"}}},
		{"empty key", Scope{Keys: []string{""}}, Scope{Keys: []string{""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Coordinator
			release := hold(t, &c, tt.first)
			a := startAttempt(t, &c, tt.second)
			assertQueued(t, &c, a)
			release()
			a.acquired(t)()
			assertIdle(t, &c)
		})
	}
}

func TestIndependentScopes(t *testing.T) {
	tests := []struct {
		name   string
		first  Scope
		second Scope
	}{
		{"path boundary", Scope{Paths: []string{"/repo"}}, Scope{Paths: []string{"/repository"}}},
		{"siblings", Scope{Paths: []string{"/repo/a"}}, Scope{Paths: []string{"/repo/b"}}},
		{"shared link siblings", Scope{Paths: []string{"/repo/a"}, SharedPaths: []string{"/links/project"}}, Scope{Paths: []string{"/repo/b"}, SharedPaths: []string{"/links/project"}}},
		{"shared link boundary", Scope{SharedPaths: []string{"/links/project"}}, Scope{Paths: []string{"/links/projects"}}},
		{"key prefix", Scope{Keys: []string{"project:1"}}, Scope{Keys: []string{"project:10"}}},
		{"separate namespaces", Scope{Paths: []string{"/repo"}}, Scope{Keys: []string{"/repo"}}},
		{"empty scope", Scope{Paths: []string{"/"}}, Scope{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Coordinator
			release := hold(t, &c, tt.first)
			startAttempt(t, &c, tt.second).acquired(t)()
			release()
			assertIdle(t, &c)
		})
	}
}

func TestMultipleResourcesReverseOrder(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{Paths: []string{"/a"}})
	first := startAttempt(t, &c, Scope{Paths: []string{"/a", "/b"}, Keys: []string{"one", "two"}})
	second := startAttempt(t, &c, Scope{Paths: []string{"/b", "/a"}, Keys: []string{"two", "one"}})
	assertQueued(t, &c, first)
	assertQueued(t, &c, second)
	c.mu.Lock()
	active := c.active.Len()
	c.mu.Unlock()
	if active != 1 {
		t.Fatalf("%d active requests: queued requests must not partially acquire resources", active)
	}
	release()
	releaseFirst := first.acquired(t)
	assertQueued(t, &c, second)
	releaseFirst()
	second.acquired(t)()
	assertIdle(t, &c)
}

func TestConflictQueueFairnessAndIndependentProgress(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{Paths: []string{"/a"}})
	first := startAttempt(t, &c, Scope{Paths: []string{"/a", "/b"}})
	second := startAttempt(t, &c, Scope{Paths: []string{"/b", "/c"}})
	third := startAttempt(t, &c, Scope{Paths: []string{"/c"}})
	assertQueued(t, &c, first)
	assertQueued(t, &c, second)
	assertQueued(t, &c, third)
	startAttempt(t, &c, Scope{Paths: []string{"/independent"}}).acquired(t)()
	release()
	releaseFirst := first.acquired(t)
	assertQueued(t, &c, second)
	assertQueued(t, &c, third)
	releaseFirst()
	releaseSecond := second.acquired(t)
	assertQueued(t, &c, third)
	releaseSecond()
	third.acquired(t)()
	assertIdle(t, &c)
}

func TestCancelQueuedRequestUnblocksSuccessor(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{Keys: []string{"one"}})
	first := startAttempt(t, &c, Scope{Keys: []string{"one", "two"}})
	second := startAttempt(t, &c, Scope{Keys: []string{"two"}})
	assertQueued(t, &c, first)
	assertQueued(t, &c, second)
	first.cancel()
	first.canceled(t)
	second.acquired(t)()
	release()
	assertIdle(t, &c)
}

func TestCanceledBeforeAcquire(t *testing.T) {
	var c Coordinator
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	release, err := c.Acquire(ctx, Scope{Paths: []string{"/repo"}})
	if !errors.Is(err, context.Canceled) || release != nil {
		t.Fatalf("Acquire = (%v, %v), want (nil, context.Canceled)", release != nil, err)
	}
	assertIdle(t, &c)
}

func TestCancelAfterAcquireKeepsOwnership(t *testing.T) {
	var c Coordinator
	first := startAttempt(t, &c, Scope{Keys: []string{"one"}})
	release := first.acquired(t)
	first.cancel()
	second := startAttempt(t, &c, Scope{Keys: []string{"one"}})
	assertQueued(t, &c, second)
	release()
	second.acquired(t)()
	assertIdle(t, &c)
}

func TestConcurrentRepeatedRelease(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{Paths: []string{"/repo"}})
	second := startAttempt(t, &c, Scope{Paths: []string{"/repo"}})
	assertQueued(t, &c, second)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(release)
	}
	wg.Wait()
	releaseSecond := second.acquired(t)
	release()
	third := startAttempt(t, &c, Scope{Paths: []string{"/repo"}})
	assertQueued(t, &c, third)
	releaseSecond()
	third.acquired(t)()
	assertIdle(t, &c)
}

func TestCancelRacesWithGrant(t *testing.T) {
	for range 32 {
		var c Coordinator
		release := hold(t, &c, Scope{Keys: []string{"one"}})
		a := startAttempt(t, &c, Scope{Keys: []string{"one"}})
		assertQueued(t, &c, a)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() { <-start; release() })
		wg.Go(func() { <-start; a.cancel() })
		close(start)
		wg.Wait()
		r := <-a.result
		if r.err == nil && r.release != nil {
			r.release()
		} else if !errors.Is(r.err, context.Canceled) || r.release != nil {
			t.Fatalf("Acquire = (%v, %v), want success or cancellation", r.release != nil, r.err)
		}
		assertIdle(t, &c)
	}
}

func TestInvalidPaths(t *testing.T) {
	for _, path := range []string{"", "relative/path", "/repo/\x00file"} {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			var c Coordinator
			for _, scope := range []Scope{{Paths: []string{"/valid", path}}, {SharedPaths: []string{"/valid", path}}} {
				release, err := c.Acquire(t.Context(), scope)
				if err == nil || release != nil {
					t.Fatalf("Acquire = (%v, %v), want (nil, error)", release != nil, err)
				}
				assertIdle(t, &c)
			}
		})
	}
}

func TestScopeCopied(t *testing.T) {
	var c Coordinator
	scope := Scope{Paths: []string{"/repo", "/repo"}, SharedPaths: []string{"/link"}, Keys: []string{"one", "one"}, SharedKeys: []string{"project"}}
	release := hold(t, &c, scope)
	scope.Paths[0], scope.Paths[1] = "/other", "/other"
	scope.Keys[0], scope.Keys[1] = "other", "other"
	scope.SharedKeys[0] = "other"
	scope.SharedPaths[0] = "/other"
	sharedPath := startAttempt(t, &c, Scope{Paths: []string{"/link"}})
	assertQueued(t, &c, sharedPath)
	path := startAttempt(t, &c, Scope{Paths: []string{"/repo"}})
	key := startAttempt(t, &c, Scope{Keys: []string{"one"}})
	sharedKey := startAttempt(t, &c, Scope{Keys: []string{"project"}})
	assertQueued(t, &c, path)
	assertQueued(t, &c, key)
	assertQueued(t, &c, sharedKey)
	release()
	path.acquired(t)()
	key.acquired(t)()
	sharedKey.acquired(t)()
	sharedPath.acquired(t)()
	assertIdle(t, &c)
}

func TestSharedProjectWithExclusiveRepositories(t *testing.T) {
	var c Coordinator
	firstScope := Scope{Paths: []string{"/project/first"}, Keys: []string{"git:first"}, SharedKeys: []string{"project"}}
	secondScope := Scope{Paths: []string{"/project/second"}, Keys: []string{"git:second"}, SharedKeys: []string{"project"}}
	releaseFirst := startAttempt(t, &c, firstScope).acquired(t)
	releaseSecond := startAttempt(t, &c, secondScope).acquired(t)
	project := startAttempt(t, &c, Scope{Keys: []string{"project"}})
	assertQueued(t, &c, project)
	releaseFirst()
	assertQueued(t, &c, project)
	releaseSecond()
	releaseProject := project.acquired(t)
	first := startAttempt(t, &c, firstScope)
	second := startAttempt(t, &c, secondScope)
	assertQueued(t, &c, first)
	assertQueued(t, &c, second)
	releaseProject()
	releaseFirst = first.acquired(t)
	releaseSecond = second.acquired(t)
	samePath := startAttempt(t, &c, Scope{Paths: []string{"/project/first"}, SharedKeys: []string{"project"}})
	sameGit := startAttempt(t, &c, Scope{Keys: []string{"git:second"}, SharedKeys: []string{"project"}})
	assertQueued(t, &c, samePath)
	assertQueued(t, &c, sameGit)
	releaseFirst()
	releaseSecond()
	samePath.acquired(t)()
	sameGit.acquired(t)()
	assertIdle(t, &c)
}

func TestQueuedExclusiveKeyPrecedesLaterSharedKeys(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{SharedKeys: []string{"project"}})
	writer := startAttempt(t, &c, Scope{Keys: []string{"project"}})
	readers := make([]*attempt, 4)
	for i := range readers {
		readers[i] = startAttempt(t, &c, Scope{SharedKeys: []string{"project"}})
		assertQueued(t, &c, readers[i])
	}
	assertQueued(t, &c, writer)
	startAttempt(t, &c, Scope{SharedKeys: []string{"other"}}).acquired(t)()
	release()
	releaseWriter := writer.acquired(t)
	for _, reader := range readers {
		assertQueued(t, &c, reader)
	}
	releaseWriter()
	releases := make([]func(), len(readers))
	for i, reader := range readers {
		releases[i] = reader.acquired(t)
	}
	for _, release := range releases {
		release()
	}
	assertIdle(t, &c)
}

func TestCancelQueuedExclusiveKeyUnblocksSharedKeys(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{SharedKeys: []string{"project"}})
	writer := startAttempt(t, &c, Scope{Keys: []string{"project"}})
	reader := startAttempt(t, &c, Scope{SharedKeys: []string{"project"}})
	assertQueued(t, &c, writer)
	assertQueued(t, &c, reader)
	writer.cancel()
	writer.canceled(t)
	reader.acquired(t)()
	release()
	assertIdle(t, &c)
}

func TestCancelQueuedSharedKeysUnblocksExclusiveKey(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{Keys: []string{"one"}})
	reader := startAttempt(t, &c, Scope{Paths: []string{"/repo"}, Keys: []string{"git:repo"}, SharedKeys: []string{"one", "two"}})
	writer := startAttempt(t, &c, Scope{Keys: []string{"two"}})
	assertQueued(t, &c, reader)
	assertQueued(t, &c, writer)
	c.mu.Lock()
	active := c.active.Len()
	c.mu.Unlock()
	if active != 1 {
		t.Fatalf("%d active requests: mixed scopes must be acquired atomically", active)
	}
	reader.cancel()
	reader.canceled(t)
	writer.acquired(t)()
	startAttempt(t, &c, Scope{Paths: []string{"/repo"}, Keys: []string{"git:repo"}}).acquired(t)()
	release()
	assertIdle(t, &c)
}
