package lark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MessageEvent 对应 `lark-cli event consume im.message.receive_v1 --as bot` 的扁平输出。
type MessageEvent struct {
	EventID     string           `json:"event_id"`
	MessageID   string           `json:"message_id"`
	ID          string           `json:"id"`
	ChatID      string           `json:"chat_id"`
	ChatType    string           `json:"chat_type"` // p2p | group
	RootID      string           `json:"root_id"`
	ParentID    string           `json:"parent_id"`
	ThreadID    string           `json:"thread_id"`
	SenderID    string           `json:"sender_id"`
	SenderType  string           `json:"sender_type"`
	MessageType string           `json:"message_type"`
	Content     string           `json:"content"`
	CreateTime  string           `json:"create_time"`
	Mentions    []MessageMention `json:"mentions"`
	RawContent  json.RawMessage  `json:"raw_content,omitempty"`
}

func (m MessageEvent) MsgID() string {
	if m.MessageID != "" {
		return m.MessageID
	}
	return m.ID
}

type MessageMention struct {
	Key    string `json:"key"`
	ID     string `json:"id"`
	OpenID string `json:"open_id"`
	Name   string `json:"name"`
}

// BotIdentity 是从 `lark-cli auth status` 读取到的当前机器人自身 OpenID 与飞书应用显示名，
// 用于在开通全群消息权限的群里精确识别「是否真的 @ 了本机器人」。
type BotIdentity struct {
	BotOpenID  string
	BotAppName string
}

type botIdentityProvider interface {
	BotIdentity(ctx context.Context, profile string) (BotIdentity, error)
}

// FetchedMessage 对应 `lark-cli im +messages-mget` / `+chat-messages-list` 返回的单条消息。
type FetchedMessage struct {
	MessageID  string `json:"message_id"`
	MsgType    string `json:"msg_type"`
	CreateTime string `json:"create_time"`
	Content    string `json:"content"`
	Deleted    bool   `json:"deleted"`
	Sender     struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		SenderType string `json:"sender_type"`
	} `json:"sender"`
	Resources []struct {
		Key       string `json:"key"`
		Type      string `json:"type"`
		LocalPath string `json:"local_path"`
		Error     bool   `json:"error"`
	} `json:"resources"`
}

type messageContextFetcher interface {
	GetMessage(ctx context.Context, profile, messageID, downloadDir string) (FetchedMessage, error)
	ListRecentMessages(ctx context.Context, profile, chatID string, limit int) ([]FetchedMessage, error)
}

// CardActionEvent 对应 `lark-cli event consume card.action.trigger --as bot` 的扁平输出。
type CardActionEvent struct {
	EventID     string `json:"event_id"`
	Timestamp   string `json:"timestamp"`
	OperatorID  string `json:"operator_id"`
	MessageID   string `json:"message_id"`
	ChatID      string `json:"chat_id"`
	Token       string `json:"token"`
	ActionTag   string `json:"action_tag"`
	ActionName  string `json:"action_name"`
	ActionValue string `json:"action_value"`
	FormValue   string `json:"form_value"`
	InputValue  string `json:"input_value"`
	Option      string `json:"option"`
}

// Client 抽象与本机 lark-cli 的交互，方便单测注入假实现。
type Client interface {
	Profiles(ctx context.Context) ([]ProfileInfo, error)
	OwnerOpenID(ctx context.Context, profile string) (string, error)
	Consume(ctx context.Context, profile, eventKey string, onReady func(), onLine func([]byte)) error
	SendCard(ctx context.Context, profile, chatID, cardJSON string) (string, error)
	ReplyCard(ctx context.Context, profile, messageID string, inThread bool, cardJSON string) (string, error)
	PatchCard(ctx context.Context, profile, cardMsgID, cardJSON string) error
	SendFile(ctx context.Context, profile, chatID, filePath string) error
	DownloadResource(ctx context.Context, profile, messageID, fileKey, kind, outDir string) (string, error)
}

// CLIClient 通过本机 `lark-cli` 执行长连接消费和消息/卡片接口。
type CLIClient struct {
	Bin string // 可选；为空时按 PATH 与常见安装目录查找 lark-cli
}

func (c *CLIClient) binary() (string, error) {
	if c.Bin != "" {
		return c.Bin, nil
	}
	if p, err := exec.LookPath("lark-cli"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/opt/homebrew/bin/lark-cli",
		"/usr/local/bin/lark-cli",
	}
	if home != "" {
		if matches, err := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "lark-cli")); err == nil {
			for i := len(matches) - 1; i >= 0; i-- {
				candidates = append([]string{matches[i]}, candidates...)
			}
		}
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "lark-cli"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("lark-cli not found in PATH")
}

func cliEnv(bin string) []string {
	binDir := filepath.Dir(bin)
	extra := []string{binDir, "/opt/homebrew/bin", "/usr/local/bin"}
	base := os.Environ()
	out := make([]string, 0, len(base)+2)
	var curPath string
	for _, kv := range base {
		if strings.HasPrefix(kv, "PATH=") {
			curPath = strings.TrimPrefix(kv, "PATH=")
			continue
		}
		if strings.HasPrefix(kv, "LARK_CLI_NO_PROXY_WARN=") {
			continue
		}
		out = append(out, kv)
	}
	parts := make([]string, 0, len(extra)+1)
	seen := map[string]bool{}
	for _, p := range extra {
		if p != "" && p != "." && !seen[p] {
			seen[p] = true
			parts = append(parts, p)
		}
	}
	if curPath != "" {
		for _, p := range strings.Split(curPath, string(os.PathListSeparator)) {
			if p != "" && !seen[p] {
				seen[p] = true
				parts = append(parts, p)
			}
		}
	}
	out = append(out, "PATH="+strings.Join(parts, string(os.PathListSeparator)))
	out = append(out, "LARK_CLI_NO_PROXY_WARN=1")
	return out
}

func (c *CLIClient) run(ctx context.Context, profile, dir string, stdin []byte, args ...string) ([]byte, error) {
	bin, err := c.binary()
	if err != nil {
		return nil, err
	}
	full := make([]string, 0, len(args)+2)
	if profile != "" {
		full = append(full, "--profile", profile)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = cliEnv(bin)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("lark-cli %s: %s", strings.Join(args[:min(2, len(args))], " "), msg)
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

func (c *CLIClient) Profiles(ctx context.Context) ([]ProfileInfo, error) {
	out, err := c.run(ctx, "", "", nil, "profile", "list")
	if err != nil {
		return nil, err
	}
	var list []ProfileInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &list); err != nil {
		return nil, fmt.Errorf("parse lark-cli profile list: %w", err)
	}
	return list, nil
}

func (c *CLIClient) OwnerOpenID(ctx context.Context, profile string) (string, error) {
	out, err := c.run(ctx, profile, "", nil, "auth", "status")
	if err != nil {
		return "", err
	}
	var st struct {
		Identities struct {
			User struct {
				OpenID string `json:"openId"`
			} `json:"user"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &st); err != nil {
		return "", err
	}
	return strings.TrimSpace(st.Identities.User.OpenID), nil
}

func (c *CLIClient) BotIdentity(ctx context.Context, profile string) (BotIdentity, error) {
	out, err := c.run(ctx, profile, "", nil, "auth", "status")
	if err != nil {
		return BotIdentity{}, err
	}
	var st struct {
		Identities struct {
			Bot struct {
				OpenID  string `json:"openId"`
				AppName string `json:"appName"`
			} `json:"bot"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &st); err != nil {
		return BotIdentity{}, err
	}
	return BotIdentity{
		BotOpenID:  strings.TrimSpace(st.Identities.Bot.OpenID),
		BotAppName: strings.TrimSpace(st.Identities.Bot.AppName),
	}, nil
}

// Consume 启动一个 `lark-cli --profile <p> event consume <eventKey> --as bot` 长连接子进程，
// 等待 stderr 出现 `[event] ready` 标记后逐行分发 stdout NDJSON；ctx 取消时关闭 stdin 优雅退出（不 kill -9）。
func (c *CLIClient) Consume(ctx context.Context, profile, eventKey string, onReady func(), onLine func([]byte)) error {
	bin, err := c.binary()
	if err != nil {
		return err
	}
	args := []string{}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	args = append(args, "event", "consume", eventKey, "--as", "bot")
	cmd := exec.Command(bin, args...)
	cmd.Env = cliEnv(bin)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		stdinPipe.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		return err
	}

	// ctx 取消时优先关闭 stdin（lark-cli 的优雅退出契约），超时再发 SIGTERM
	stopDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = stdinPipe.Close()
			select {
			case <-stopDone:
			case <-time.After(3 * time.Second):
				if cmd.Process != nil {
					_ = cmd.Process.Signal(syscall.SIGTERM)
				}
			}
		case <-stopDone:
			_ = stdinPipe.Close()
		}
	}()

	var stderrMu sync.Mutex
	var stderrLines []string
	var stderrBytes int
	readyOnce := sync.Once{}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rd := bufio.NewReaderSize(stderrPipe, 16*1024)
		for {
			line, eof := readCappedLine(rd, 16*1024)
			if line != "" {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "[event] ready") {
					readyOnce.Do(func() {
						stderrMu.Lock()
						stderrLines = nil
						stderrBytes = 0
						stderrMu.Unlock()
						if onReady != nil {
							onReady()
						}
					})
				} else if line != "" && !strings.HasPrefix(line, "[event] ") {
					stderrMu.Lock()
					stderrLines = append(stderrLines, line)
					stderrBytes += len(line) + 1
					for len(stderrLines) > 200 || (stderrBytes > 64*1024 && len(stderrLines) > 1) {
						stderrBytes -= len(stderrLines[0]) + 1
						stderrLines = stderrLines[1:]
					}
					stderrMu.Unlock()
				}
			}
			if eof {
				return
			}
		}
	}()

	sc := bufio.NewScanner(stdoutPipe)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		cp := append([]byte(nil), raw...)
		if onLine != nil {
			onLine(cp)
		}
	}
	wg.Wait()
	waitErr := cmd.Wait()
	close(stopDone)
	if ctx.Err() != nil {
		return nil
	}
	stderrMu.Lock()
	msg := parseConsumeStderr(stderrLines)
	stderrMu.Unlock()
	if waitErr != nil {
		if msg != "" {
			return errors.New(msg)
		}
		return waitErr
	}
	if msg != "" {
		return errors.New(msg)
	}
	return io.EOF
}

// readCappedLine 读取一行文本，超长部分截断但持续排空至换行符，避免 stderr 超长行堵死子进程管道。
func readCappedLine(rd *bufio.Reader, maxBytes int) (string, bool) {
	var buf []byte
	for {
		frag, err := rd.ReadSlice('\n')
		if len(frag) > 0 && len(buf) < maxBytes {
			remain := maxBytes - len(buf)
			if len(frag) > remain {
				buf = append(buf, frag[:remain]...)
			} else {
				buf = append(buf, frag...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return string(buf), err != nil
	}
}

// parseConsumeStderr 从 lark-cli event consume 的 stderr 输出中提取错误原因，
// 兼容单行与多行缩进 JSON 错误信封；若无 JSON 信封则退回非告警文本行。
func parseConsumeStderr(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var filtered []string
	for _, l := range lines {
		if strings.HasPrefix(l, "[lark-cli] [WARN]") || strings.HasPrefix(l, "[event] ") {
			continue
		}
		filtered = append(filtered, l)
	}
	if len(filtered) == 0 {
		return ""
	}
	joined := strings.Join(filtered, "\n")
	var lastJSONErr string
	for off := 0; off < len(joined); {
		idx := strings.IndexByte(joined[off:], '{')
		if idx < 0 {
			break
		}
		off += idx
		var env struct {
			OK    *bool `json:"ok"`
			Error struct {
				Message string `json:"message"`
				Hint    string `json:"hint"`
			} `json:"error"`
		}
		dec := json.NewDecoder(strings.NewReader(joined[off:]))
		if err := dec.Decode(&env); err == nil {
			if env.OK != nil && !*env.OK && strings.TrimSpace(env.Error.Message) != "" {
				msg := strings.TrimSpace(env.Error.Message)
				if hint := strings.TrimSpace(env.Error.Hint); hint != "" {
					msg += " (" + hint + ")"
				}
				lastJSONErr = msg
			}
			consumed := int(dec.InputOffset())
			if consumed <= 0 {
				off++
			} else {
				off += consumed
			}
			continue
		}
		off++
	}
	if lastJSONErr != "" {
		return lastJSONErr
	}
	msg := strings.Join(filtered, "; ")
	if len(msg) > 300 {
		msg = truncateUTF8(msg, 300) + "..."
	}
	return msg
}

func parseLarkMessageID(out []byte) (string, error) {
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &env); err != nil {
		return "", fmt.Errorf("parse lark message response: %w", err)
	}
	if !env.OK {
		if env.Error.Message != "" {
			return "", errors.New(env.Error.Message)
		}
		return "", errors.New("lark message API returned ok=false")
	}
	return env.Data.MessageID, nil
}

func (c *CLIClient) SendCard(ctx context.Context, profile, chatID, cardJSON string) (string, error) {
	out, err := c.run(ctx, profile, "", []byte(cardJSON),
		"im", "+messages-send", "--as", "bot", "--chat-id", chatID, "--msg-type", "interactive", "--content", "-")
	if err != nil {
		return "", err
	}
	return parseLarkMessageID(out)
}

func (c *CLIClient) ReplyCard(ctx context.Context, profile, messageID string, inThread bool, cardJSON string) (string, error) {
	args := []string{"im", "+messages-reply", "--as", "bot", "--message-id", messageID, "--msg-type", "interactive", "--content", "-"}
	if inThread {
		args = append(args, "--reply-in-thread")
	}
	out, err := c.run(ctx, profile, "", []byte(cardJSON), args...)
	if err != nil {
		return "", err
	}
	return parseLarkMessageID(out)
}

func (c *CLIClient) PatchCard(ctx context.Context, profile, cardMsgID, cardJSON string) error {
	body, err := json.Marshal(map[string]string{"content": cardJSON})
	if err != nil {
		return err
	}
	out, err := c.run(ctx, profile, "", body,
		"im", "messages", "patch", "--as", "bot", "--message-id", cardMsgID, "--data", "-")
	if err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil
	}
	var env struct {
		OK    *bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return fmt.Errorf("parse lark patch card response: %w", err)
	}
	if env.OK != nil && !*env.OK {
		if msg := strings.TrimSpace(env.Error.Message); msg != "" {
			return errors.New(msg)
		}
		return errors.New("lark patch card API returned ok=false")
	}
	return nil
}

func (c *CLIClient) SendFile(ctx context.Context, profile, chatID, filePath string) error {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	_, err := c.run(ctx, profile, dir, nil,
		"im", "+messages-send", "--as", "bot", "--chat-id", chatID, "--file", base)
	return err
}

func (c *CLIClient) DownloadResource(ctx context.Context, profile, messageID, fileKey, kind, outDir string) (string, error) {
	if kind != "image" {
		kind = "file"
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(fileKey)
	if name == "" || name == "." || name == "/" {
		name = "resource"
	}
	out, err := c.run(ctx, profile, outDir, nil,
		"im", "+messages-resources-download", "--as", "bot",
		"--message-id", messageID, "--file-key", fileKey, "--type", kind, "--output", name)
	if err != nil {
		return "", err
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Path string `json:"path"`
			File string `json:"file"`
		} `json:"data"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &env) == nil && env.OK {
		if p := env.Data.Path; p != "" {
			if filepath.IsAbs(p) {
				return p, nil
			}
			return filepath.Join(outDir, p), nil
		}
		if p := env.Data.File; p != "" {
			if filepath.IsAbs(p) {
				return p, nil
			}
			return filepath.Join(outDir, p), nil
		}
	}
	// lark-cli 可能按 Content-Type 补了后缀，扫描目录找匹配文件
	matches, _ := filepath.Glob(filepath.Join(outDir, name+"*"))
	if len(matches) > 0 {
		return matches[0], nil
	}
	return filepath.Join(outDir, name), nil
}

func (c *CLIClient) GetMessage(ctx context.Context, profile, messageID, downloadDir string) (FetchedMessage, error) {
	if messageID == "" {
		return FetchedMessage{}, errors.New("message ID is required")
	}
	args := []string{"im", "+messages-mget", "--as", "bot", "--message-ids", messageID, "--no-reactions", "--format", "json"}
	if downloadDir != "" {
		if err := os.MkdirAll(downloadDir, 0o700); err != nil {
			return FetchedMessage{}, err
		}
		args = append(args, "--download-resources")
	}
	out, err := c.run(ctx, profile, downloadDir, nil, args...)
	if err != nil {
		return FetchedMessage{}, err
	}
	msgs, err := parseFetchedMessages(out)
	if err != nil {
		return FetchedMessage{}, err
	}
	if len(msgs) == 0 {
		return FetchedMessage{}, errors.New("message not found")
	}
	msg := msgs[0]
	if downloadDir != "" {
		for i := range msg.Resources {
			if p := strings.TrimSpace(msg.Resources[i].LocalPath); p != "" && !filepath.IsAbs(p) {
				msg.Resources[i].LocalPath = filepath.Join(downloadDir, p)
			}
		}
	}
	return msg, nil
}

func (c *CLIClient) ListRecentMessages(ctx context.Context, profile, chatID string, limit int) ([]FetchedMessage, error) {
	if chatID == "" || limit <= 0 {
		return nil, nil
	}
	if limit > 50 {
		limit = 50
	}
	out, err := c.run(ctx, profile, "", nil,
		"im", "+chat-messages-list", "--as", "bot",
		"--chat-id", chatID, "--order", "desc",
		"--page-size", fmt.Sprintf("%d", limit),
		"--no-reactions", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseFetchedMessages(out)
}

func (c *CLIClient) ListRecentThreadMessages(ctx context.Context, profile, threadID string, limit int) ([]FetchedMessage, error) {
	if threadID == "" || limit <= 0 {
		return nil, nil
	}
	if limit > 50 {
		limit = 50
	}
	out, err := c.run(ctx, profile, "", nil,
		"im", "+threads-messages-list", "--as", "bot",
		"--thread", threadID, "--order", "desc",
		"--page-size", fmt.Sprintf("%d", limit),
		"--no-reactions", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseFetchedMessages(out)
}

func parseFetchedMessages(out []byte) ([]FetchedMessage, error) {
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Messages []FetchedMessage `json:"messages"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &env); err != nil {
		return nil, fmt.Errorf("parse lark messages response: %w", err)
	}
	if !env.OK {
		if env.Error.Message != "" {
			return nil, errors.New(env.Error.Message)
		}
		return nil, errors.New("lark messages API returned ok=false")
	}
	return env.Data.Messages, nil
}
