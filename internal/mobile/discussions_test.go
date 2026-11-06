package mobile

import (
	"context"
	"encoding/json"
	"testing"

	"posse/internal/rpc"
)

type discTestHost struct{ fakeHost }

func (h *discTestHost) Discussions(ctx context.Context, device, method string, p json.RawMessage) (any, error) {
	return nil, &rpc.Error{Code: "conflict", Message: "unknown result", Data: json.RawMessage(`{"unknown":true,"clientId":"retry-id"}`)}
}
func TestPhoneDiscussionCapabilityAndErrorData(t *testing.T) {
	srv, err := New(t.TempDir(), &discTestHost{}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	id := p.call("discussion.list", map[string]any{})
	res := p.next(id)
	if res["error"].(map[string]any)["code"] != "method_not_found" {
		t.Fatal(res)
	}
	id = p.call("runtime.clientCapabilities.update", map[string]any{"capabilities": []string{"posse.discussions.v1"}})
	p.next(id)
	id = p.call("discussion.member.add", map[string]any{})
	res = p.next(id)
	e := res["error"].(map[string]any)
	data := e["data"].(map[string]any)
	if e["code"] != "conflict" || data["unknown"] != true || data["clientId"] != "retry-id" {
		t.Fatal(res)
	}
	id = p.call("discussion.read", map[string]any{})
	res = p.next(id)
	if res["ok"] == true {
		t.Fatal("worker method exposed")
	}
}
