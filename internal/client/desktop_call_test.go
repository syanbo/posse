package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"posse/internal/rpc"
)

func TestDesktopCallLostResponseIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	request := make(chan rpc.DesktopCall, 1)
	socket := fakeAttach(t, func(conn net.Conn) {
		defer conn.Close()
		calls.Add(1)
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var req rpc.Request
		json.Unmarshal(line, &req)
		if req.Method != "desktop.call" {
			t.Errorf("unguarded method %s", req.Method)
		}
		var p rpc.DesktopCall
		json.Unmarshal(req.Params, &p)
		request <- p
	})
	id := rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 3}
	c := (&Client{Socket: socket, Remote: true}).WithDesktop(id)
	err := c.Call(context.Background(), "project.add", map[string]string{"path": "/remote"}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown || rpc.Retryable(err) {
		t.Fatalf("lost response offered replay: %v", err)
	}
	p := <-request
	if p.Desktop != id || p.Sequence == 0 || p.Method != "project.add" || string(p.Params) != `{"path":"/remote"}` || calls.Load() != 1 {
		t.Fatalf("incorrect request: %+v calls=%d", p, calls.Load())
	}
	var ref rpc.DesktopOperationRef
	if err := json.Unmarshal(re.Data, &ref); err != nil || ref.Desktop != id || ref.Sequence != p.Sequence {
		t.Fatalf("lost source identity: %+v %v", ref, err)
	}
}

func TestDesktopConnectionCannotUseIndependentWritePath(t *testing.T) {
	c := (&Client{Socket: "/missing", Remote: true}).WithDesktop(rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 3})
	if _, _, err := c.Open(context.Background(), "terminal.send", map[string]string{"text": "input"}, nil); err == nil {
		t.Fatal("desktop opened independent write method")
	} else {
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeUnknown {
			t.Fatalf("request reached socket: %v", err)
		}
	}
	plain := &Client{Socket: "/missing", Remote: true}
	for _, method := range []string{"project.add", "terminal.send", "project.list"} {
		err := plain.Call(context.Background(), method, nil, nil)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
			t.Fatalf("remote client without identity used %s: %v", method, err)
		}
	}
}
