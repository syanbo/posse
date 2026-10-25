package mobile

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/term"
)

// 图片与视频：没声明 posse.media.v1 的连接看不到；列出工具返回的截图和工具输出里的视频，
// 缩略图解得出、按段读回的是原图，没列过的 ID 读不到。
func TestSessionMedia(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 900, 1800)))
	shot := buf.Bytes()
	video := filepath.Join(dir, "rec.mp4")
	os.WriteFile(video, []byte("not really a video"), 0o644)
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u1","name":"mcp__iphone-use__phone_screenshot","input":{}}]}}`,
		`{"type":"user","timestamp":"2026-10-19T06:43:23Z","message":{"content":[{"type":"tool_result","tool_use_id":"u1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(shot) + `"}}]}]}}`,
		`{"type":"user","timestamp":"2026-10-19T06:45:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"u2","content":"saved ` + video + `"}]}}`,
	}
	transcript := filepath.Join(dir, "s1.jsonl")
	os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	args := map[string]any{"agent": "claude", "sessionId": "s1", "transcriptPath": transcript}
	if hidden := p.next(p.call("session.media", args)); hidden["ok"] != false {
		t.Fatalf("orca connection saw session.media: %v", hidden)
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.media.v1"},
	})))
	list := result(t, p.next(p.call("session.media", args)))
	made, _ := list["made"].([]any)
	if len(made) != 2 || list["madeAll"] != float64(2) {
		t.Fatalf("session.media %v", list)
	}
	rec, img := made[0].(map[string]any), made[1].(map[string]any)
	if rec["kind"] != "video" || rec["label"] != "rec.mp4" || rec["size"] != float64(18) ||
		img["kind"] != "image" || img["label"] != "phone_screenshot" {
		t.Fatalf("items %v %v", rec, img)
	}

	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range args {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	thumb := result(t, p.next(p.call("session.mediaThumb", with(map[string]any{"id": img["id"]}))))
	if thumb["width"] != float64(mediaThumbWidth) || thumb["height"] != float64(mediaThumbWidth*2) {
		t.Fatalf("thumb %v x %v", thumb["width"], thumb["height"])
	}
	var got []byte
	for {
		part := result(t, p.next(p.call("session.mediaRead", with(map[string]any{"id": img["id"], "offset": len(got), "limit": 4096}))))
		b, _ := base64.StdEncoding.DecodeString(part["content"].(string))
		got = append(got, b...)
		if part["size"] != float64(len(shot)) || part["mime"] != "image/png" {
			t.Fatalf("read %v %v", part["size"], part["mime"])
		}
		if len(b) == 0 || len(got) >= len(shot) {
			break
		}
	}
	if !bytes.Equal(got, shot) {
		t.Fatalf("read back %d bytes, want %d", len(got), len(shot))
	}
	part := result(t, p.next(p.call("session.mediaRead", with(map[string]any{"id": rec["id"], "offset": 4}))))
	if b, _ := base64.StdEncoding.DecodeString(part["content"].(string)); string(b) != "really a video" {
		t.Fatalf("video range %q", b)
	}
	if m := p.next(p.call("session.mediaRead", with(map[string]any{"id": "/etc/passwd"}))); m["ok"] != false {
		t.Fatalf("unlisted id accepted: %v", m)
	}
}
