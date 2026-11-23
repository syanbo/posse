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
	endpoint := "http://" + net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10)) +
		"/jetski.product.v1.ConversationService/UpdateConversation"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("X-Antigravity-CSRF-Token", token)
	resp, err := antigravityTitleClient.Do(req)
	if err != nil {
		return fmt.Errorf("call Antigravity session service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Antigravity session service returned HTTP %d", resp.StatusCode)
	}
	var updated struct {
		ID          string      `json:"id"`
		Annotations annotations `json:"annotations"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&updated) != nil ||
		updated.ID != sessionID || updated.Annotations.Title != title {
		return errors.New("Antigravity did not confirm the new session title")
	}
	return nil
}
