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

func clientCommitTarget() rpc.GitCommitParams {
	p := rpc.GitCommitParams{GitFetchParams: clientFetchTarget(), Head: strings.Repeat("a", 40), IndexID: strings.Repeat("b", 64), Action: rpc.GitCommitPaths, Message: "subject\n\nbody", Paths: []rpc.GitPath{{Entry: gitx.Entry{Path: "[new]*.txt", X: '?', Y: '?', Untracked: true}}}, Add: []string{"[new]*.txt"}}
	p.Paths[0].Version = &files.Version{Path: p.Expected.Root + "/[new]*.txt", Hash: strings.Repeat("c", 64), Stamp: files.Stamp{}}
	p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
	return p
}

func clientCommitResult(p rpc.GitCommitParams) rpc.GitCommitResult {
	out := rpc.GitCommitResult{Target: p.Metadata(), Status: rpc.GitFetchSucceeded, Started: true, Logs: []gitx.Run{}}
	for i, args := range rpc.GitCommitCommands(p) {
		out.Logs = append(out.Logs, gitx.Run{ID: int64(i + 1), OpID: 1, Op: "commit", Dir: p.Expected.Root, Args: args, Start: time.Now(), Done: true})
	}
	return out
}
func TestGitCommitClientLostResponseQueriesOriginal(t *testing.T) {
	var calls, queries atomic.Int32
	p := clientCommitTarget()
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
		raw, _ := json.Marshal(clientCommitResult(p))
		op := rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.commit", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
		result, _ := json.Marshal(op)
		json.NewEncoder(conn).Encode(rpc.Response{Result: result})
	})
	c := (&Client{Socket: socket}).WithDesktop(rpc.DesktopIdentity{ClientID: "paths", InstanceID: "daemon", Epoch: 1})
	op, err := c.PrepareGitCommit(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.Execute(t.Context())
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown {
		t.Fatal("response not unknown", err)
	}
	var sent rpc.GitCommitParams
	json.Unmarshal(recorded.Load().(rpc.DesktopCall).Params, &sent)
	if !reflect.DeepEqual(sent, p) {
		t.Fatal("params changed")
	}
	if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 1 {
		t.Fatal("request replayed")
	}
	out, err := c.GitCommitStatus(t.Context(), op.Ref(), p)
	if err != nil || out.Result == nil || queries.Load() != 1 || calls.Load() != 1 {
		t.Fatal("original status", out, err)
	}
	wrong := p
	wrong.Paths = append([]rpc.GitPath{}, p.Paths...)
	wrong.Paths[0].Entry.Path = "different"
	if _, err := c.GitCommitStatus(t.Context(), op.Ref(), wrong); err == nil {
		t.Fatal("different original params accepted")
	}
}
func TestGitCommitClientResultValidation(t *testing.T) {
	p := clientCommitTarget()
	for _, mode := range []string{"valid", "force", "glob", "target", "missing-log", "wrong-op"} {
		t.Run(mode, func(t *testing.T) {
			out := clientCommitResult(p)
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
			if validGitCommitResult(out, p) != (mode == "valid") {
				t.Fatal("result validity", mode)
			}
		})
	}
}
