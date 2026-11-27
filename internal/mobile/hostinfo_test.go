package mobile

import (
	"context"
	"errors"
	"testing"

	"posse/internal/rpc"
)

type infoFakeHost struct {
	*fakeHost
	info         HostInfo
	infoErr      error
	mem          rpc.DaemonMemoryResult
	memErr       error
	lastExpanded []string
	setStatus    rpc.AutostartStatus
	setErr       error
	setCalled    int
	lastEnabled  bool
}

func (h *infoFakeHost) HostInfo(ctx context.Context) (HostInfo, error) {
	return h.info, h.infoErr
}

func (h *infoFakeHost) HostMemory(ctx context.Context, expanded []string) (rpc.DaemonMemoryResult, error) {
	h.lastExpanded = append([]string(nil), expanded...)
	return h.mem, h.memErr
}

func (h *infoFakeHost) SetHostAutostart(ctx context.Context, enabled bool) (rpc.AutostartStatus, error) {
	h.setCalled++
	h.lastEnabled = enabled
	return h.setStatus, h.setErr
}

func TestMobileHostInfoMemoryAndAutostartRPC(t *testing.T) {
	host := &infoFakeHost{
		fakeHost: &fakeHost{},
		info: HostInfo{
			Version:          "0.1.55",
			Commit:           "6e22247",
			Platform:         "linux",
			Arch:             "amd64",
			Edition:          "headless",
			Managed:          true,
			Pid:              1234,
			Home:             "/home/dev/.posse",
			Terminals:        3,
			RunningTerminals: 2,
			DaemonMemoryKB:   32768,
			Autostart: &rpc.AutostartStatus{
				Supported: true, Kind: "systemd", Managed: true, Writable: true,
				Installed: true, Enabled: true, Active: true, Linger: "no",
				NoteCode: rpc.AutostartNoteSystemdLingerDisabled,
			},
		},
		mem: rpc.DaemonMemoryResult{
			TotalKB: 128000,
			Workspaces: []rpc.DaemonMemoryWorkspace{{
				ID: "w1", Label: "posse/main", KB: 128000,
				Terminals: []rpc.DaemonMemoryTerminal{{ID: "t1", Title: "bash", KB: 128000}},
			}},
		},
		setStatus: rpc.AutostartStatus{
			Supported: true, Kind: "systemd", Managed: true, Writable: true,
			Installed: false, Enabled: false, Active: false,
		},
	}

	srv, err := New(t.TempDir(), host, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	pair, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	// 1. 未声明 posse.host.v1 时拒绝访问，返回 method_not_found。
	pNoCap := dialPhone(t, srv, pair)
	defer pNoCap.ws.CloseNow()
	if errCode := callExpectErr(pNoCap, "posse.host.info", map[string]any{}); errCode != "method_not_found" {
		t.Fatalf("expected method_not_found without posse.host.v1, got %q", errCode)
	}

	// 2. 声明 posse.host.v1 后正常返回节点详情、内存明细与自启开关。
	callExpectOK(pNoCap, "runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "locale": "zh-Hans",
		"capabilities": []string{"posse.host.v1"},
	})
	res := callExpectOK(pNoCap, "posse.host.info", map[string]any{})
	if res["platform"] != "linux" || res["edition"] != "headless" || res["managed"] != true {
		t.Fatalf("unexpected posse.host.info: %+v", res)
	}

	memRes := callExpectOK(pNoCap, "posse.host.memory", map[string]any{"expanded": []string{"t1"}})
	if memRes["totalKB"].(float64) != 128000 || len(host.lastExpanded) != 1 || host.lastExpanded[0] != "t1" {
		t.Fatalf("unexpected posse.host.memory: res=%+v expanded=%v", memRes, host.lastExpanded)
	}

	// 缺 enabled 字段、null、未知字段、大小写别名、重复键均须拒绝，绝不当作 false 关闭自启。
	if errCode, _ := callExpectErrDetail(pNoCap, "posse.host.autostartSet", map[string]any{}); errCode != "bad_params" {
		t.Fatalf("expected bad_params for empty autostartSet, got %q", errCode)
	}
	if errCode, _ := callExpectErrDetail(pNoCap, "posse.host.autostartSet", map[string]any{"enabled": nil}); errCode != "bad_params" {
		t.Fatalf("expected bad_params for null enabled on autostartSet, got %q", errCode)
	}
	if errCode, _ := callExpectErrDetail(pNoCap, "posse.host.info", map[string]any{"extra": 1}); errCode != "bad_params" {
		t.Fatalf("expected bad_params for unknown field on host.info, got %q", errCode)
	}
	if errCode, _ := callExpectErrDetail(pNoCap, "posse.host.autostartSet", map[string]any{"Enabled": true}); errCode != "bad_params" {
		t.Fatalf("expected bad_params for case alias on autostartSet, got %q", errCode)
	}
	if host.setCalled != 0 {
		t.Fatalf("invalid autostartSet should not call host, setCalled=%d", host.setCalled)
	}

	// 合法关自启
	setRes := callExpectOK(pNoCap, "posse.host.autostartSet", map[string]any{"enabled": false})
	if host.setCalled != 1 || host.lastEnabled != false || setRes["enabled"] != false {
		t.Fatalf("unexpected autostartSet result: called=%d enabled=%v res=%+v", host.setCalled, host.lastEnabled, setRes)
	}

	// 错误应返回明确错误码并按客户端 locale（zh-Hans）翻译。
	host.setErr = rpc.Errorf(rpc.CodeBadRequest, "autostart can only be managed on managed headless servers")
	errCode, errMsg := callExpectErrDetail(pNoCap, "posse.host.autostartSet", map[string]any{"enabled": true})
	if errCode != rpc.CodeBadRequest || errMsg == "" || errMsg == "autostart can only be managed on managed headless servers" {
		t.Fatalf("expected translated Chinese bad_request error, got code=%q msg=%q", errCode, errMsg)
	}
	host.setErr = errors.New("boom")

	// 严格解析单元测试：重复键拒绝
	if _, ok := parseStrictObject([]byte(`{"enabled":true,"enabled":false}`), map[string]bool{"enabled": true}); ok {
		t.Fatal("expected duplicate key in strict JSON object to be rejected")
	}
}

func callExpectOK(p *phone, method string, params any) map[string]any {
	p.t.Helper()
	msg := p.next(p.call(method, params))
	if msg["error"] != nil {
		p.t.Fatalf("%s failed: %+v", method, msg["error"])
	}
	res, _ := msg["result"].(map[string]any)
	return res
}

func callExpectErr(p *phone, method string, params any) string {
	code, _ := callExpectErrDetail(p, method, params)
	return code
}

func callExpectErrDetail(p *phone, method string, params any) (string, string) {
	p.t.Helper()
	msg := p.next(p.call(method, params))
	if errObj, ok := msg["error"].(map[string]any); ok {
		code, _ := errObj["code"].(string)
		text, _ := errObj["message"].(string)
		if code != "" {
			return code, text
		}
	}
	p.t.Fatalf("%s expected error, got %+v", method, msg)
	return "", ""
}
