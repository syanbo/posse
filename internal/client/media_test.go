package client

import (
	"context"
	"errors"
	"testing"

	"posse/internal/media"
	"posse/internal/rpc"
)

func TestMediaClient(t *testing.T) {
	source := rpc.MediaSource{Agent: "codex", SessionID: "session", TranscriptPath: "/target/session.jsonl"}
	item := media.Item{Key: "target-image", Path: "/target/image.png"}
	var calls int
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		calls++
		switch p.Method {
		case "media.list":
			var got rpc.MediaSource
			if err := rpc.Decode(p.Params, &got); err != nil || got != source {
				t.Errorf("source = %+v %v", got, err)
			}
			return rpc.MediaListResult{Made: []media.Item{item}, MadeAll: 1, Found: true}, nil
		case "media.path":
			var got rpc.MediaPathParams
			if err := rpc.Decode(p.Params, &got); err != nil || got.Item.Path != item.Path {
				t.Errorf("item = %+v %v", got, err)
			}
			return rpc.MediaPathResult{Path: "/target/private/image.png"}, nil
		default:
			t.Errorf("unexpected method %s", p.Method)
			return nil, nil
		}
	})
	list, err := c.MediaList(t.Context(), source)
	if err != nil || len(list.Made) != 1 || !list.Found {
		t.Fatalf("list = %+v %v", list, err)
	}
	path, err := c.MediaPath(t.Context(), item)
	if err != nil || path != "/target/private/image.png" || calls != 2 {
		t.Fatalf("path = %q %v calls=%d", path, err, calls)
	}
}

func TestMediaClientRequiresDesktop(t *testing.T) {
	for _, remote := range []bool{false, true} {
		c := &Client{Remote: remote, Socket: "/missing"}
		_, listErr := c.MediaList(t.Context(), rpc.MediaSource{})
		_, pathErr := c.MediaPath(t.Context(), media.Item{})
		for _, err := range []error{listErr, pathErr} {
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
				t.Fatalf("unbound media touched socket: %v", err)
			}
		}
	}
}

func TestMediaClientRejectsResponses(t *testing.T) {
	for _, path := range []string{"", "relative", "/target/nul\x00"} {
		c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { return rpc.MediaPathResult{Path: path}, nil })
		if got, err := c.MediaPath(t.Context(), media.Item{Path: "/target/image"}); err == nil || got != "" {
			t.Fatalf("invalid path = %q %v", got, err)
		}
	}
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		return rpc.MediaListResult{Made: []media.Item{{Key: "one"}}, MadeAll: 0}, nil
	})
	if _, err := c.MediaList(t.Context(), rpc.MediaSource{}); err == nil {
		t.Fatal("invalid list accepted")
	}
	calls := 0
	c = fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeUnknown, "media not supported")
	})
	_, err := c.MediaPath(context.Background(), media.Item{Path: "/target/image"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeUnknown || re.Message != "media not supported" || calls != 1 {
		t.Fatalf("unknown error = %v calls=%d", err, calls)
	}
}
