package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestStatuslineReportsContextWithoutUserStatusline(t *testing.T) {
	dir, err := os.MkdirTemp("", "sl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan json.RawMessage, 1)
	srv := rpc.NewServer()
	srv.Handle("usage.report", func(_ context.Context, raw json.RawMessage) (any, error) {
		got <- raw
		return map[string]bool{"updated": true}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)
	t.Setenv("POSSE_SOCKET", sock)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	payload := `{"session_id":"s","context_window":{"context_window_size":1000000,"used_percentage":16.8}}`
	f, err := os.CreateTemp(dir, "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	var out, errOut bytes.Buffer
	if err := cmdStatusline(&env{out: &out, errOut: &errOut}, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("unexpected statusline output: %q %q", out.String(), errOut.String())
	}
	select {
	case raw := <-got:
		var p struct {
			Agent   string          `json:"agent"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &p) != nil || p.Agent != "claude" || string(p.Payload) != payload {
			t.Fatalf("report %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("context without rate_limits was not reported")
	}
}
