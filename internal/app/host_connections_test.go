package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"posse/internal/client"
	"posse/internal/remote"
	"posse/internal/rpc"
)

func connectionFixture(t *testing.T) (*hostConnections, *app, *app, *hostControlFixture, *client.Client, chan struct{}) {
	t.Helper()
	v, a, b := hostViewsFixture(t)
	f, c := newHostControlFixture(t)
	c.Remote, c.Home = true, "/target/home"
	closed := make(chan struct{})
	k := newHostConnections(v)
	a.hosts = k
	k.dial = func(context.Context, remote.Host, remote.Options) (hostTransport, error) {
		return hostTransport{c, closed}, nil
	}
	t.Cleanup(k.close)
	return k, a, b, f, c, closed
}

func remoteTestHost(id string) remote.Host {
	return remote.Host{ID: id, Name: id, Target: "test.invalid"}
}

func waitConnectionEvent(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("connection event did not arrive")
	}
}

func TestHostConnectionsHandshakeAndClose(t *testing.T) {
	k, _, b, f, base, _ := connectionFixture(t)
	k.connectRemote(remoteTestHost("remote"), false)
	awaitHostUpdate(t, b)
	s := b.host.snapshot()
	if b.connecting || !s.Connected || !s.Controlled || s.Client.Home != base.Home || !s.Client.Remote {
		t.Fatalf("remote handshake = %+v", s)
	}
	f.mu.Lock()
	takes := f.takes
	f.mu.Unlock()
	if takes != 1 {
		t.Fatalf("unowned remote connection did not auto-take control: takes=%d", takes)
	}
	k.close()
	f.mu.Lock()
	owner := f.state.Owner
	f.mu.Unlock()
	if owner != "" || b.host.snapshot().Connected {
		t.Fatal("window close retained desktop lease")
	}
	if !base.Up() {
		t.Fatal("window close stopped target service")
	}
}

func TestHostConnectionsLateDialAfterSwitch(t *testing.T) {
	k, _, a, _, base, closed := connectionFixture(t)
	entered, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(finish) }) })
	k.dial = func(ctx context.Context, h remote.Host, _ remote.Options) (hostTransport, error) {
		if h.ID == "remote" {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-finish
		}
		return hostTransport{base, closed}, nil
	}
	k.connectRemote(remoteTestHost("remote"), false)
	waitConnectionEvent(t, entered)
	k.connectRemote(remoteTestHost("other"), false)
	b := k.views.all["other"]
	waitConnectionEvent(t, cancelled)
	once.Do(func() { close(finish) })
	awaitHostUpdate(t, b)
	a.runPending()
	if a.host.snapshot().Connected || a.connecting || !b.host.snapshot().Connected || k.views.active.Load() != b {
		t.Fatal("late connection revived old host or displaced current host")
	}
}

func TestHostConnectionsAuthenticationCancellation(t *testing.T) {
	k, a, b, _, _, _ := connectionFixture(t)
	k.dial = func(ctx context.Context, _ remote.Host, opts remote.Options) (hostTransport, error) {
		_, err := opts.Prompt(ctx, remote.Prompt{Text: "Password:"})
		return hostTransport{}, err
	}
	k.connectRemote(remoteTestHost("remote"), false)
	// 切主机先排了唤醒，再等待真正的认证请求。
	deadline := time.Now().Add(3 * time.Second)
	for a.sshAuth.current() == nil {
		if time.Now().After(deadline) {
			t.Fatal("connection did not request authentication")
		}
		time.Sleep(time.Millisecond)
	}
	q := a.sshAuth.current()
	if q.host != "remote" {
		t.Fatal("authentication named a different host")
	}
	a.sshAuth.finish(q, "", context.Canceled)
	awaitHostUpdate(t, b)
	if b.connecting || b.host.snapshot().Connected || k.active != nil || b.err != "" {
		t.Fatal("cancelled authentication left a connection or an error banner")
	}
}

func TestHostConnectionsHeartbeatAndMasterExit(t *testing.T) {
	k, _, b, f, _, closed := connectionFixture(t)
	k.connectRemote(remoteTestHost("remote"), false)
	awaitHostUpdate(t, b)
	if err := b.host.take(context.Background(), b.host.snapshot()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.heartbeatFail = true
	f.mu.Unlock()
	if !k.heartbeat(b) || b.host.snapshot().Controlled {
		t.Fatal("failed heartbeat did not demote to viewer")
	}
	k.disconnect("other")
	if !b.host.snapshot().Connected {
		t.Fatal("disconnecting another host affected current host")
	}
	close(closed)
	awaitHostUpdate(t, b)
	if b.host.snapshot().Connected || k.active != nil || b.err == "" {
		t.Fatal("master exit was not published")
	}
}

func TestHostConnectionsCloseCollectsDial(t *testing.T) {
	k, _, b, _, base, closed := connectionFixture(t)
	entered, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(finish) }) })
	k.dial = func(ctx context.Context, _ remote.Host, _ remote.Options) (hostTransport, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-finish
		return hostTransport{base, closed}, nil
	}
	k.connectRemote(remoteTestHost("remote"), false)
	waitConnectionEvent(t, entered)
	done := make(chan struct{})
	go func() { k.close(); close(done) }()
	waitConnectionEvent(t, cancelled)
	select {
	case <-done:
		t.Fatal("close returned before collecting dial")
	default:
	}
	once.Do(func() { close(finish) })
	waitConnectionEvent(t, done)
	b.runPending()
	if b.connecting || b.host.snapshot().Connected {
		t.Fatal("queued completion revived closed connection")
	}
}

type observedHostCleanup struct {
	hostTransportCleanup
	disconnected chan struct{}
	closed       chan struct{}
}

func (p *observedHostCleanup) Disconnect(id string) {
	p.disconnected <- struct{}{}
	p.hostTransportCleanup.Disconnect(id)
}

func (p *observedHostCleanup) Close() {
	p.closed <- struct{}{}
	p.hostTransportCleanup.Close()
}

func TestHostConnectionsReleaseBeforeTransportClose(t *testing.T) {
	for _, action := range []string{"disconnect", "reconnect", "close"} {
		t.Run(action, func(t *testing.T) {
			k, _, b, f, _, _ := connectionFixture(t)
			k.connectRemote(remoteTestHost("remote"), false)
			awaitHostUpdate(t, b)
			if err := b.host.take(context.Background(), b.host.snapshot()); err != nil {
				t.Fatal(err)
			}
			entered, finish := make(chan struct{}), make(chan struct{})
			var signal, once sync.Once
			t.Cleanup(func() { once.Do(func() { close(finish) }) })
			f.mu.Lock()
			f.beforeRelease = func() { signal.Do(func() { close(entered) }); <-finish }
			f.mu.Unlock()
			pool := &observedHostCleanup{k.pool, make(chan struct{}, 8), make(chan struct{}, 8)}
			k.pool = pool
			done := make(chan struct{})
			if action == "close" {
				go func() { k.close(); close(done) }()
			} else {
				k.disconnect("remote")
			}
			waitConnectionEvent(t, entered)
			if action != "close" {
				k.disconnect("remote")
			}
			select {
			case <-pool.disconnected:
				t.Fatal("transport disconnected before release")
			default:
			}
			select {
			case <-pool.closed:
				t.Fatal("transport closed before release")
			default:
			}
			if action == "reconnect" {
				k.connectRemote(remoteTestHost("remote"), false)
				awaitHostUpdate(t, b)
			}
			once.Do(func() { close(finish) })
			if action == "close" {
				waitConnectionEvent(t, done)
				waitConnectionEvent(t, pool.closed)
			} else {
				awaitHostUpdate(t, b)
				if action == "disconnect" {
					waitConnectionEvent(t, pool.disconnected)
				} else {
					select {
					case <-pool.disconnected:
						t.Fatal("old release closed reconnected transport")
					default:
					}
					if !b.host.snapshot().Connected {
						t.Fatal("reconnected host was detached")
					}
				}
			}
			f.mu.Lock()
			owner := f.state.Owner
			f.mu.Unlock()
			if action == "reconnect" {
				if !b.host.snapshot().Controlled || owner != b.host.controllerID {
					t.Fatalf("old release revoked reconnected control: snap=%+v owner=%q", b.host.snapshot(), owner)
				}
			} else if owner != "" {
				t.Fatal("transport cleanup left the desktop lease held")
			}
		})
	}
}

func TestHostConnectionsPollCancellation(t *testing.T) {
	k, a, b, f, _, _ := connectionFixture(t)
	k.connectRemote(remoteTestHost("remote"), false)
	awaitHostUpdate(t, b)
	entered, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(finish) }) })
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "project.list" {
			close(entered)
			<-finish
		}
		return nil, errors.New("test stopped")
	}
	f.mu.Unlock()
	done := make(chan struct{})
	go func() { a.poll(k.ctx); close(done) }()
	waitConnectionEvent(t, entered)
	k.cancel()
	waitConnectionEvent(t, done)
	once.Do(func() { close(finish) })
}
