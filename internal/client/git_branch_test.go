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

func clientBranchTarget() rpc.GitBranchParams {
	return rpc.GitBranchParams{GitFetchParams: clientFetchTarget(), Action: rpc.GitBranchCheckout, Branch: "main", BranchOID: strings.Repeat("a", 40)}
}
func clientBranchWriteResult(p rpc.GitBranchParams) rpc.GitBranchResult {
	return rpc.GitBranchResult{Target: p, Status: rpc.GitFetchSucceeded, Started: true, OID: p.BranchOID, Logs: []gitx.Run{{ID: 1, OpID: 1, Op: "branch", Dir: p.Expected.Root, Args: []string{"switch", p.Branch}, Start: time.Now(), Done: true}}}
}

func TestGitBranchClientPreparedAndStatus(t *testing.T) {
	p := clientBranchTarget()
	result := clientBranchWriteResult(p)
	var calls atomic.Int32
	var current atomic.Value
	current.Store("running")
	c := operationTestClient(t, func(req rpc.Request) any {
		if req.Method == "desktop.call" {
			calls.Add(1)
			var call rpc.DesktopCall
			json.Unmarshal(req.Params, &call)
			var got rpc.GitBranchParams
			json.Unmarshal(call.Params, &got)
			if got != p || call.Method != "git.branch" || call.Sequence == 0 {
				t.Errorf("request %+v", call)
			}
			return result
		}
		var ref rpc.DesktopOperationRef
		json.Unmarshal(req.Params, &ref)
		state := current.Load().(string)
		out := rpc.DesktopOperation{DesktopOperationRef: ref, State: state}
		if state != "unknown" {
			out.Method = "git.branch"
			out.StartedAt = 1
		}
		if state == "done" {
			out.FinishedAt = 2
			out.Result, _ = json.Marshal(result)
		}
		return out
	})
	op, err := c.PrepareGitBranch(p)
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
		got, err := c.GitBranchStatus(t.Context(), op.Ref(), p)
		if err != nil || got.Operation.State != state || (got.Result != nil) != (state == "done") {
			t.Fatalf("status %+v %v", got, err)
		}
	}
	p.WorkspacePath = "relative"
	if _, err := c.PrepareGitBranch(p); err == nil || calls.Load() != 1 {
		t.Fatal("invalid target sent")
	}
}

func TestGitBranchClientRejectsResult(t *testing.T) {
	p := clientBranchTarget()
	for name, mutate := range map[string]func(*rpc.GitBranchResult){
		"oid":          func(r *rpc.GitBranchResult) { r.OID = strings.Repeat("b", 40) },
		"unmerged":     func(r *rpc.GitBranchResult) { r.Unmerged = true },
		"target":       func(r *rpc.GitBranchResult) { r.Target.Expected.RootInode++ },
		"state":        func(r *rpc.GitBranchResult) { r.Status = "unknown" },
		"started":      func(r *rpc.GitBranchResult) { r.Started = false },
		"nil-logs":     func(r *rpc.GitBranchResult) { r.Logs = nil },
		"missing-logs": func(r *rpc.GitBranchResult) { r.Logs = []gitx.Run{} },
		"error":        func(r *rpc.GitBranchResult) { r.Error = rpc.Errorf(rpc.CodeInternal, "bad") },
		"args":         func(r *rpc.GitBranchResult) { r.Logs[0].Args = append(r.Logs[0].Args, "--prune") },
		"dir":          func(r *rpc.GitBranchResult) { r.Logs[0].Dir = "/other" },
		"op":           func(r *rpc.GitBranchResult) { r.Logs[0].Op = "push" },
		"unfinished":   func(r *rpc.GitBranchResult) { r.Logs[0].Done = false },
		"canceled":     func(r *rpc.GitBranchResult) { r.Logs[0].Canceled = true },
		"duplicate":    func(r *rpc.GitBranchResult) { r.Logs = append(r.Logs, r.Logs[0]) },
		"huge":         func(r *rpc.GitBranchResult) { r.Logs[0].Out = strings.Repeat("x", 6<<10) },
		"utf8":         func(r *rpc.GitBranchResult) { r.Logs[0].Out = "\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			out := clientBranchWriteResult(p)
			mutate(&out)
			if validGitBranchResult(out, p) {
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
				return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.branch", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
			})
			op, err := c.PrepareGitBranch(p)
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
			if _, err := c.GitBranchStatus(t.Context(), ref, p); err == nil {
				t.Fatal("invalid cached outcome")
			}
			if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 2 {
				t.Fatal("invalid result replayed")
			}
		})
	}
}

func TestGitBranchClientNoopAndUnmerged(t *testing.T) {
	p := clientBranchTarget()
	out := rpc.GitBranchResult{Target: p, Status: rpc.GitFetchSucceeded, OID: p.BranchOID, Logs: []gitx.Run{}}
	if !validGitBranchResult(out, p) {
		t.Fatal("noop rejected")
	}
	p.Action = rpc.GitBranchDelete
	out = clientBranchWriteResult(p)
	out.Unmerged, out.Started, out.OID = true, false, ""
	out.Logs[0].Args = []string{"branch", "-d", p.Branch}
	out.Logs[0].Err = "branch not fully merged"
	if !validGitBranchResult(out, p) {
		t.Fatal("unmerged rejected")
	}
	p.Force, out.Target.Force = true, true
	if validGitBranchResult(out, p) {
		t.Fatal("forced unmerged accepted")
	}
}

func TestGitBranchClientCommands(t *testing.T) {
	for _, action := range []string{rpc.GitBranchCheckout, rpc.GitBranchRevision, rpc.GitBranchCreate, rpc.GitBranchDelete} {
		p := clientBranchTarget()
		p.Action = action
		oid := p.BranchOID
		switch action {
		case rpc.GitBranchCheckout:
			p.Remote, p.Branch = true, "origin/topic"
		case rpc.GitBranchRevision:
			p.Revision, p.RevisionOID, p.Branch, p.BranchOID = "release", oid, "", ""
		case rpc.GitBranchCreate:
			p.FromOID, p.BranchOID, p.Checkout = oid, "", true
		case rpc.GitBranchDelete:
			p.Remote, p.Branch = true, "origin/+main"
		}
		out := clientBranchWriteResult(p)
		out.OID, out.Logs[0].Args = oid, rpc.GitBranchArgs(p, oid)
		if !validGitBranchResult(out, p) {
			t.Fatalf("rejected %s", action)
		}
		out.Logs[0].Args = append(out.Logs[0].Args, "--force")
		if validGitBranchResult(out, p) {
			t.Fatalf("force accepted %s", action)
		}
	}
}
