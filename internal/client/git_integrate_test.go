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

func clientIntegrateTarget() rpc.GitIntegrateParams {
	return rpc.GitIntegrateParams{GitFetchParams: clientFetchTarget(), Action: rpc.GitIntegrateCherryPick, Head: strings.Repeat("a", 40), Current: "main", OIDs: []string{strings.Repeat("b", 40), strings.Repeat("c", 40)}}
}
func clientIntegrateResult(p rpc.GitIntegrateParams) rpc.GitIntegrateResult {
	return rpc.GitIntegrateResult{Target: p, Status: rpc.GitFetchSucceeded, Started: true, Logs: []gitx.Run{{ID: 1, OpID: 1, Op: "integrate", Dir: p.Expected.Root, Args: rpc.GitIntegrateArgs(p), Start: time.Now(), Done: true}}}
}
func TestGitIntegrateClientLostResponseQueriesOriginal(t *testing.T) {
	var calls, queries atomic.Int32
	p := clientIntegrateTarget()
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
		raw, _ := json.Marshal(clientIntegrateResult(p))
		op := rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.integrate", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
		result, _ := json.Marshal(op)
		json.NewEncoder(conn).Encode(rpc.Response{Result: result})
	})
	c := (&Client{Socket: socket}).WithDesktop(rpc.DesktopIdentity{ClientID: "integrate", InstanceID: "daemon", Epoch: 1})
	op, err := c.PrepareGitIntegrate(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.Execute(t.Context())
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown {
		t.Fatal("response not unknown", err)
	}
	var sent rpc.GitIntegrateParams
	json.Unmarshal(recorded.Load().(rpc.DesktopCall).Params, &sent)
	if !reflect.DeepEqual(sent, p) {
		t.Fatal("params changed")
	}
	if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 1 {
		t.Fatal("replayed request")
	}
	out, err := c.GitIntegrateStatus(t.Context(), op.Ref(), p)
	if err != nil || out.Result == nil || queries.Load() != 1 || calls.Load() != 1 {
		t.Fatal("original status", out, err)
	}
	wrong := p
	wrong.Head = strings.Repeat("d", 40)
	if _, err := c.GitIntegrateStatus(t.Context(), op.Ref(), wrong); err == nil {
		t.Fatal("changed original params accepted")
	}
}

func TestGitIntegrateClientResults(t *testing.T) {
	for _, action := range []string{"merge", "rebase", "checkout-rebase", "cherry-pick", "revert", "continue", "skip", "abort"} {
		p := clientIntegrateTarget()
		p.Action = action
		p.OIDs = nil
		switch action {
		case "merge", "rebase", "checkout-rebase":
			p.Branch, p.BranchOID = "topic", strings.Repeat("b", 40)
		case "cherry-pick", "revert":
			p.OIDs = []string{strings.Repeat("b", 40)}
		case "continue":
			p.Operation = "merge"
			p.IndexID = strings.Repeat("b", 64)
		default:
			p.Operation = "rebase"
		}
		for _, mode := range []string{"valid", "extra", "wrong-command", "missing-log", "target", "message", "abort", "unknown"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				out := clientIntegrateResult(p)
				switch mode {
				case "extra":
					out.Logs[0].Args = append(out.Logs[0].Args, "--no-verify")
				case "wrong-command":
					out.Logs[0].Args = []string{"commit", "--only", "-F", "-"}
				case "missing-log":
					out.Logs = nil
				case "target":
					out.Target.Head = strings.Repeat("d", 40)
				case "message":
					out.Logs[0].Args = []string{"merge", "-m", "replacement", p.Branch}
				case "abort":
					out.Logs = append(out.Logs, gitx.Run{ID: 2, OpID: 1, Op: "integrate", Dir: p.Expected.Root, Args: []string{"reset", "--hard"}, Start: time.Now(), Done: true})
				case "unknown":
					out.Status = "unknown"
				}
				if validGitIntegrateResult(out, p) != (mode == "valid") {
					t.Fatal("invalid result validation", out)
				}
			})
		}
	}
}

func TestGitIntegrateClientOwnedParams(t *testing.T) {
	p := clientIntegrateTarget()
	c := (&Client{Socket: "unused"}).WithDesktop(rpc.DesktopIdentity{ClientID: "integrate", InstanceID: "daemon", Epoch: 1})
	op, err := c.PrepareGitIntegrate(p)
	if err != nil {
		t.Fatal(err)
	}
	first := p.OIDs[0]
	p.OIDs[0] = strings.Repeat("d", 40)
	if op.target.OIDs[0] != first {
		t.Fatal("borrowed mutable slice")
	}
	p = clientIntegrateTarget()
	p.OIDs = make([]string, 500)
	for i := range p.OIDs {
		p.OIDs[i] = strings.Repeat("b", 40)
	}
	if !rpc.ValidGitIntegrateParams(p) {
		t.Fatal("500 commits rejected")
	}
	p.OIDs = append(p.OIDs, p.OIDs[0])
	if rpc.ValidGitIntegrateParams(p) {
		t.Fatal("501 commits accepted")
	}
}
