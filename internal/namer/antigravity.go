package namer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

// agy 把会话服务地址和 CSRF token 交给工具命令。只直连回环地址，避免 token 经代理或重定向泄露。
var antigravityTitleClient = &http.Client{
	Transport:     &http.Transport{DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       3 * time.Second,
}

// antigravityUpdatePaths 按新旧顺序尝试：agy 1.5.16 起去掉了路径前缀 jetski.，404 时回退旧路径兼容旧版。
var antigravityUpdatePaths = []string{
	"/product.v1.ConversationService/UpdateConversation",
	"/jetski.product.v1.ConversationService/UpdateConversation",
}

func WriteAntigravity(ctx context.Context, address, token, sessionID, title string) error {
	host, portText, err := net.SplitHostPort(address)
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip, _ := netip.ParseAddr(host)
	port, portErr := strconv.ParseUint(portText, 10, 16)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" || portErr != nil || port == 0 {
		return errors.New("invalid Antigravity session service address")
	}
	type annotations struct {
		Title string `json:"title"`
	}
	body, _ := json.Marshal(struct {
		ConversationID string      `json:"conversationId"`
		Annotations    annotations `json:"annotations"`
	}{sessionID, annotations{Title: title}})
	base := "http://" + net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10))
	for i, path := range antigravityUpdatePaths {
		status, err := postAntigravityTitle(ctx, base+path, token, body, sessionID, title)
		if err == nil {
			return nil
		}
		if status == http.StatusNotFound && i+1 < len(antigravityUpdatePaths) {
			continue
		}
		return err
	}
	return nil
}

func postAntigravityTitle(ctx context.Context, endpoint, token string, body []byte, sessionID, title string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("X-Antigravity-CSRF-Token", token)
	resp, err := antigravityTitleClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("call Antigravity session service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("Antigravity session service returned HTTP %d", resp.StatusCode)
	}
	var updated struct {
		ID          string `json:"id"`
		Annotations struct {
			Title string `json:"title"`
		} `json:"annotations"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&updated) != nil ||
		updated.ID != sessionID || updated.Annotations.Title != title {
		return resp.StatusCode, errors.New("Antigravity did not confirm the new session title")
	}
	return resp.StatusCode, nil
}
