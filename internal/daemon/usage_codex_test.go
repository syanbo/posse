package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/usage"
)

type usageTransport func(*http.Request) (*http.Response, error)

func (f usageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func codexUsageDaemon(t *testing.T, transport usageTransport) *Daemon {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"test-token","account_id":"test-account"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Home: t.TempDir()}}
	d.usage.store = usage.Open(d.cfg.Home)
	d.usage.http = &http.Client{Transport: transport}
	return d
}

func TestCodexUsageRefreshState(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	d := codexUsageDaemon(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		status, body := http.StatusOK, `{"rate_limit":{"secondary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_after_seconds":2520}}}`
		if fail.Load() {
			status, body = http.StatusServiceUnavailable, "unavailable"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	refresh := func(force bool) *usageStatus {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"agent": "codex", "force": force})
		res, err := d.usageRefresh(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.(*usageStatus)
	}
	if st := refresh(false); st.CodexError != "" || st.CodexReading || len(st.Usages) != 1 || st.Usages[0].Windows[0].Percent != 100 {
		t.Fatalf("initial refresh: %+v", st)
	}
	refresh(false)
	if calls.Load() != 1 {
		t.Fatal("automatic refresh ignored its one-minute interval")
	}
	fail.Store(true)
	if st := refresh(true); st.CodexError == "" || st.CodexReading || st.Usages[0].Windows[0].Percent != 100 {
		t.Fatalf("failure lost its status or previous reading: %+v", st)
	}
	fail.Store(false)
	if st := refresh(true); st.CodexError != "" {
		t.Fatalf("successful retry kept the error: %+v", st)
	}
}

func TestCodexUsageDetailsTimeout(t *testing.T) {
	d := codexUsageDaemon(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/rate-limit-reset-credits") {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		body := `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}},"rate_limit_reset_credits":{"available_count":2}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	d.usage.store.Update(usage.Usage{Agent: "codex", UpdatedAt: time.Now().Add(-time.Hour).UnixMilli(),
		Windows: []usage.Window{{Minutes: 300, Percent: 70}}, Resets: &usage.Resets{Count: 3}})
	d.codexAsk(context.Background(), true)
	st := d.usageStatus()
	if st.CodexError != "codex: reset credits query timed out" || st.CodexReading {
		t.Fatalf("details timeout lost its specific error: %+v", st)
	}
	u := st.Usages[0]
	if u.Windows[0].Percent != 10 || u.Resets == nil || u.Resets.Count != 3 || time.Since(time.UnixMilli(u.UpdatedAt)) > time.Minute {
		t.Fatalf("details timeout lost fresh quota or cached credits: %+v", u)
	}
}

func TestCodexUsageConcurrentRefresh(t *testing.T) {
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	d := codexUsageDaemon(t, func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000}}}`)), Header: http.Header{}}, nil
	})
	defer close(release)
	go func() {
		d.codexAsk(context.Background(), true)
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("query did not start")
	}
	if !d.usageStatus().CodexReading {
		t.Fatal("query not reported as pending")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := d.usageRefresh(ctx, json.RawMessage(`{"agent":"codex","force":true}`))
	if err != context.DeadlineExceeded || calls.Load() != 1 || !d.usageStatus().CodexReading {
		t.Fatalf("a canceled waiter canceled or duplicated the shared query: err=%v calls=%d", err, calls.Load())
	}
	select {
	case <-finished:
		t.Fatal("waiter cancellation completed the shared query")
	default:
	}
	// 解除网络等待后，仍在等待的调用能拿到读数。
	release <- struct{}{}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("shared query did not finish")
	}
	if st := d.usageStatus(); st.CodexReading || st.CodexError != "" || len(st.Usages) != 1 {
		t.Fatalf("completion not published: %+v", st)
	}
}
