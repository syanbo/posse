package mobile

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/secretbox"

	"hive/internal/term"
)

// sendBin 发一个二进制帧，和 sendRaw 共用手机的发送计数。
func (p *phone) sendBin(b []byte) {
	s := p.sess
	n := s.recvN
	s.recvN++
	nonce := s.nonce(dirMobileToDesktop, kindBinary, n)
	frame := secretbox.Seal(nonce[:], append(s.header(dirMobileToDesktop, kindBinary, n), b...), &nonce, &s.recvKey)
	p.ws.Write(context.Background(), websocket.MessageBinary, frame)
}

// 声明 hive.bulk.v1 后，大块内容先按块走二进制帧，回复里只剩编号；上传的分段也走二进制帧
func TestBulkFrames(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 3*bulkChunk+123)
	rand.Read(data)
	os.WriteFile(filepath.Join(dir, "clip.mp4"), data, 0o644)

	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}, dir: dir}, -1)
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
	wt := "id:p1::w1"
	params := map[string]any{"worktree": wt, "relativePath": "clip.mp4", "offset": 0, "limit": len(data)}

	// 没声明时照旧是 base64
	old := result(t, p.next(p.call("files.readRange", params)))
	if b, _ := base64.StdEncoding.DecodeString(old["content"].(string)); !bytes.Equal(b, data) {
		t.Fatal("plain range differs")
	}

	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "hive-flutter", "version": 1, "capabilities": []string{"hive.bulk.v1"},
	})))
	part := result(t, p.next(p.call("files.readRange", params)))
	if _, ok := part["content"]; ok {
		t.Fatalf("content still inline: %v", part)
	}
	blob := uint32(part["blobs"].(map[string]any)["content"].(float64))
	var got []byte
	chunks := 0
	for len(p.binary) > 0 {
		op, id, payload, ok := parseBulk(<-p.binary)
		if !ok || op != opBlobChunk || id != blob {
			t.Fatalf("frame op %d id %d", op, id)
		}
		if len(payload) > bulkChunk {
			t.Fatalf("chunk of %d bytes", len(payload))
		}
		got = append(got, payload...)
		chunks++
	}
	if !bytes.Equal(got, data) || chunks != 4 || part["size"] != float64(len(data)) {
		t.Fatalf("got %d bytes in %d chunks, size %v", len(got), chunks, part["size"])
	}

	// 上传：说好大小，分段走二进制帧，提交时对得上就存盘
	img := append([]byte{0xff, 0xd8, 0xff}, data...)
	start := result(t, p.next(p.call("clipboard.startImageUpload", map[string]any{"expectedBytes": len(img)})))
	up := uint32(start["blob"].(float64))
	for off := 0; off < len(img); off += bulkChunk {
		p.sendBin(bulkFrame(opUploadChunk, up, img[off:min(off+bulkChunk, len(img))]))
	}
	m := p.next(p.call("clipboard.commitImageUpload", map[string]any{"uploadId": start["uploadId"]}))
	path, _ := m["result"].(string)
	if filepath.Ext(path) != ".jpg" {
		t.Fatalf("commit %v", m)
	}
	defer os.Remove(path)
	if saved, _ := os.ReadFile(path); !bytes.Equal(saved, img) {
		t.Fatal("saved image differs")
	}

	// 少了一段的提交失败
	start = result(t, p.next(p.call("clipboard.startImageUpload", map[string]any{"expectedBytes": len(img)})))
	p.sendBin(bulkFrame(opUploadChunk, uint32(start["blob"].(float64)), img[:bulkChunk]))
	if m := p.next(p.call("clipboard.commitImageUpload", map[string]any{"uploadId": start["uploadId"]})); m["ok"] != false {
		t.Fatalf("short upload committed: %v", m)
	}
}
