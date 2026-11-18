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

func clientHistoryTarget() rpc.GitHistoryParams {
	p := rpc.GitHistoryParams{GitFetchParams: clientFetchTarget(), Head: strings.Repeat("a", 40), OID: strings.Repeat("a", 40), Current: "main", Action: rpc.GitHistoryReword, Message: "private note"}
	p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
	return p
}
func clientHistoryResult(p rpc.GitHistoryParams) rpc.GitHistoryResult {
	return rpc.GitHistoryResult{Target: p.Metadata(), Status: rpc.GitFetchSucceeded, Started: true, Logs: []gitx.Run{{ID: 1, OpID: 1, Op: "history", Dir: p.Expected.Root, Args: []string{"commit", "--amend", "--only", "-F", "-"}, Start: time.Now(), Done: true}}}
}
func TestGitHistoryClientLostResponseQueriesOriginal(t *testing.T) {
	var calls, queries atomic.Int32
	p := clientHistoryTarget()
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
		raw, _ := json.Marshal(clientHistoryResult(p))
		op := rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.history", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
		result, _ := json.Marshal(op)
		json.NewEncoder(conn).Encode(rpc.Response{Result: result})
	})
	c := (&Client{Socket: socket}).WithDesktop(rpc.DesktopIdentity{ClientID: "paths", InstanceID: "daemon", Epoch: 1})
	op, err := c.PrepareGitHistory(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.Execute(t.Context())
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown {
		t.Fatal("response not unknown", err)
	}
	var sent rpc.GitHistoryParams
	json.Unmarshal(recorded.Load().(rpc.DesktopCall).Params, &sent)
	if !reflect.DeepEqual(sent, p) {
		t.Fatal("params changed")
	}
	if _, err := op.Execute(t.Context()); err == nil || calls.Load() != 1 {
		t.Fatal("request replayed")
	}
	out, err := c.GitHistoryStatus(t.Context(), op.Ref(), p)
	if err != nil || out.Result == nil || queries.Load() != 1 || calls.Load() != 1 {
		t.Fatal("original status", out, err)
	}
	wrong := p
	wrong.Head = strings.Repeat("b", 40)
	if _, err := c.GitHistoryStatus(t.Context(), op.Ref(), wrong); err == nil {
		t.Fatal("different original params accepted")
	}
}
