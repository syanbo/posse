package remote

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Prompt struct {
	Text    string `json:"text"`
	Confirm bool   `json:"confirm"`
	Notify  bool   `json:"notify"`
}

// PromptFunc 在用户取消或 ctx 结束时返回错误；回答只通过本次连接的私有 socket 传递。
type PromptFunc func(context.Context, Prompt) (string, error)

type authRequest struct {
	Token  string `json:"token"`
	Prompt Prompt `json:"prompt"`
}
type authResponse struct {
	Answer string `json:"answer"`
	OK     bool   `json:"ok"`
}

type askpass struct {
	ln                    net.Listener
	cancel                context.CancelFunc
	wg                    sync.WaitGroup
	socket, token, script string
}

func newAskpass(ctx context.Context, dir, exe string, prompt PromptFunc) (*askpass, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	a := &askpass{cancel: cancel, socket: filepath.Join(dir, "a"), token: hex.EncodeToString(secret[:]), script: filepath.Join(dir, "askpass")}
	ln, err := net.Listen("unix", a.socket)
	if err != nil {
		cancel()
		return nil, err
	}
	a.ln = ln
	// wrapper 只有程序路径；凭据和通道令牌都不写进文件。
	if err := os.WriteFile(a.script, []byte("#!/bin/sh\nexec "+shellQuote(exe)+" remote askpass \"$@\"\n"), 0700); err != nil {
		ln.Close()
		cancel()
		return nil, err
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			a.wg.Add(1)
			go func() {
				defer a.wg.Done()
				defer conn.Close()
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				stop := context.AfterFunc(requestCtx, func() { conn.Close() })
				defer stop()
				conn.SetDeadline(time.Now().Add(5 * time.Minute))
				var req authRequest
				if json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&req) != nil || subtle.ConstantTimeCompare([]byte(req.Token), []byte(a.token)) != 1 {
					return
				}
				// SSH 会提前终止安全密钥通知 helper，关闭连接时也要取消对应提示。
				go func() { io.Copy(io.Discard, conn); cancel() }()
				var response authResponse
				if prompt != nil {
					answer, err := prompt(requestCtx, req.Prompt)
					response = authResponse{Answer: answer, OK: err == nil}
				}
				json.NewEncoder(conn).Encode(response)
			}()
		}
	}()
	return a, nil
}

func (a *askpass) close() { a.cancel(); a.ln.Close(); a.wg.Wait() }

// AnswerPrompt 仅供 SSH_ASKPASS helper 使用；失败不回显提示、令牌或回答。
func AnswerPrompt(ctx context.Context, text string) (string, error) {
	socket, token := os.Getenv("POSSE_ASKPASS_SOCKET"), os.Getenv("POSSE_ASKPASS_TOKEN")
	if socket == "" || token == "" {
		return "", errors.New("SSH authentication channel unavailable")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return "", errors.New("SSH authentication channel unavailable")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(5 * time.Minute))
	req := authRequest{Token: token, Prompt: Prompt{Text: text, Confirm: os.Getenv("SSH_ASKPASS_PROMPT") == "confirm", Notify: os.Getenv("SSH_ASKPASS_PROMPT") == "none"}}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return "", errors.New("SSH authentication cancelled")
	}
	var res authResponse
	if json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&res) != nil || !res.OK {
		return "", errors.New("SSH authentication cancelled")
	}
	return res.Answer, nil
}

func shellQuote(s string) string {
	var out []byte
	out = append(out, '\'')
	for _, c := range []byte(s) {
		if c == '\'' {
			out = append(out, []byte("'\"'\"'")...)
		} else {
			out = append(out, c)
		}
	}
	return string(append(out, '\''))
}
