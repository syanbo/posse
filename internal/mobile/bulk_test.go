package mobile

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"posse/internal/term"
)

// sendBin 发一个二进制帧，和 sendRaw 共用手机的发送计数。
func (p *phone) sendBin(b []byte) {
	p.ws.Write(context.Background(), websocket.MessageBinary, p.seal(kindBinary, b))
}

// 声明 posse.bulk.v1 后，大块内容先按块走二进制帧，回复里只剩编号；上传的分段也走二进制帧
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
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.bulk.v1"},
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
	if start["ack"] != true {
		t.Fatalf("start %v without ack", start)
	}
	for off := 0; off < len(img); off += bulkChunk {
		p.sendBin(bulkFrame(opUploadChunk, up, img[off:min(off+bulkChunk, len(img))]))
	}
	// 确认是累计的已收字节数，积压时可能合并，最后一个是全部
	for acked := uint64(0); acked < uint64(len(img)); {
		op, id, payload, ok := parseBulk(<-p.binary)
		if !ok || op != opUploadAck || id != up || len(payload) != 8 {
			t.Fatalf("ack frame op %d id %d len %d", op, id, len(payload))
		}
		n := binary.LittleEndian.Uint64(payload)
		if n <= acked {
			t.Fatalf("ack went from %d to %d", acked, n)
		}
		acked = n
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
	<-p.binary

	// 超出说好大小的段也确认（手机不在窗口上干等），提交时报错
	start = result(t, p.next(p.call("clipboard.startImageUpload", map[string]any{"expectedBytes": 10})))
	over := uint32(start["blob"].(float64))
	p.sendBin(bulkFrame(opUploadChunk, over, img[:20]))
	if op, id, payload, _ := parseBulk(<-p.binary); op != opUploadAck || id != over || binary.LittleEndian.Uint64(payload) != 20 {
		t.Fatalf("overflow ack op %d id %d %v", op, id, payload)
	}
	if m := p.next(p.call("clipboard.commitImageUpload", map[string]any{"uploadId": start["uploadId"]})); m["ok"] != false {
		t.Fatalf("overflowed upload committed: %v", m)
	}

	// 带文件名的（视频、文件）放进新的临时目录、用原名；名字里的目录去掉
	video := append([]byte("\x00\x00\x00\x14ftypqt  "), data...)
	start = result(t, p.next(p.call("clipboard.startImageUpload", map[string]any{"expectedBytes": len(video), "name": "../录屏 1.mov"})))
	up = uint32(start["blob"].(float64))
	for off := 0; off < len(video); off += bulkChunk {
		p.sendBin(bulkFrame(opUploadChunk, up, video[off:min(off+bulkChunk, len(video))]))
	}
	m = p.next(p.call("clipboard.commitImageUpload", map[string]any{"uploadId": start["uploadId"]}))
	path, _ = m["result"].(string)
	if filepath.Base(path) != "录屏 1.mov" || !strings.HasPrefix(filepath.Base(filepath.Dir(path)), "posse-mobile-") {
		t.Fatalf("named upload %v", m)
	}
	defer os.RemoveAll(filepath.Dir(path))
	if saved, _ := os.ReadFile(path); !bytes.Equal(saved, video) {
		t.Fatal("saved video differs")
	}
}

func TestUploadNamesAndSniffing(t *testing.T) {
	for in, want := range map[string]string{
		"a.mp4": "a.mp4", "../../etc/passwd": "passwd", "dir\\x.pdf": "x.pdf", "..": "", "": "", " b\x01.txt ": "b.txt",
	} {
		if got := uploadName(in); got != want {
			t.Errorf("uploadName(%q) = %q, want %q", in, got, want)
		}
	}
	for head, want := range map[string]string{
		"\x00\x00\x00\x14ftypqt  x": ".mov", "\x00\x00\x00\x18ftypmp42x": ".mp4", "\x00\x00\x00\x18ftypheicx": ".heic", "\xff\xd8\xffx": ".jpg", "zz": ".png",
	} {
		if got := sniffExt([]byte(head)); got != want {
			t.Errorf("sniffExt(%q) = %q, want %q", head, got, want)
		}
	}
}
