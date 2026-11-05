package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/rpc"
)

// 加入的讨论里加成员，房主没回应（结果未知）：CLI 说明可能已经加上了，给出用同一个请求 ID 重试的参数；
// 带 --client-id 时原样交给常驻进程。
func TestDiscussionAddMemberUnknownResult(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "pa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	sock := filepath.Join(home, "possed.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	s := rpc.NewServer()
	s.Handle("discussion.member.add", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct{ ClientID string }
		json.Unmarshal(raw, &p)
		got = append(got, p.ClientID)
		e := rpc.Errorf(rpc.CodeConflict, "the host did not answer (EOF); it may or may not have done this")
		e.Data = json.RawMessage(`{"unknown":true,"clientId":"c-7"}`)
		return nil, e
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx, ln)
	var errOut bytes.Buffer
	e := &env{home: home, socket: sock, out: &bytes.Buffer{}, errOut: &errOut}
	if err := cmdDiscussionAddMember(e, []string{"fp/d_1", "--name", "Bob", "--agent", "codex"}); err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(errOut.String(), "--client-id c-7") {
		t.Fatalf("no retry hint: %q", errOut.String())
	}
	if err := cmdDiscussionAddMember(e, []string{"fp/d_1", "--name", "Bob", "--agent", "codex", "--client-id", "c-7"}); err == nil {
		t.Fatal("no error")
	}
	if len(got) != 2 || got[0] != "" || got[1] != "c-7" {
		t.Fatalf("client ids sent %q", got)
	}
}
