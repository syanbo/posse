package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestDesktopViewerDoesNotWriteFrames(t *testing.T) {
	done := make(chan error, 1)
	id := rpc.DesktopIdentity{ClientID: "viewer", InstanceID: "daemon", Epoch: 3}
	socket := fakeAttach(t, func(c net.Conn) {
		defer c.Close()
		r := bufio.NewReader(c)
		line, err := r.ReadBytes('\n')
		if err != nil {
			done <- err
			return
		}
		var request rpc.Request
		if err := json.Unmarshal(line, &request); err != nil {
			done <- err
			return
		}
		var p struct {
			Desktop rpc.DesktopIdentity `json:"desktop"`
			Cols    int                 `json:"cols"`
			Rows    int                 `json:"rows"`
		}
		if err := json.Unmarshal(request.Params, &p); err != nil {
			done <- err
			return
		}
		if p.Desktop != id {
			done <- errors.New("attach identity changed")
			return
		}
		if p.Cols != 100 || p.Rows != 30 {
			done <- errors.New("attach request dimensions changed")
			return
		}
		rpc.WriteResult(c, map[string]any{"cols": 80, "rows": 24, "readOnly": true})
		data, err := io.ReadAll(r)
		if len(data) != 0 {
			err = errors.New("viewer sent input or resize frame")
		}
		done <- err
	})
	k := (&Client{Socket: socket}).WithDesktop(id).Attach("term")
	defer k.Close()
	if err := k.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	cols, rows, readOnly, err := k.TerminalState()
	if err != nil || cols != 80 || rows != 24 || !readOnly {
		t.Fatalf("terminal state = (%d, %d, %t, %v), want (80, 24, true, nil)", cols, rows, readOnly, err)
	}
	var re *rpc.Error
	if _, err := k.Write([]byte("input")); !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
		t.Fatalf("view input: %v", err)
	}
	if err := k.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	k.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("viewer connection stayed open")
	}
}

func TestTerminalStateWaitsForResize(t *testing.T) {
	attached := make(chan struct{}, 1)
	socket := fakeAttach(t, func(c net.Conn) {
		defer c.Close()
		attached <- struct{}{}
		if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
			return
		}
		rpc.WriteResult(c, map[string]any{"cols": 80, "rows": 24})
	})
	k := (&Client{Socket: socket}).Attach("term")
	defer k.Close()
	done := make(chan error, 1)
	go func() {
		cols, rows, readOnly, err := k.TerminalState()
		if err == nil && (cols != 80 || rows != 24 || readOnly) {
			err = errors.New("terminal state differs from handshake")
		}
		done <- err
	}()
	select {
	case <-attached:
		t.Fatal("TerminalState initiated attach before Resize")
	case err := <-done:
		t.Fatalf("TerminalState returned before attach: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := k.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("TerminalState did not return after handshake")
	}
}

func TestTerminalStateHandshakeFailure(t *testing.T) {
	socket := fakeAttach(t, func(c net.Conn) {
		defer c.Close()
		if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
			return
		}
		json.NewEncoder(c).Encode(rpc.Response{Error: rpc.Errorf(rpc.CodeNotFound, "terminal not found")})
	})
	k := (&Client{Socket: socket}).Attach("missing")
	defer k.Close()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := k.TerminalState()
		done <- err
	}()
	k.Resize(100, 30)
	select {
	case err := <-done:
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeNotFound {
			t.Fatalf("TerminalState error = %v, want not found", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TerminalState hung after handshake failure")
	}
}

func TestTerminalStateClose(t *testing.T) {
	for _, duringHandshake := range []bool{false, true} {
		name := "before attach"
		if duringHandshake {
			name = "during handshake"
		}
		t.Run(name, func(t *testing.T) {
			attached := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			socket := fakeAttach(t, func(c net.Conn) {
				defer c.Close()
				if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
					return
				}
				close(attached)
				<-release
			})
			k := (&Client{Socket: socket}).Attach("term")
			defer k.Close()
			done := make(chan error, 1)
			go func() {
				_, _, _, err := k.TerminalState()
				done <- err
			}()
			if duringHandshake {
				go k.Resize(100, 30)
				select {
				case <-attached:
				case <-time.After(time.Second):
					t.Fatal("attach did not start")
				}
			}
			k.Close()
			select {
			case err := <-done:
				if !errors.Is(err, io.EOF) {
					t.Fatalf("TerminalState error = %v, want EOF", err)
				}
			case <-time.After(time.Second):
				t.Fatal("TerminalState hung after Close")
			}
		})
	}
}
