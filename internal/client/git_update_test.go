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

func clientUpdateTarget() rpc.GitUpdateParams {
	return rpc.GitUpdateParams{GitFetchParams: clientFetchTarget(), Branch: "main", BranchOID: strings.Repeat("a", 40), Upstream: "origin/main", Current: true}
}
func clientUpdateWriteResult(p rpc.GitUpdateParams) rpc.GitUpdateResult {
	return rpc.GitUpdateResult{Target: p, Status: rpc.GitFetchSucceeded, Started: true, Logs: []gitx.Run{{ID: 1, OpID: 1, Op: "update", Dir: p.Expected.Root, Args: []string{"pull", "--rebase", "--autostash"}, Start: time.Now(), Done: true}}}
}

func TestGitUpdateClientPreparedAndStatus(t *testing.T) {
	p := clientUpdateTarget()
	result := clientUpdateWriteResult(p)
	var calls atomic.Int32
	var current atomic.Value
	current.Store("running")
	c := operationTestClient(t, func(req rpc.Request) any {
		if req.Method == "desktop.call" {
			calls.Add(1)
			var call rpc.DesktopCall
			json.Unmarshal(req.Params, &call)
			var got rpc.GitUpdateParams
			json.Unmarshal(call.Params, &got)
			if got != p || call.Method != "git.update" || call.Sequence == 0 {
				t.Errorf("request %+v", call)
			}
			return result
		}
		var ref rpc.DesktopOperationRef
		json.Unmarshal(req.Params, &ref)
		state := current.Load().(string)
		out := rpc.DesktopOperation{DesktopOperationRef: ref, State: state}
		if state != "unknown" {
			out.Method = "git.update"
			out.StartedAt = 1
		}
		if state == "done" {
			out.FinishedAt = 2
			out.Result, _ = json.Marshal(result)
		}
		return out
	})
	op, err := c.PrepareGitUpdate(p)
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
		got, err := c.GitUpdateStatus(t.Context(), op.Ref(), p)
		if err != nil || got.Operation.State != state || (got.Result != nil) != (state == "done") {
			t.Fatalf("status %+v %v", got, err)
		}
	}
	p.WorkspacePath = "relative"
	if _, err := c.PrepareGitUpdate(p); err == nil || calls.Load() != 1 {
		t.Fatal("invalid target sent")
	}
}

func TestGitUpdateClientRejectsResult(t *testing.T) {
	p := clientUpdateTarget()
	for name, mutate := range map[string]func(*rpc.GitUpdateResult){
		"autostash":    func(r *rpc.GitUpdateResult) { r.Autostash = &gitx.AutostashLeft{Stash: "bad", Conflicts: true} },
		"incoming":     func(r *rpc.GitUpdateResult) { r.Incoming = -1 },
		"target":       func(r *rpc.GitUpdateResult) { r.Target.Expected.RootInode++ },
		"state":        func(r *rpc.GitUpdateResult) { r.Status = "unknown" },
		"started":      func(r *rpc.GitUpdateResult) { r.Started = false },
		"nil-logs":     func(r *rpc.GitUpdateResult) { r.Logs = nil },
		"missing-logs": func(r *rpc.GitUpdateResult) { r.Logs = []gitx.Run{} },
		"error":        func(r *rpc.GitUpdateResult) { r.Error = rpc.Errorf(rpc.CodeInternal, "bad") },
		"args":         func(r *rpc.GitUpdateResult) { r.Logs[0].Args = append(r.Logs[0].Args, "--prune") },
		"dir":          func(r *rpc.GitUpdateResult) { r.Logs[0].Dir = "/other" },
		"op":           func(r *rpc.GitUpdateResult) { r.Logs[0].Op = "push" },
		"unfinished":   func(r *rpc.GitUpdateResult) { r.Logs[0].Done = false },
		"canceled":     func(r *rpc.GitUpdateResult) { r.Logs[0].Canceled = true },
		"duplicate":    func(r *rpc.GitUpdateResult) { r.Logs = append(r.Logs, r.Logs[0]) },
		"huge":         func(r *rpc.GitUpdateResult) { r.Logs[0].Out = strings.Repeat("x", 6<<10) },
		"utf8":         func(r *rpc.GitUpdateResult) { r.Logs[0].Out = "\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			out := clientUpdateWriteResult(p)
			mutate(&out)
			if validGitUpdateResult(out, p) {
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
				return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.update", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
			})
			op, err := c.PrepareGitUpdate(p)
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
			if _, err := c.GitUpdateStatus(t.Context(), ref, p); err == nil {
				t.Fatal("invalid cached outcome")
			}
			if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 2 {
				t.Fatal("invalid result replayed")
			}
		})
	}
}

func TestGitUpdateClientFailureStates(t *testing.T) {
	p := clientUpdateTarget()
	for state, code := range map[string]string{rpc.GitFetchFailed: rpc.CodeInternal, rpc.GitFetchCanceled: "canceled", rpc.GitFetchTimedOut: rpc.CodeTimeout, rpc.GitFetchConflict: rpc.CodeConflict} {
		for _, started := range []bool{false, true} {
			if state == rpc.GitFetchConflict && started {
				continue
			}
			out := clientUpdateWriteResult(p)
			out.Status = state
			out.Started = started
			out.Error = rpc.Errorf(code, "operation failed")
			if started {
				out.Logs[0].Err = "operation failed"
				out.Logs[0].Canceled = state == rpc.GitFetchCanceled
			} else {
				out.Logs = []gitx.Run{}
			}
			if !validGitUpdateResult(out, p) {
				t.Fatalf("valid failure rejected %+v", out)
			}
			out.Error.Code = "unexpected"
			if validGitUpdateResult(out, p) {
				t.Fatal("wrong code accepted")
			}
		}
	}
	c := operationTestClient(t, func(req rpc.Request) any {
		var ref rpc.DesktopOperationRef
		json.Unmarshal(req.Params, &ref)
		return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.update", State: "done", StartedAt: 1, FinishedAt: 2, Error: rpc.Errorf(rpc.CodeBadRequest, "invalid Git update parameters")}
	})
	op, err := c.PrepareGitUpdate(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.GitUpdateStatus(t.Context(), op.Ref(), p)
	if err != nil || out.Result != nil || out.Operation.Error == nil {
		t.Fatalf("upper error %+v %v", out, err)
	}
}

func TestGitUpdateClientOtherRefspec(t *testing.T) {
	for _, branch := range []string{"main", "+main"} {
		p := clientUpdateTarget()
		p.Current = false
		p.Upstream = "origin/" + branch
		out := clientUpdateWriteResult(p)
		source := branch
		if strings.HasPrefix(source, "+") {
			source = "refs/heads/" + source
		}
		out.Logs[0].Args = []string{"fetch", "origin", source + ":refs/heads/main"}
		if !validGitUpdateResult(out, p) {
			t.Fatal("valid fast-forward request rejected")
		}
		out.Logs[0].Args[2] = "+" + out.Logs[0].Args[2]
		if validGitUpdateResult(out, p) {
			t.Fatal("forced refspec accepted")
		}
	}
}
