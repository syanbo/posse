package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientPathsTarget() rpc.GitPathsParams {
	return rpc.GitPathsParams{GitFetchParams: clientFetchTarget(), Head: strings.Repeat("a", 40), Action: rpc.GitPathsStage, Paths: []rpc.GitPath{{Entry: gitx.Entry{Path: "[new]*.txt", X: '?', Y: '?', Untracked: true}}}}
}
func clientPathsResult(p rpc.GitPathsParams) rpc.GitPathsResult {
	out := rpc.GitPathsResult{Target: p.Metadata(), Status: rpc.GitFetchSucceeded, Started: true, Logs: []gitx.Run{}}
	for i, args := range rpc.GitPathsCommands(p) {
		out.Logs = append(out.Logs, gitx.Run{ID: int64(i + 1), OpID: 1, Op: "paths", Dir: p.Expected.Root, Args: args, Start: time.Now(), Done: true})
	}
	return out
}
func TestGitPathsClientLostResponseQueriesOriginal(t *testing.T) {
	var calls, queries atomic.Int32
	p := clientPathsTarget()
	var recorded atomic.Value
	socket := fakeAttach(t, func(conn net.Conn) {
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var req rpc.Request
		json.Unmarshal(line, &req)
		if req.Method == "desktop.call" {
			calls.Add(1)
			var original rpc.DesktopCall
			json.Unmarshal(req.Params, &original)
			recorded.Store(original)
			return
		}
		queries.Add(1)
		var ref rpc.DesktopOperationRef
		json.Unmarshal(req.Params, &ref)
		original := recorded.Load().(rpc.DesktopCall)
		if ref.Desktop != original.Desktop || ref.Sequence != original.Sequence {
			t.Error("query changed ref")
		}
		raw, _ := json.Marshal(clientPathsResult(p))
		op := rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.paths", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
		result, _ := json.Marshal(op)
		json.NewEncoder(conn).Encode(rpc.Response{Result: result})
	})
	c := (&Client{Socket: socket}).WithDesktop(rpc.DesktopIdentity{ClientID: "paths", InstanceID: "daemon", Epoch: 1})
	op, err := c.PrepareGitPaths(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.Execute(t.Context())
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown {
		t.Fatal("response not unknown", err)
	}
	var sent rpc.GitPathsParams
	json.Unmarshal(recorded.Load().(rpc.DesktopCall).Params, &sent)
	if !reflect.DeepEqual(sent, p) {
		t.Fatal("params changed")
	}
	if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 1 {
		t.Fatal("request replayed")
	}
	out, err := c.GitPathsStatus(t.Context(), op.Ref(), p)
	if err != nil || out.Result == nil || queries.Load() != 1 || calls.Load() != 1 {
		t.Fatal("original status", out, err)
	}
	wrong := p
	wrong.Paths = append([]rpc.GitPath{}, p.Paths...)
	wrong.Paths[0].Entry.Path = "different"
	if _, err := c.GitPathsStatus(t.Context(), op.Ref(), wrong); err == nil {
		t.Fatal("different original params accepted")
	}
}
func TestGitPathsClientResultValidation(t *testing.T) {
	p := clientPathsTarget()
	for _, mode := range []string{"valid", "force", "glob", "target", "missing-log", "wrong-op"} {
		t.Run(mode, func(t *testing.T) {
			out := clientPathsResult(p)
			switch mode {
			case "force":
				out.Logs[0].Args = append(out.Logs[0].Args, "--force")
			case "glob":
				out.Logs[0].Args[len(out.Logs[0].Args)-1] = "[new]*.txt"
			case "target":
				out.Target.Head = strings.Repeat("b", 40)
			case "missing-log":
				out.Logs = []gitx.Run{}
			case "wrong-op":
				out.Logs[0].Op = "branch"
			}
			if validGitPathsResult(out, p) != (mode == "valid") {
				t.Fatal("result validity", mode)
			}
		})
	}
	p.Action = rpc.GitPathsWrite
	p.Hash = strings.Repeat("a", 64)
	p.Paths[0].Version = &files.Version{Path: p.Expected.Root + "/[new]*.txt", Hash: strings.Repeat("b", 64), Stamp: files.Stamp{}}
	out := clientPathsResult(p)
	out.Version = &files.Version{Path: p.Paths[0].Version.Path, Hash: p.Hash}
	if !validGitPathsResult(out, p) {
		t.Fatal("write with no Git logs rejected")
	}
	out.Status = rpc.GitFetchConflict
	out.Version = nil
	out.Error = rpc.Errorf(rpc.CodeConflict, "file changed")
	if !validGitPathsResult(out, p) {
		t.Fatal("started save conflict rejected")
	}
	out.Started = false
	if !validGitPathsResult(out, p) {
		t.Fatal("preflight save conflict rejected")
	}
}
