package usage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

const codexUsageBase = "https://chatgpt.com/backend-api"
const codexTokenEndpoint = "https://auth.openai.com/oauth/token"
const codexOAuthClient = "app_EMoamEEZ73f0CkXaXp7hrann"

var errCodexLoginExpired = errors.New("codex: login expired; run codex login again")
var codexRefreshGate = make(chan struct{}, 1)

// FetchCodex 直接查询 ChatGPT 套餐用量。home 是 CodexHome()。
// file 登录遇到 401 先重读，再刷新；仅在原凭据未改变时原子保存。
func FetchCodex(ctx context.Context, client *http.Client, home string, now time.Time) (Usage, error) {
	return fetchCodex(ctx, client, home, now, codexUsageBase)
}

func fetchCodex(ctx context.Context, client *http.Client, home string, now time.Time, base string) (Usage, error) {
	return fetchCodexEndpoints(ctx, client, home, now, base, codexTokenEndpoint)
}

func fetchCodexEndpoints(ctx context.Context, client *http.Client, home string, now time.Time, base, tokenEndpoint string) (Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Usage{}, err
	}
	token, account, err := codexCredential(home, now)
	if err != nil {
		return Usage{}, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	// 禁止重定向，避免自定义账号 header 随跳转发给别的站点。
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	type window struct {
		Used    *float64 `json:"used_percent"`
		Seconds int      `json:"limit_window_seconds"`
		ResetAt int64    `json:"reset_at"`
		After   int64    `json:"reset_after_seconds"`
	}
	var data struct {
		Plan      string `json:"plan_type"`
		RateLimit struct {
			Primary   *window `json:"primary_window"`
			Secondary *window `json:"secondary_window"`
		} `json:"rate_limit"`
		Resets *struct {
			Count *int `json:"available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	err = codexGET(ctx, &c, base+"/wham/usage", token, account, &data)
	if errors.Is(err, errCodexLoginExpired) {
		var freshToken, freshAccount string
		freshToken, freshAccount, err = codexCredential(home, now)
		if err == nil && freshToken != token {
			token, account = freshToken, freshAccount
			err = codexGET(ctx, &c, base+"/wham/usage", token, account, &data)
		} else if err == nil {
			err = errCodexLoginExpired
		}
		if errors.Is(err, errCodexLoginExpired) {
			token, account, err = refreshCodexCredential(ctx, &c, home, token, tokenEndpoint, now)
			if err == nil {
				err = codexGET(ctx, &c, base+"/wham/usage", token, account, &data)
			}
		}
	}
	if err != nil {
		return Usage{}, err
	}
	u := Usage{Agent: "codex", Plan: titleWord(data.Plan), UpdatedAt: now.UnixMilli()}
	for _, w := range []*window{data.RateLimit.Primary, data.RateLimit.Secondary} {
		if w == nil || w.Used == nil {
			continue
		}
		var reset int64
		if w.ResetAt > 0 {
			reset = w.ResetAt * 1000
		} else if w.After > 0 {
			reset = now.Add(time.Duration(w.After) * time.Second).UnixMilli()
		}
		u.Windows = append(u.Windows, Window{Percent: clampPercent(*w.Used), Minutes: w.Seconds / 60, ResetsAt: reset})
	}
	// 没有主窗口的回复不能成为总额度的新读数，否则合并会清掉已有窗口。
	if len(u.Windows) == 0 {
		return Usage{}, errors.New("codex: no rate limits in response")
	}
	if data.Resets != nil && data.Resets.Count != nil {
		// 字段缺席只表示这次没读到；明确给出数量（包括零）才覆盖旧卡。
		u.Covers |= CoverResets
		u.Resets = &Resets{Count: max(0, *data.Resets.Count)}
		if u.Resets.Count > 0 {
			var details struct {
				Credits []struct {
					Status    string `json:"status"`
					ExpiresAt string `json:"expires_at"`
				} `json:"credits"`
			}
			// 数量和最早到期时间都属于这次读数；明细失败不能当成完整成功。
			detailCtx, done := context.WithTimeout(ctx, 4*time.Second)
			err := codexGET(detailCtx, &c, base+"/wham/rate-limit-reset-credits", token, account, &details)
			done()
			if err != nil {
				u.Resets = nil
				u.Covers &^= CoverResets
				return u, fmt.Errorf("codex: could not read reset credits: %w", err)
			}
			for _, credit := range details.Credits {
				at := isoMs(credit.ExpiresAt)
				if credit.Status == "available" && at > 0 && (u.Resets.ExpiresAt == 0 || at < u.Resets.ExpiresAt) {
					u.Resets.ExpiresAt = at
				}
			}
		}
	}
	return u, nil
}

func codexGET(ctx context.Context, client *http.Client, endpoint, token, account string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("codex: invalid usage endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-ID", account)
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		// 不把上游响应、header 或凭据放进错误；保留 context 错误供调用方识别。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("codex: usage request failed")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return errCodexLoginExpired
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("codex: usage request returned HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("codex: could not read usage response")
	}
	if len(b) > 1<<20 || json.Unmarshal(b, dst) != nil {
		return errors.New("codex: invalid usage response")
	}
	return nil
}

func codexCredential(home string, now time.Time) (token, account string, err error) {
	var config struct {
		Store string `toml:"cli_auth_credentials_store"`
	}
	if _, e := toml.DecodeFile(filepath.Join(home, "config.toml"), &config); e != nil && !errors.Is(e, os.ErrNotExist) {
		return "", "", errors.New("codex: could not read auth storage configuration")
	}
	// auto 优先 keyring；不能把磁盘上可能已失效的回退记录当成当前账号。
	if config.Store != "" && config.Store != "file" {
		return "", "", errors.New("codex: usage requires file auth storage; keyring, auto and ephemeral storage are not supported")
	}
	b, e := os.ReadFile(filepath.Join(home, "auth.json"))
	if errors.Is(e, os.ErrNotExist) {
		return "", "", errors.New("codex: not logged in; run codex login")
	}
	if e != nil {
		return "", "", errors.New("codex: could not read auth.json")
	}
	return parseCodexCredential(b)
}

func parseCodexCredential(b []byte) (token, account string, err error) {
	var auth struct {
		Mode   string `json:"auth_mode"`
		Key    string `json:"OPENAI_API_KEY"`
		Tokens struct {
			Access  string `json:"access_token"`
			Account string `json:"account_id"`
			ID      string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &auth) != nil {
		return "", "", errors.New("codex: invalid auth.json; run codex login again")
	}
	if auth.Mode != "" && auth.Mode != "chatgpt" || auth.Mode == "" && auth.Key != "" {
		return "", "", errors.New("codex: usage requires ChatGPT login; run codex login")
	}
	token, account = auth.Tokens.Access, auth.Tokens.Account
	if token == "" {
		return "", "", errors.New("codex: not logged in; run codex login")
	}
	claims := codexClaims(token)
	if account == "" {
		account = codexClaims(auth.Tokens.ID).Auth.Account
	}
	if account == "" {
		account = claims.Auth.Account
	}
	if account == "" {
		return "", "", errors.New("codex: ChatGPT account ID is missing; run codex login again")
	}
	return token, account, nil
}

// refreshCodexCredential 不在网络请求期间持 Codex 的 auth.lock；提交时与官方写入共用锁，
// 再比较完整旧文件。若 Codex 已换过账号或 token，只采用它的新记录，不覆盖。
func refreshCodexCredential(ctx context.Context, client *http.Client, home, stale, endpoint string, now time.Time) (string, string, error) {
	select {
	case codexRefreshGate <- struct{}{}:
		defer func() { <-codexRefreshGate }()
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	token, account, err := codexCredential(home, now)
	if err != nil || token != stale {
		return token, account, err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(home, "auth.json"))
	if err != nil {
		return "", "", errors.New("codex: could not read auth.json")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return "", "", errors.New("codex: could not read auth.json")
	}
	if current, id, e := parseCodexCredential(original); e != nil || current != stale {
		return current, id, e
	}
	var document map[string]json.RawMessage
	var tokens map[string]json.RawMessage
	if json.Unmarshal(original, &document) != nil || json.Unmarshal(document["tokens"], &tokens) != nil {
		return "", "", errors.New("codex: invalid auth.json; run codex login again")
	}
	var access, refresh string
	json.Unmarshal(tokens["access_token"], &access)
	json.Unmarshal(tokens["refresh_token"], &refresh)
	if access != stale {
		return codexCredential(home, now)
	}
	if refresh == "" {
		return "", "", errCodexLoginExpired
	}
	// 只刷新官方客户端签发的登录；不从 JWT 或自定义 issuer 推导发送凭据的地址。
	var provenance *struct {
		Issuer string `json:"issuer"`
		Client string `json:"client_id"`
	}
	if raw := tokens["oauth_provenance"]; len(raw) > 0 && json.Unmarshal(raw, &provenance) != nil {
		return "", "", errors.New("codex: unsupported OAuth login; run codex login again")
	}
	if provenance != nil && (strings.TrimRight(provenance.Issuer, "/") != "https://auth.openai.com" || provenance.Client != codexOAuthClient) {
		return "", "", errors.New("codex: unsupported OAuth login; run codex login again")
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": codexOAuthClient, "refresh_token": refresh})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", errors.New("codex: invalid token endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", errors.New("codex: token refresh request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// 别的 Codex 可能已轮换 refresh token；优先重读，避免误报需要重新登录。
		if current, id, e := codexCredential(home, now); e != nil || current != stale {
			return current, id, e
		}
		if res.StatusCode == 400 || res.StatusCode == 401 {
			return "", "", errCodexLoginExpired
		}
		return "", "", fmt.Errorf("codex: token refresh returned HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", errors.New("codex: could not read token refresh response")
	}
	var renewed struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
	}
	if len(b) > 1<<20 || json.Unmarshal(b, &renewed) != nil || renewed.Access == "" {
		return "", "", errors.New("codex: invalid token refresh response")
	}
	lock, err := os.OpenFile(filepath.Join(home, "auth.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", "", errors.New("codex: could not lock auth.json")
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return "", "", errors.New("codex: could not lock auth.json")
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	currentPath, err := filepath.EvalSymlinks(filepath.Join(home, "auth.json"))
	if err != nil {
		return "", "", errors.New("codex: could not read auth.json")
	}
	current, err := os.ReadFile(currentPath)
	if err != nil {
		return "", "", errors.New("codex: could not read auth.json")
	}
	if currentPath != path || !bytes.Equal(current, original) {
		return codexCredential(home, now)
	}
	if _, _, err := codexCredential(home, now); err != nil {
		return "", "", err
	}
	for key, value := range map[string]string{"access_token": renewed.Access, "refresh_token": renewed.Refresh, "id_token": renewed.ID} {
		if value != "" {
			tokens[key], _ = json.Marshal(value)
		}
	}
	document["tokens"], _ = json.Marshal(tokens)
	document["last_refresh"], _ = json.Marshal(now.UTC().Format(time.RFC3339Nano))
	updated, _ := json.MarshalIndent(document, "", "  ")
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return "", "", errors.New("codex: could not save refreshed login")
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(updated)
	if writeErr == nil {
		writeErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		return "", "", errors.New("codex: could not save refreshed login")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", "", errors.New("codex: could not save refreshed login")
	}
	return codexCredential(home, now)
}

type codexTokenClaims struct {
	Expiry int64 `json:"exp"`
	Auth   struct {
		Account string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// JWT 仅提取账号和提前识别过期；是否有效仍由 ChatGPT 验证，不据此改变请求目的地。
func codexClaims(token string) (claims codexTokenClaims) {
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if b, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			json.Unmarshal(b, &claims)
		}
	}
	return claims
}
