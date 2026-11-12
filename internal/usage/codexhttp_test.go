package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func codexTestHome(t *testing.T, auth string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

const codexTestAuth = `{"auth_mode":"chatgpt","tokens":{"access_token":"test-token","account_id":"test-account"}}`

func TestFetchCodexHTTP(t *testing.T) {
	now := time.Unix(1792430000, 0)
	home := codexTestHome(t, codexTestAuth)
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("ChatGPT-Account-ID") != "test-account" || r.Header.Get("Accept") != "application/json" {
			t.Error("unexpected request method or headers")
		}
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/wham/usage":
			fmt.Fprint(w, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":2,"limit_window_seconds":18000,"reset_at":1792446937},"secondary_window":{"used_percent":8,"limit_window_seconds":604800,"reset_at":1792900210}},"rate_limit_reset_credits":{"available_count":3}}`)
		case "/wham/rate-limit-reset-credits":
			fmt.Fprint(w, `{"available_count":3,"credits":[{"status":"available","expires_at":"2026-10-29T15:22:36Z"},{"status":"available","expires_at":"2026-10-22T19:49:50Z"},{"status":"redeemed","expires_at":"2026-01-01T00:00:00Z"},{"status":"available","expires_at":null}]}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	u, err := fetchCodex(context.Background(), s.Client(), home, now, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := []Window{{Percent: 2, Minutes: 300, ResetsAt: 1792446937000}, {Percent: 8, Minutes: 10080, ResetsAt: 1792900210000}}
	if u.Agent != "codex" || u.Plan != "Plus" || u.UpdatedAt != now.UnixMilli() || !reflect.DeepEqual(u.Windows, want) || u.Covers&CoverResets == 0 {
		t.Fatalf("usage = %+v", u)
	}
	if u.Resets == nil || u.Resets.Count != 3 || u.Resets.ExpiresAt != isoMs("2026-10-22T19:49:50Z") {
		t.Fatalf("resets = %+v", u.Resets)
	}
	if !reflect.DeepEqual(paths, []string{"/wham/usage", "/wham/rate-limit-reset-credits"}) {
		t.Fatalf("paths = %v", paths)
	}
	b, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || string(b) != codexTestAuth {
		t.Fatal("credentials changed")
	}
}

func TestFetchCodexResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError string
	}{
		{"unauthorized", 401, "secret-response", "login expired"},
		{"forbidden", 403, "secret-response", "HTTP 403"},
		{"limited", 429, "secret-response", "HTTP 429"},
		{"server", 500, "secret-response", "HTTP 500"},
		{"invalid", 200, "secret-response", "invalid usage response"},
		{"large", 200, strings.Repeat("x", (1<<20)+1), "invalid usage response"},
		{"empty", 200, `{}`, "no rate limits"},
		{"null", 200, `null`, "no rate limits"},
		{"resets without windows", 200, `{"rate_limit_reset_credits":{"available_count":0}}`, "no rate limits"},
		{"zero resets", 200, `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000}},"rate_limit_reset_credits":{"available_count":0}}`, ""},
		{"unknown resets", 200, `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_after_seconds":60}}}`, ""},
		{"clamp", 200, `{"rate_limit":{"primary_window":{"used_percent":101},"secondary_window":{"used_percent":-2}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := codexTestHome(t, codexTestAuth)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/wham/usage" {
					t.Error("unexpected details request")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			u, err := fetchCodex(context.Background(), s.Client(), home, time.Unix(1000, 0), s.URL)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(err.Error(), "secret-response") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "zero resets" && (u.Resets == nil || u.Resets.Count != 0) {
				t.Fatalf("resets = %+v", u.Resets)
			}
			if tc.name == "unknown resets" && (u.Resets != nil || u.Windows[0].ResetsAt != 1060000) {
				t.Fatalf("usage = %+v", u)
			}
			if tc.name == "clamp" && (u.Windows[0].Percent != 100 || u.Windows[1].Percent != 0) {
				t.Fatalf("windows = %+v", u.Windows)
			}
		})
	}
}

func TestFetchCodexIncompleteResponsePreservesCache(t *testing.T) {
	const window = `"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}`
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
		percent float64
		count   int
	}{
		{"missing resets", `{` + window + `}`, false, 10, 3},
		{"missing count", `{` + window + `,"rate_limit_reset_credits":{}}`, false, 10, 3},
		{"null count", `{` + window + `,"rate_limit_reset_credits":{"available_count":null}}`, false, 10, 3},
		{"zero resets", `{` + window + `,"rate_limit_reset_credits":{"available_count":0}}`, false, 10, 0},
		{"missing windows", `{"rate_limit_reset_credits":{"available_count":0}}`, true, 70, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			home := t.TempDir()
			store := Open(home)
			old := Usage{Agent: "codex", UpdatedAt: now.UnixMilli() - 1000,
				Windows: []Window{{Minutes: 300, Percent: 70}}, Resets: &Resets{Count: 3, ExpiresAt: 2000000}}
			store.Update(old)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/wham/usage" {
					t.Error("unexpected details request")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			u, err := fetchCodex(context.Background(), s.Client(), codexTestHome(t, codexTestAuth), now, s.URL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v", err)
			}
			store.Update(u)
			// 重开验证落盘结果，不能只保证本次返回值还在。
			got := Open(home).List(now)[0]
			if len(got.Windows) != 1 || got.Windows[0].Percent != tc.percent || got.Resets == nil || got.Resets.Count != tc.count {
				t.Fatalf("incomplete response changed cached data: %+v", got)
			}
			if tc.wantErr && got.UpdatedAt != old.UpdatedAt {
				t.Fatalf("rejected response advanced reading time: %d", got.UpdatedAt)
			}
		})
	}
}

func TestFetchCodexDetailsFailure(t *testing.T) {
	home := codexTestHome(t, codexTestAuth)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wham/usage" {
			fmt.Fprint(w, `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}},"rate_limit_reset_credits":{"available_count":2}}`)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer s.Close()
	u, err := fetchCodex(context.Background(), s.Client(), home, time.Now(), s.URL)
	if err == nil || !strings.Contains(err.Error(), "could not read reset credits") || !strings.Contains(err.Error(), "HTTP 503") || u.Agent != "codex" || len(u.Windows) != 1 || u.Resets != nil || u.Covers&CoverResets != 0 {
		t.Fatalf("usage = %+v, error = %v", u, err)
	}
	old := Usage{Agent: "codex", UpdatedAt: u.UpdatedAt - 1000, Resets: &Resets{Count: 3, ExpiresAt: 1234}}
	merged := merge(old, u)
	if merged.Resets == nil || merged.Resets.Count != 3 || merged.Resets.ExpiresAt != 1234 || len(merged.Windows) != 1 || merged.Windows[0].Percent != 10 {
		t.Fatalf("partial reading merged = %+v", merged)
	}
}

func TestFetchCodexContextAndTransport(t *testing.T) {
	home := codexTestHome(t, codexTestAuth)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := fetchCodex(ctx, s.Client(), home, time.Now(), s.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = fetchCodex(ctx, s.Client(), home, time.Now(), s.URL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	s.Close()
	_, err = fetchCodex(context.Background(), s.Client(), home, time.Now(), s.URL)
	if err == nil || err.Error() != "codex: usage request failed" {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchCodexNoRedirect(t *testing.T) {
	home := codexTestHome(t, codexTestAuth)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed redirect") }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer s.Close()
	_, err := fetchCodex(context.Background(), s.Client(), home, time.Now(), s.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("error = %v", err)
	}
}

func TestCodexCredential(t *testing.T) {
	jwt := func(payload string) string {
		return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
	}
	for _, tc := range []struct{ name, auth, account, wantError string }{
		{"file", codexTestAuth, "test-account", ""},
		{"legacy", `{"tokens":{"access_token":"test-token","account_id":"test-account"}}`, "test-account", ""},
		{"key", `{"OPENAI_API_KEY":"test-key"}`, "", "requires ChatGPT login"},
		{"explicit key", `{"auth_mode":"apikey","tokens":{"access_token":"test-token","account_id":"test-account"}}`, "", "requires ChatGPT login"},
		{"logged out", `{}`, "", "not logged in"},
		{"invalid", `{"tokens":"secret"}`, "", "invalid auth.json"},
		{"no account", `{"tokens":{"access_token":"test-token"}}`, "", "account ID is missing"},
		{"expired is tried remotely", fmt.Sprintf(`{"tokens":{"access_token":%q,"account_id":"test-account"}}`, jwt(`{"exp":999}`)), "test-account", ""},
		{"id claim", fmt.Sprintf(`{"tokens":{"access_token":"test-token","id_token":%q}}`, jwt(`{"https://api.openai.com/auth":{"chatgpt_account_id":"from-id"}}`)), "from-id", ""},
		{"access claim", fmt.Sprintf(`{"tokens":{"access_token":%q}}`, jwt(`{"exp":2000,"https://api.openai.com/auth":{"chatgpt_account_id":"from-access"}}`)), "from-access", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := codexTestHome(t, tc.auth)
			token, account, err := codexCredential(home, time.Unix(1000, 0))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || token == "" || account != tc.account {
				t.Fatalf("account = %q, error = %v", account, err)
			}
		})
	}
	_, _, err := codexCredential(t.TempDir(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("error = %v", err)
	}
	for _, mode := range []string{"file", "keyring", "auto", "ephemeral"} {
		home := codexTestHome(t, codexTestAuth)
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf("cli_auth_credentials_store = %q", mode)), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err := codexCredential(home, time.Now())
		if mode == "file" && err != nil || mode != "file" && (err == nil || !strings.Contains(err.Error(), "not supported")) {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
}

const codexRefreshAuth = `{"auth_mode":"chatgpt","extra":{"keep":true},"tokens":{"access_token":"test-token","refresh_token":"test-refresh","account_id":"test-account","login_owner_id":"keep-owner"}}`

func TestFetchCodexRefresh(t *testing.T) {
	for _, scenario := range []string{"refresh", "external before refresh", "external during refresh", "external after failed refresh", "refresh rejected", "refresh unavailable", "refresh invalid", "custom issuer"} {
		t.Run(scenario, func(t *testing.T) {
			auth := codexRefreshAuth
			if scenario == "custom issuer" {
				auth = strings.Replace(auth, `"login_owner_id":"keep-owner"`, `"oauth_provenance":{"issuer":"https://other.invalid","client_id":"other"}`, 1)
			}
			home := codexTestHome(t, auth)
			var usageCalls, refreshCalls int
			replace := func() {
				if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(strings.Replace(codexRefreshAuth, "test-token", "external-token", 1)), 0600); err != nil {
					t.Error(err)
				}
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/wham/usage" {
					usageCalls++
					if r.Header.Get("Authorization") == "Bearer test-token" {
						if scenario == "external before refresh" {
							replace()
						}
						w.WriteHeader(401)
						return
					}
					want := "Bearer refreshed-token"
					if strings.HasPrefix(scenario, "external") {
						want = "Bearer external-token"
					}
					if r.Header.Get("Authorization") != want {
						t.Error("wrong retry authorization")
					}
					fmt.Fprint(w, `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}}`)
					return
				}
				if r.URL.Path != "/token" {
					t.Error("unexpected endpoint")
					w.WriteHeader(404)
					return
				}
				refreshCalls++
				var request map[string]string
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&request) != nil || request["refresh_token"] != "test-refresh" || request["client_id"] != codexOAuthClient || request["grant_type"] != "refresh_token" {
					t.Error("invalid refresh request")
				}
				switch scenario {
				case "external during refresh":
					replace()
				case "external after failed refresh":
					replace()
					w.WriteHeader(401)
					return
				case "refresh rejected":
					w.WriteHeader(401)
					fmt.Fprint(w, "secret-response")
					return
				case "refresh unavailable":
					w.WriteHeader(503)
					fmt.Fprint(w, "secret-response")
					return
				case "refresh invalid":
					fmt.Fprint(w, `{"access_token":"","refresh_token":"secret-response"}`)
					return
				}
				fmt.Fprint(w, `{"access_token":"refreshed-token","refresh_token":"refreshed-refresh","id_token":"refreshed-id"}`)
			}))
			defer s.Close()
			u, err := fetchCodexEndpoints(context.Background(), s.Client(), home, time.Unix(1000, 0), s.URL, s.URL+"/token")
			if strings.HasPrefix(scenario, "refresh ") || scenario == "custom issuer" {
				if err == nil || strings.Contains(err.Error(), "secret-response") || u.Agent != "" {
					t.Fatalf("usage = %+v, error = %v", u, err)
				}
				b, _ := os.ReadFile(filepath.Join(home, "auth.json"))
				if string(b) != auth {
					t.Fatal("credentials changed after failed refresh")
				}
				if scenario == "custom issuer" && refreshCalls != 0 {
					t.Fatal("refreshed custom issuer")
				}
				return
			}
			if err != nil || len(u.Windows) != 1 || usageCalls != 2 {
				t.Fatalf("usage = %+v, calls = %d, error = %v", u, usageCalls, err)
			}
			if scenario == "external before refresh" && refreshCalls != 0 {
				t.Fatal("refreshed despite external update")
			}
			b, err := os.ReadFile(filepath.Join(home, "auth.json"))
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "refresh" {
				if string(b) != strings.Replace(codexRefreshAuth, "test-token", "external-token", 1) {
					t.Fatal("overwrote external credentials")
				}
				return
			}
			var saved struct {
				Extra  map[string]bool   `json:"extra"`
				Tokens map[string]string `json:"tokens"`
				Last   string            `json:"last_refresh"`
			}
			if json.Unmarshal(b, &saved) != nil || !saved.Extra["keep"] || saved.Tokens["login_owner_id"] != "keep-owner" || saved.Tokens["access_token"] != "refreshed-token" || saved.Tokens["refresh_token"] != "refreshed-refresh" || saved.Tokens["id_token"] != "refreshed-id" || saved.Last != "1970-01-01T00:16:40Z" {
				t.Fatal("refreshed document lost fields")
			}
			fi, err := os.Stat(filepath.Join(home, "auth.json"))
			if err != nil || fi.Mode().Perm() != 0600 {
				t.Fatal("refreshed document permissions")
			}
		})
	}
}

func TestCodexRefreshConcurrent(t *testing.T) {
	home := codexTestHome(t, codexRefreshAuth)
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		fmt.Fprint(w, `{"access_token":"new-token"}`)
	}))
	defer s.Close()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			token, _, err := refreshCodexCredential(context.Background(), s.Client(), home, "test-token", s.URL, time.Now())
			if err != nil || token != "new-token" {
				t.Errorf("refresh failed: %v", err)
			}
		})
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("refresh calls = %d", count.Load())
	}
}

func TestCodexRefreshLockContext(t *testing.T) {
	home := codexTestHome(t, codexRefreshAuth)
	lock, err := os.OpenFile(filepath.Join(home, "auth.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"access_token":"new-token"}`) }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err = refreshCodexCredential(ctx, s.Client(), home, "test-token", s.URL, time.Now())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestCodexHomeOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if got := CodexHome(); got != home {
		t.Fatalf("home = %q", got)
	}
}
