package remote

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeConnection(t *testing.T, h Host) *Session {
	t.Helper()
	dir := t.TempDir()
	done := make(chan struct{})
	var once sync.Once
	return &Session{Host: h, dir: dir, done: done, cancel: func() { once.Do(func() { close(done) }) }}
}

func connectionHost(id string) Host { return Host{ID: id, Name: id, Target: "example.invalid"} }

func testConnections(t *testing.T) *Connections {
	t.Helper()
	p := NewConnections()
	p.dial = func(_ context.Context, h Host, _ Options) (*Session, error) { return fakeConnection(t, h), nil }
	p.check = func(context.Context, *Session, Options) error { return nil }
	t.Cleanup(p.Close)
	return p
}

func openConnection(t *testing.T, p *Connections, h Host) *Session {
	t.Helper()
	s, err := p.Open(context.Background(), h, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func awaitConnectionClosed(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("connection was not closed")
	}
}

func TestConnectionsReuseAndConfig(t *testing.T) {
	p := testConnections(t)
	var checks int
	p.check = func(context.Context, *Session, Options) error { checks++; return nil }
	h := connectionHost("a")
	a := openConnection(t, p, h)
	p.Park()
	if !sessionAlive(a) {
		t.Fatal("parking stopped connection")
	}
	if openConnection(t, p, h) != a || checks != 1 {
		t.Fatal("cached connection not rechecked and reused")
	}
	b := openConnection(t, p, connectionHost("b"))
	if openConnection(t, p, h) != a {
		t.Fatal("returning to host did not reuse master")
	}
	h.Home = "/changed/home"
	next := openConnection(t, p, h)
	if next == a || next.Host != h {
		t.Fatal("configuration change reused old target")
	}
	awaitConnectionClosed(t, a)
	p.Disconnect("b")
	awaitConnectionClosed(t, b)
	if !sessionAlive(next) {
		t.Fatal("disconnect stopped a different host")
	}
	p.Close()
	awaitConnectionClosed(t, next)
	if _, err := os.Stat(next.dir); !os.IsNotExist(err) {
		t.Fatal("Close left connection directory")
	}
	if _, err := p.Open(context.Background(), h, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal("closed manager reopened")
	}
}

func TestConnectionsReuseFullCache(t *testing.T) {
	p := testConnections(t)
	var sessions []*Session
	for _, id := range []string{"a", "b", "c", "d"} {
		sessions = append(sessions, openConnection(t, p, connectionHost(id)))
	}
	for _, s := range append(sessions, sessions[len(sessions)-1]) {
		if openConnection(t, p, s.Host) != s {
			t.Fatalf("full cache evicted requested host %s", s.Host.ID)
		}
	}
	for _, s := range sessions {
		if !sessionAlive(s) {
			t.Fatalf("reused master %s was closed", s.Host.ID)
		}
	}
}

func TestConnectionsCancelLateSuccess(t *testing.T) {
	p := testConnections(t)
	entered, finish, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a := fakeConnection(t, connectionHost("a"))
	p.dial = func(ctx context.Context, h Host, _ Options) (*Session, error) {
		if h.ID != "a" {
			return fakeConnection(t, h), nil
		}
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-finish
		return a, nil
	}
	result := make(chan error, 1)
	go func() { _, err := p.Open(context.Background(), a.Host, Options{}); result <- err }()
	<-entered
	b := openConnection(t, p, connectionHost("b"))
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("switch did not cancel previous connection")
	}
	close(finish)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("late result = %v", err)
	}
	awaitConnectionClosed(t, a)
	if !sessionAlive(b) {
		t.Fatal("late completion closed current host")
	}
}

func TestConnectionsParkCancelsAuthentication(t *testing.T) {
	p := testConnections(t)
	prompting := make(chan struct{})
	p.dial = func(ctx context.Context, h Host, opts Options) (*Session, error) {
		_, err := opts.Prompt(ctx, Prompt{Text: "Password:"})
		return nil, err
	}
	result := make(chan error, 1)
	go func() {
		_, err := p.Open(context.Background(), connectionHost("a"), Options{Prompt: func(ctx context.Context, _ Prompt) (string, error) {
			close(prompting)
			<-ctx.Done()
			return "", ctx.Err()
		}})
		result <- err
	}()
	<-prompting
	p.Park()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("authentication result = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authentication did not cancel")
	}
}

func TestConnectionsIdleBoundsAndDeadMaster(t *testing.T) {
	p := testConnections(t)
	p.limit = 1
	a := openConnection(t, p, connectionHost("a"))
	b := openConnection(t, p, connectionHost("b"))
	p.Park()
	awaitConnectionClosed(t, a)
	if openConnection(t, p, b.Host) != b {
		t.Fatal("recent idle connection was evicted")
	}
	b.cancel()
	awaitConnectionClosed(t, b)
	next := openConnection(t, p, b.Host)
	if next == b {
		t.Fatal("dead master was reused")
	}
	p.ttl = 20 * time.Millisecond
	p.Park()
	awaitConnectionClosed(t, next)
}

func TestConnectionsReuseFailureDoesNotRetry(t *testing.T) {
	p := testConnections(t)
	var dials atomic.Int32
	p.dial = func(_ context.Context, h Host, _ Options) (*Session, error) {
		dials.Add(1)
		return fakeConnection(t, h), nil
	}
	a := openConnection(t, p, connectionHost("a"))
	p.Park()
	failure := errors.New("capability changed")
	p.check = func(context.Context, *Session, Options) error { return failure }
	if _, err := p.Open(context.Background(), a.Host, Options{}); !errors.Is(err, failure) {
		t.Fatalf("reuse failure = %v", err)
	}
	awaitConnectionClosed(t, a)
	if dials.Load() != 1 {
		t.Fatal("reuse failure automatically started another connection")
	}
}

func TestConnectionsCloseWaitsForLateDial(t *testing.T) {
	p := testConnections(t)
	entered, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a := fakeConnection(t, connectionHost("a"))
	p.dial = func(ctx context.Context, _ Host, _ Options) (*Session, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-finish
		return a, nil
	}
	result := make(chan error, 1)
	go func() { _, err := p.Open(context.Background(), a.Host, Options{}); result <- err }()
	<-entered
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	<-cancelled
	select {
	case <-closed:
		t.Fatal("Close returned before dial was collected")
	default:
	}
	close(finish)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("late dial published after Close")
	}
	if sessionAlive(a) {
		t.Fatal("late master leaked after Close")
	}
}
