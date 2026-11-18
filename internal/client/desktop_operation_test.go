package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"posse/internal/rpc"
)

func operationTestClient(t *testing.T, handler func(rpc.Request) any) *Client {
	t.Helper()
	socket := fakeAttach(t, func(conn net.Conn) {
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var p rpc.Request
		if err := json.Unmarshal(line, &p); err != nil {
			t.Error(err)
			return
		}
		rpc.WriteResult(conn, handler(p))
	})
	return (&Client{Socket: socket, Remote: true}).WithDesktop(rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 3})
}

func TestDesktopOperationPreparedSnapshot(t *testing.T) {
	requests := make(chan rpc.DesktopCall, 4)
	c := operationTestClient(t, func(req rpc.Request) any {
		if req.Method != "desktop.call" {
			t.Errorf("method %s", req.Method)
		}
		var p rpc.DesktopCall
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Error(err)
		}
		requests <- p
		return map[string]bool{"ok": true}
	})
	original := c.WithDesktop(*c.Desktop)
	params := map[string]string{"path": "/original"}
	op, err := c.PrepareDesktopOperation("project.add", params)
	if err != nil {
		t.Fatal(err)
	}
	ref := op.Ref()
	if ref.Sequence == 0 || ref.Desktop != *original.Desktop {
		t.Fatalf("prepared reference %+v", ref)
	}
	select {
	case <-requests:
		t.Fatal("prepare sent request")
	default:
	}
	params["path"] = "/changed"
	c.Socket = "/missing"
	c.Desktop.ClientID = "changed"
	var result map[string]bool
	if err := op.Execute(t.Context(), &result); err != nil || !result["ok"] {
		t.Fatalf("snapshot execution %v %+v", err, result)
	}
	p := <-requests
	if p.Desktop != ref.Desktop || p.Sequence != ref.Sequence || p.Method != "project.add" || string(p.Params) != `{"path":"/original"}` {
		t.Fatalf("snapshot changed %+v", p)
	}
	if err := original.Call(t.Context(), "project.add", nil, nil); err != nil {
		t.Fatal(err)
	}
	legacy := <-requests
	next, err := original.PrepareDesktopOperation("project.add", nil)
	if err != nil || legacy.Sequence <= ref.Sequence || next.Ref().Sequence <= legacy.Sequence {
		t.Fatalf("sequence not shared: %d %d %d %v", ref.Sequence, legacy.Sequence, next.Ref().Sequence, err)
	}
}

func TestDesktopOperationExecuteOnce(t *testing.T) {
	var calls atomic.Int32
	c := operationTestClient(t, func(rpc.Request) any { calls.Add(1); return true })
	op, err := c.PrepareDesktopOperation("project.add", nil)
	if err != nil {
		t.Fatal(err)
	}
	copy := *op
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				results <- op.Execute(t.Context(), nil)
			} else {
				results <- copy.Execute(t.Context(), nil)
			}
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeConflict {
			t.Fatalf("duplicate error %v", err)
		}
	}
	if calls.Load() != 1 || succeeded != 1 {
		t.Fatalf("calls=%d successful=%d", calls.Load(), succeeded)
	}
	canceled, err := c.PrepareDesktopOperation("project.add", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := canceled.Execute(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled %v", err)
	}
	if err := canceled.Execute(t.Context(), nil); err == nil || calls.Load() != 1 {
		t.Fatalf("canceled request replayed calls=%d err=%v", calls.Load(), err)
	}
}

func TestDesktopOperationLostResponse(t *testing.T) {
	var calls atomic.Int32
	request := make(chan rpc.DesktopCall, 1)
	socket := fakeAttach(t, func(conn net.Conn) {
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		calls.Add(1)
		var req rpc.Request
		json.Unmarshal(line, &req)
		var p rpc.DesktopCall
		json.Unmarshal(req.Params, &p)
		request <- p
	})
	c := (&Client{Socket: socket}).WithDesktop(rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 1})
	op, err := c.PrepareDesktopOperation("project.add", nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := op.Ref()
	err = op.Execute(t.Context(), nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown || rpc.Retryable(err) {
		t.Fatalf("lost response %v", err)
	}
	var got rpc.DesktopOperationRef
	if json.Unmarshal(re.Data, &got) != nil || got != ref {
		t.Fatalf("lost reference %+v", got)
	}
	p := <-request
	if p.Sequence != ref.Sequence || p.Desktop != ref.Desktop {
		t.Fatalf("sent %+v ref=%+v", p, ref)
	}
	if err := op.Execute(t.Context(), nil); err == nil || calls.Load() != 1 {
		t.Fatalf("replayed calls=%d %v", calls.Load(), err)
	}
}

func TestDesktopOperationCancelLostResponse(t *testing.T) {
	var calls atomic.Int32
	request := make(chan rpc.Request, 1)
	socket := fakeAttach(t, func(conn net.Conn) {
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var req rpc.Request
		if err := json.Unmarshal(line, &req); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		request <- req
	})
	c := (&Client{Socket: socket}).WithDesktop(rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 3})
	op, err := c.PrepareDesktopOperation("project.add", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelDesktopOperation(t.Context(), op.Ref()); err == nil {
		t.Fatal("lost cancel response accepted")
	}
	p := <-request
	var cancel rpc.DesktopOperationCancel
	if p.Method != "desktop.operation.cancel" || json.Unmarshal(p.Params, &cancel) != nil || cancel.Operation != op.Ref() || calls.Load() != 1 {
		t.Fatalf("cancel replayed or changed: %+v calls=%d", p, calls.Load())
	}
	next, err := c.PrepareDesktopOperation("project.add", nil)
	if err != nil || next.Ref().Sequence != op.Ref().Sequence+1 {
		t.Fatal("lost cancellation allocated a write sequence")
	}
}

func TestDesktopOperationStatusCancel(t *testing.T) {
	old := rpc.DesktopOperationRef{Desktop: rpc.DesktopIdentity{ClientID: "old", InstanceID: "daemon", Epoch: 1}, Sequence: 42}
	var queries, cancels atomic.Int32
	c := operationTestClient(t, func(req rpc.Request) any {
		switch req.Method {
		case "desktop.operation":
			queries.Add(1)
			var p rpc.DesktopOperationRef
			if json.Unmarshal(req.Params, &p) != nil || p != old {
				t.Errorf("query %s", req.Params)
			}
		case "desktop.operation.cancel":
			cancels.Add(1)
			var p rpc.DesktopOperationCancel
			if json.Unmarshal(req.Params, &p) != nil || p.Operation != old || p.Desktop.ClientID != "window" || p.Desktop.Epoch != 3 {
				t.Errorf("cancel %s", req.Params)
			}
		default:
			t.Errorf("replayed method %s", req.Method)
		}
		return rpc.DesktopOperation{DesktopOperationRef: old, State: "unknown"}
	})
	before, err := c.PrepareDesktopOperation("project.add", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.DesktopOperationStatus(t.Context(), old); err != nil || out.State != "unknown" {
		t.Fatalf("status %+v %v", out, err)
	}
	if out, err := c.CancelDesktopOperation(t.Context(), old); err != nil || out.State != "unknown" {
		t.Fatalf("cancel before acceptance %+v %v", out, err)
	}
	after, err := c.PrepareDesktopOperation("project.add", nil)
	if err != nil || after.Ref().Sequence != before.Ref().Sequence+1 || queries.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("allocated/replayed sequence %v", err)
	}
}

func TestDesktopOperationRejectsResponse(t *testing.T) {
	ref := rpc.DesktopOperationRef{Desktop: rpc.DesktopIdentity{ClientID: "old", InstanceID: "daemon", Epoch: 1}, Sequence: 1}
	for _, method := range []string{"status", "cancel"} {
		for name, mutate := range map[string]func(*rpc.DesktopOperation){
			"reference":        func(o *rpc.DesktopOperation) { o.Sequence++ },
			"state":            func(o *rpc.DesktopOperation) { o.State = "canceled" },
			"method":           func(o *rpc.DesktopOperation) { o.Method = "daemon.stop" },
			"read-method":      func(o *rpc.DesktopOperation) { o.Method = "project.list" },
			"started":          func(o *rpc.DesktopOperation) { o.StartedAt = 0 },
			"running-result":   func(o *rpc.DesktopOperation) { o.Result = json.RawMessage(`true`) },
			"running-error":    func(o *rpc.DesktopOperation) { o.Error = rpc.Errorf(rpc.CodeInternal, "failure") },
			"running-finished": func(o *rpc.DesktopOperation) { o.FinishedAt = 2 },
			"done-empty":       func(o *rpc.DesktopOperation) { o.State = "done"; o.FinishedAt = 2 },
			"done-unfinished":  func(o *rpc.DesktopOperation) { o.State = "done"; o.Result = json.RawMessage(`true`) },
			"done-both": func(o *rpc.DesktopOperation) {
				o.State = "done"
				o.FinishedAt = 2
				o.Result = json.RawMessage(`true`)
				o.Error = rpc.Errorf(rpc.CodeInternal, "failure")
			},
			"unknown-metadata": func(o *rpc.DesktopOperation) { o.State = "unknown" },
		} {
			t.Run(method+"/"+name, func(t *testing.T) {
				out := rpc.DesktopOperation{DesktopOperationRef: ref, State: "running", Method: "project.add", StartedAt: 1}
				mutate(&out)
				var calls atomic.Int32
				c := operationTestClient(t, func(rpc.Request) any { calls.Add(1); return out })
				var err error
				if method == "status" {
					_, err = c.DesktopOperationStatus(t.Context(), ref)
				} else {
					_, err = c.CancelDesktopOperation(t.Context(), ref)
				}
				if err == nil || calls.Load() != 1 {
					t.Fatalf("accepted bad response calls=%d %v", calls.Load(), err)
				}
			})
		}
	}
	for _, out := range []rpc.DesktopOperation{
		{DesktopOperationRef: ref, State: "running", Method: "files.save", StartedAt: 1},
		{DesktopOperationRef: ref, State: "done", Method: "files.save", StartedAt: 1, FinishedAt: 2, Result: json.RawMessage(`null`)},
		{DesktopOperationRef: ref, State: "done", Method: "files.save", StartedAt: 1, FinishedAt: 2, Error: rpc.Errorf(rpc.CodeConflict, "changed")},
	} {
		c := operationTestClient(t, func(rpc.Request) any { return out })
		if _, err := c.DesktopOperationStatus(t.Context(), ref); err != nil {
			t.Fatalf("valid response rejected %+v %v", out, err)
		}
	}
}

func TestDesktopOperationPrepareValidation(t *testing.T) {
	c := (&Client{Socket: "/missing"}).WithDesktop(rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 1})
	for _, method := range []string{"project.list", "daemon.stop", "desktop.operation.cancel", "git.pull", "git.checkout"} {
		if _, err := c.PrepareDesktopOperation(method, nil); err == nil {
			t.Fatalf("accepted %s", method)
		}
	}
	if _, err := c.PrepareDesktopOperation("project.add", make(chan int)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := c.DesktopOperationStatus(t.Context(), rpc.DesktopOperationRef{}); err == nil {
		t.Fatal("invalid reference sent")
	}
	var empty DesktopOperation
	if err := empty.Execute(t.Context(), nil); err == nil {
		t.Fatal("empty operation executed")
	}
	c.Desktop = nil
	if _, err := c.PrepareDesktopOperation("project.add", nil); err == nil {
		t.Fatal("missing identity accepted")
	}
	if _, err := c.CancelDesktopOperation(t.Context(), rpc.DesktopOperationRef{}); err == nil {
		t.Fatal("cancel without identity sent")
	}
}
