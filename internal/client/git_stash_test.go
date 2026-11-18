package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func clientStashTarget() rpc.GitStashParams {
	p := rpc.GitStashParams{GitFetchParams: clientFetchTarget(), Head: strings.Repeat("a", 40), IndexID: strings.Repeat("b", 64), Action: rpc.GitStashPush, Entries: []gitx.Entry{{Path: "new", X: '?', Y: '?', Untracked: true}}, Message: "private note"}
	p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
	return p
}
func clientStashResult(p rpc.GitStashParams) rpc.GitStashResult {
	args := []string{"stash", "push", "--include-untracked", "-m", "[message omitted]"}
	if p.Action != rpc.GitStashPush {
		args = []string{"stash", p.Action}
		if p.Action != rpc.GitStashDrop {
			args = append(args, "--index")
		}
		args = append(args, "stash@{1}")
	}
	return rpc.GitStashResult{Target: p.Metadata(), Status: rpc.GitFetchSucceeded, Started: true, Logs: []gitx.Run{{ID: 1, OpID: 1, Op: "stash", Dir: p.Expected.Root, Args: args, Start: time.Now(), Done: true}}}
}
func TestGitStashClientLostResponseQueriesOriginal(t *testing.T) {
	var calls, queries atomic.Int32
	p := clientStashTarget()
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
		raw, _ := json.Marshal(clientStashResult(p))
		op := rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.stash", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
		result, _ := json.Marshal(op)
		json.NewEncoder(conn).Encode(rpc.Response{Result: result})
	})
	c := (&Client{Socket: socket}).WithDesktop(rpc.DesktopIdentity{ClientID: "paths", InstanceID: "daemon", Epoch: 1})
	op, err := c.PrepareGitStash(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.Execute(t.Context())
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown {
		t.Fatal("response not unknown", err)
	}
	var sent rpc.GitStashParams
	json.Unmarshal(recorded.Load().(rpc.DesktopCall).Params, &sent)
	if !reflect.DeepEqual(sent, p) {
		t.Fatal("params changed")
	}
	if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 1 {
		t.Fatal("request replayed")
	}
	out, err := c.GitStashStatus(t.Context(), op.Ref(), p)
	if err != nil || out.Result == nil || queries.Load() != 1 || calls.Load() != 1 {
		t.Fatal("original status", out, err)
	}
	wrong := p
	wrong.Entries = append([]gitx.Entry{}, p.Entries...)
	wrong.Entries[0].Path = "different"
	if _, err := c.GitStashStatus(t.Context(), op.Ref(), wrong); err == nil {
		t.Fatal("different original params accepted")
	}
}

func TestGitStashClientResultValidation(t *testing.T) {
	for _, action := range []string{rpc.GitStashPush, rpc.GitStashApply, rpc.GitStashPop, rpc.GitStashDrop} {
		p := clientStashTarget()
		p.Action = action
		if action != rpc.GitStashPush {
			p.Message, p.MessageHash = "", ""
			p.Entries = nil
			p.OID = strings.Repeat("c", 40)
		}
		if action == rpc.GitStashDrop {
			p.Head, p.IndexID = "", ""
		}
		for _, mode := range []string{"valid", "all", "keep-index", "missing-log", "wrong-op", "target", "bad-oid", "ref"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				out := clientStashResult(p)
				switch mode {
				case "all":
					out.Logs[0].Args = append(out.Logs[0].Args, "--all")
				case "keep-index":
					out.Logs[0].Args = append(out.Logs[0].Args, "-k")
				case "missing-log":
					out.Logs = nil
				case "wrong-op":
					out.Logs[0].Op = "commit"
				case "target":
					out.Target.Expected.Root = "/different"
				case "bad-oid":
					out.OID = "stash@{0}"
				case "ref":
					out.Logs[0].Args[len(out.Logs[0].Args)-1] = "HEAD"
				}
				if validGitStashResult(out, p) != (mode == "valid") {
					t.Fatal("result validation", out)
				}
			})
		}
	}
}

func TestGitStashClientParams(t *testing.T) {
	p := clientStashTarget()
	p.Message = string([]byte{0xff})
	p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
	if rpc.ValidGitStashParams(p) {
		t.Fatal("invalid UTF-8 accepted")
	}
	p = clientStashTarget()
	p.Message = strings.Repeat("x", 4096)
	p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
	if !rpc.ValidGitStashParams(p) {
		t.Fatal("4096 bytes rejected")
	}
	p.Message += "x"
	p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
	if rpc.ValidGitStashParams(p) {
		t.Fatal("overlong message accepted")
	}
}
