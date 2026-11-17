package client

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientFetchTarget() rpc.GitFetchParams {
	return rpc.GitFetchParams{WorkspaceID: "workspace", WorkspacePath: "/target/repo", Expected: testGitIdentity()}
}
func clientFetchResult(p rpc.GitFetchParams) rpc.GitFetchResult {
	return rpc.GitFetchResult{Target: p, Status: rpc.GitFetchSucceeded, Started: true, Logs: []gitx.Run{{ID: 1, OpID: 1, Op: "fetch", Dir: p.Expected.Root, Args: []string{"fetch", "--all"}, Start: time.Now(), Done: true}}}
}

func TestGitFetchClientPreparedAndStatus(t *testing.T) {
	p := clientFetchTarget()
	result := clientFetchResult(p)
	var calls atomic.Int32
	var current atomic.Value
	current.Store("running")
	c := operationTestClient(t, func(req rpc.Request) any {
		if req.Method == "desktop.call" {
			calls.Add(1)
			var call rpc.DesktopCall
			json.Unmarshal(req.Params, &call)
			var got rpc.GitFetchParams
			json.Unmarshal(call.Params, &got)
			if got != p || call.Method != "git.fetch" || call.Sequence == 0 {
				t.Errorf("request %+v", call)
			}
			return result
		}
		var ref rpc.DesktopOperationRef
		json.Unmarshal(req.Params, &ref)
		state := current.Load().(string)
		out := rpc.DesktopOperation{DesktopOperationRef: ref, State: state}
		if state != "unknown" {
			out.Method = "git.fetch"
			out.StartedAt = 1
		}
		if state == "done" {
			out.FinishedAt = 2
			out.Result, _ = json.Marshal(result)
		}
		return out
	})
	op, err := c.PrepareGitFetch(p)
	if err != nil || calls.Load() != 0 || op.Ref().Sequence == 0 {
		t.Fatalf("prepare %v", err)
	}
	if out, err := op.Execute(t.Context()); err != nil || out.Status != rpc.GitFetchSucceeded {
		t.Fatalf("execute %+v %v", out, err)
	}
	if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 1 {
		t.Fatal("fetch replayed")
	}
	for _, next := range []string{"running", "done", "unknown"} {
		state := next
		current.Store(state)
		got, err := c.GitFetchStatus(t.Context(), op.Ref(), p)
		if err != nil || got.Operation.State != state || (got.Result != nil) != (state == "done") {
			t.Fatalf("status %+v %v", got, err)
		}
	}
	p.WorkspacePath = "relative"
	if _, err := c.PrepareGitFetch(p); err == nil || calls.Load() != 1 {
		t.Fatal("invalid target sent")
	}
}

func TestGitFetchClientRejectsResult(t *testing.T) {
	p := clientFetchTarget()
	for name, mutate := range map[string]func(*rpc.GitFetchResult){
		"target":       func(r *rpc.GitFetchResult) { r.Target.Expected.RootInode++ },
		"state":        func(r *rpc.GitFetchResult) { r.Status = "unknown" },
		"started":      func(r *rpc.GitFetchResult) { r.Started = false },
		"nil-logs":     func(r *rpc.GitFetchResult) { r.Logs = nil },
		"missing-logs": func(r *rpc.GitFetchResult) { r.Logs = []gitx.Run{} },
		"error":        func(r *rpc.GitFetchResult) { r.Error = rpc.Errorf(rpc.CodeInternal, "bad") },
		"args":         func(r *rpc.GitFetchResult) { r.Logs[0].Args = append(r.Logs[0].Args, "--prune") },
		"dir":          func(r *rpc.GitFetchResult) { r.Logs[0].Dir = "/other" },
		"op":           func(r *rpc.GitFetchResult) { r.Logs[0].Op = "pull" },
		"unfinished":   func(r *rpc.GitFetchResult) { r.Logs[0].Done = false },
		"canceled":     func(r *rpc.GitFetchResult) { r.Logs[0].Canceled = true },
		"duplicate":    func(r *rpc.GitFetchResult) { r.Logs = append(r.Logs, r.Logs[0]) },
		"huge":         func(r *rpc.GitFetchResult) { r.Logs[0].Out = strings.Repeat("x", 6<<10) },
		"utf8":         func(r *rpc.GitFetchResult) { r.Logs[0].Out = "\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			out := clientFetchResult(p)
			mutate(&out)
			if validGitFetchResult(out, p) {
				t.Fatal("invalid result accepted")
			}
			// JSON 会把无效 UTF-8 替换为有效字符；此项只检验结构校验器。
			if name == "utf8" {
				return
			}
			var calls atomic.Int32
			c := operationTestClient(t, func(req rpc.Request) any {
				calls.Add(1)
				if req.Method == "desktop.call" {
					return out
				}
				var ref rpc.DesktopOperationRef
				json.Unmarshal(req.Params, &ref)
				raw, _ := json.Marshal(out)
				return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.fetch", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
			})
			op, err := c.PrepareGitFetch(p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = op.Execute(t.Context())
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown {
				t.Fatalf("invalid outcome %v", err)
			}
			var ref rpc.DesktopOperationRef
			if json.Unmarshal(re.Data, &ref) != nil || ref != op.Ref() {
				t.Fatalf("lost reference %+v", re)
			}
			if _, err := c.GitFetchStatus(t.Context(), ref, p); err == nil {
				t.Fatal("invalid cached outcome")
			}
			if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 2 {
				t.Fatal("invalid result replayed")
			}
		})
	}
}

func TestGitFetchClientFailureStates(t *testing.T) {
	p := clientFetchTarget()
	for state, code := range map[string]string{rpc.GitFetchFailed: rpc.CodeInternal, rpc.GitFetchCanceled: "canceled", rpc.GitFetchTimedOut: rpc.CodeTimeout, rpc.GitFetchConflict: rpc.CodeConflict} {
		for _, started := range []bool{false, true} {
			if state == rpc.GitFetchConflict && started {
				continue
			}
			out := clientFetchResult(p)
			out.Status = state
			out.Started = started
			out.Error = rpc.Errorf(code, "operation failed")
			if started {
				out.Logs[0].Err = "operation failed"
				out.Logs[0].Canceled = state == rpc.GitFetchCanceled
			} else {
				out.Logs = []gitx.Run{}
			}
			if !validGitFetchResult(out, p) {
				t.Fatalf("valid failure rejected %+v", out)
			}
			out.Error.Code = "unexpected"
			if validGitFetchResult(out, p) {
				t.Fatal("wrong code accepted")
			}
		}
	}
	c := operationTestClient(t, func(req rpc.Request) any {
		var ref rpc.DesktopOperationRef
		json.Unmarshal(req.Params, &ref)
		return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.fetch", State: "done", StartedAt: 1, FinishedAt: 2, Error: rpc.Errorf(rpc.CodeBadRequest, "invalid Git fetch parameters")}
	})
	op, err := c.PrepareGitFetch(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.GitFetchStatus(t.Context(), op.Ref(), p)
	if err != nil || out.Result != nil || out.Operation.Error == nil {
		t.Fatalf("upper error %+v %v", out, err)
	}
}
