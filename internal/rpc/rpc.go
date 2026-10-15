// Package rpc 是 CLI 与常驻进程之间的协议：unix socket 上每个连接一次请求，
// 请求和响应各占一行 JSON。
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error 带一个稳定的 code，供调用方（包括 agent）按类型处理。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

const (
	CodeBadRequest = "bad_request"
	CodeNotFound   = "not_found"
	CodeConflict   = "conflict"
	CodeTimeout    = "timeout"
	CodeInternal   = "internal"
	CodeUnknown    = "unknown_method"
)

func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Handler 的 ctx 在客户端断开时取消。
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// StreamHandler 接管整个连接：先用 WriteResult/WriteError 回复一行，之后双方改用帧通信。
// ctx 在常驻进程退出时取消；客户端断开表现为读写出错。
type StreamHandler func(ctx context.Context, params json.RawMessage, conn net.Conn, r *bufio.Reader) error

type Server struct {
	handlers map[string]Handler
	streams  map[string]StreamHandler
}

func NewServer() *Server {
	return &Server{handlers: map[string]Handler{}, streams: map[string]StreamHandler{}}
}

func (s *Server) Handle(method string, h Handler) { s.handlers[method] = h }

func (s *Server) HandleStream(method string, h StreamHandler) { s.streams[method] = h }

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, conn)
	}
}

func (s *Server) serveConn(parent context.Context, conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, nil, Errorf(CodeBadRequest, "invalid request: %v", err))
		return
	}
	if sh, ok := s.streams[req.Method]; ok {
		if err := sh(parent, req.Params, conn, r); err != nil {
			writeResponse(conn, nil, err)
		}
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// 客户端一次连接只发一个请求，之后再读到 EOF 就说明它已断开
	go func() {
		r.ReadByte()
		cancel()
	}()
	h, ok := s.handlers[req.Method]
	if !ok {
		writeResponse(conn, nil, Errorf(CodeUnknown, "unknown method %q", req.Method))
		return
	}
	result, err := h(ctx, req.Params)
	writeResponse(conn, result, err)
}

func writeResponse(conn net.Conn, result any, err error) {
	var resp Response
	if err != nil {
		var re *Error
		if !errors.As(err, &re) {
			re = &Error{Code: CodeInternal, Message: err.Error()}
		}
		resp.Error = re
	} else if result != nil {
		b, merr := json.Marshal(result)
		if merr != nil {
			resp.Error = &Error{Code: CodeInternal, Message: merr.Error()}
		} else {
			resp.Result = b
		}
	}
	b, _ := json.Marshal(resp)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	conn.Write(append(b, '\n'))
}

// WriteResult 是流式处理器在切换到帧之前的成功回复。
func WriteResult(conn net.Conn, result any) { writeResponse(conn, result, nil) }

// Open 发起流式请求：读完回复行后返回连接，之后用帧通信。
func Open(ctx context.Context, socket, method string, params, out any) (net.Conn, *bufio.Reader, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, nil, err
	}
	req := Request{Method: method}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			conn.Close()
			return nil, nil, err
		}
	}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		conn.Close()
		return nil, nil, err
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		conn.Close()
		return nil, nil, resp.Error
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			conn.Close()
			return nil, nil, err
		}
	}
	return conn, r, nil
}

// Call 连接 socket 发起一次请求；out 为 nil 时丢弃结果。
func Call(ctx context.Context, socket, method string, params, out any) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req := Request{Method: method}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 64<<10).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Decode 把请求参数解到 v；空参数视为 {}。
func Decode(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return Errorf(CodeBadRequest, "invalid params: %v", err)
	}
	return nil
}
