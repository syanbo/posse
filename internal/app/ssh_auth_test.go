package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
)

func authFixture(t *testing.T) (*app, chan struct{}) {
	t.Helper()
	wake := make(chan struct{}, 16)
	a := &app{sshAuth: &sshAuthentication{wake: func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}}}
	t.Cleanup(func() { a.sshAuth.cancelPending(true) })
	return a, wake
}

func startAuth(t *testing.T, s *sshAuthentication, wake <-chan struct{}, ctx context.Context, host string, p remote.Prompt, cancel context.CancelFunc) <-chan sshAuthResult {
	t.Helper()
	result := make(chan sshAuthResult, 1)
	go func() { answer, err := s.ask(ctx, host, p, cancel); result <- sshAuthResult{answer, err} }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		found := false
		for _, q := range s.queue {
			if q.host == host {
				found = true
			}
		}
		s.mu.Unlock()
		if found {
			return result
		}
		select {
		case <-wake:
		case <-deadline.C:
			t.Fatal("authentication was not queued")
		}
	}
}

func authResult(t *testing.T, result <-chan sshAuthResult) sshAuthResult {
	t.Helper()
	select {
	case r := <-result:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("authentication did not finish")
	}
	return sshAuthResult{}
}

func TestSSHAuthPassword(t *testing.T) {
	for _, value := range []string{"  secret with spaces  ", ""} {
		t.Run(fmt.Sprint(len(value)), func(t *testing.T) {
			a, wake := authFixture(t)
			result := startAuth(t, a.sshAuth, wake, context.Background(), "Office", remote.Prompt{Text: "Password:"}, nil)
			tt := newTester(a.sshAuthDialog, 760, 480)
			settle(tt)
			tt.Type(value)
			if err := tt.Click("继续"); err != nil {
				t.Fatal(err)
			}
			r := authResult(t, result)
			if r.err != nil || r.answer != value {
				t.Fatal("password changed or was rejected")
			}
			if a.sshAuth.current() != nil {
				t.Fatal("answered prompt remained open")
			}
		})
	}
}

func TestSSHAuthConfirmAndCancel(t *testing.T) {
	for _, action := range []string{"accept", "enter", "escape", "cancel"} {
		t.Run(action, func(t *testing.T) {
			a, wake := authFixture(t)
			var cancelled atomic.Int32
			result := startAuth(t, a.sshAuth, wake, context.Background(), "Office", remote.Prompt{Text: "Fingerprint SHA256:example", Confirm: true}, func() { cancelled.Add(1) })
			tt := newTester(a.sshAuthDialog, 760, 480)
			settle(tt)
			switch action {
			case "accept":
				if err := tt.Click("信任并连接"); err != nil {
					t.Fatal(err)
				}
			case "enter":
				tt.Key(0, ui.KeyEnter)
			case "escape":
				tt.Key(0, ui.KeyEscape)
			case "cancel":
				if err := tt.Click("取消"); err != nil {
					t.Fatal(err)
				}
			}
			r := authResult(t, result)
			if action == "accept" {
				if r.err != nil || r.answer != "yes" || cancelled.Load() != 0 {
					t.Fatal("explicit trust failed")
				}
			} else if !errors.Is(r.err, context.Canceled) || r.answer != "" || cancelled.Load() != 1 {
				t.Fatal("dismissal did not cancel connection")
			}
		})
	}
}

func TestSSHAuthNotificationAndQueue(t *testing.T) {
	a, wake := authFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := startAuth(t, a.sshAuth, wake, ctx, "First", remote.Prompt{Text: "Touch your security key", Notify: true}, nil)
	old := a.sshAuth.current()
	second := startAuth(t, a.sshAuth, wake, context.Background(), "Second", remote.Prompt{Text: "Passphrase:"}, nil)
	tt := newTester(a.sshAuthDialog, 760, 480)
	settle(tt)
	if tt.HasText("继续") || tt.HasText("密码或私钥口令") {
		t.Fatal("notification asked for an answer")
	}
	cancel()
	if r := authResult(t, first); !errors.Is(r.err, context.Canceled) {
		t.Fatal("notification did not disappear on cancellation")
	}
	q := a.sshAuth.current()
	if q == nil || q.host != "Second" {
		t.Fatal("cancellation removed next prompt")
	}
	a.sshAuth.finish(old, "yes", nil)
	if a.sshAuth.current() != q {
		t.Fatal("old callback changed new prompt")
	}
	settle(tt)
	tt.Type("next passphrase")
	if err := tt.Click("继续"); err != nil {
		t.Fatal(err)
	}
	if r := authResult(t, second); r.err != nil || r.answer != "next passphrase" {
		t.Fatal("next prompt lost focus or answer")
	}
}

func TestSSHAuthSwitchAndClose(t *testing.T) {
	v, a, _ := hostViewsFixture(t)
	wake := make(chan struct{}, 16)
	a.sshAuth.wake = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { a.sshAuth.cancelPending(true) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := startAuth(t, a.sshAuth, wake, ctx, "Office", remote.Prompt{}, cancel)
	v.activate("remote")
	if r := authResult(t, result); !errors.Is(r.err, context.Canceled) || ctx.Err() == nil {
		t.Fatal("host switch left authentication running")
	}
	result = startAuth(t, a.sshAuth, wake, context.Background(), "New", remote.Prompt{}, nil)
	a.sshAuth.cancelPending(true)
	if r := authResult(t, result); !errors.Is(r.err, context.Canceled) {
		t.Fatal("closing left authentication waiting")
	}
	if _, err := a.sshAuth.ask(context.Background(), "Late", remote.Prompt{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("authentication reopened after close")
	}
}

func TestPreviewSSHAuth(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		for _, kind := range []string{"password", "confirm", "notify"} {
			t.Run(fmt.Sprintf("%s-%t", kind, dark), func(t *testing.T) {
				a, wake := authFixture(t)
				p := remote.Prompt{Text: "office-mac password:"}
				switch kind {
				case "confirm":
					p = remote.Prompt{Text: "The authenticity of host 'office-mac' can't be established.\nED25519 key fingerprint is SHA256:ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefg.\nAre you sure you want to continue connecting?", Confirm: true}
				case "notify":
					p = remote.Prompt{Text: "Confirm user presence for key ED25519-SK. Touch your security key.", Notify: true}
				}
				startAuth(t, a.sshAuth, wake, context.Background(), "Office Mac", p, nil)
				tt := newTester(func(c *ui.Context) {
					c.SetTheme(posseTheme(c.Theme().Dark))
					a.sshAuthDialog(c)
				}, 760, 480)
				tt.SetDark(dark)
				settle(tt)
				savePNG(t, tt, fmt.Sprintf("ssh-auth-%s-%t.png", kind, dark))
			})
		}
	}
}
