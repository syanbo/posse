package app

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hive/internal/history"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
}

func TestListAndScaleImages(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "2.png"), 2400, 1520)
	writePNG(t, filepath.Join(dir, "10.png"), 300, 200)
	writePNG(t, filepath.Join(dir, "note.png"), 10, 10) // 不是序号命名
	os.WriteFile(filepath.Join(dir, "3.txt"), nil, 0o644)
	items := listImages(dir)
	if len(items) != 2 || items[0].n != 10 || items[1].n != 2 {
		t.Fatalf("items = %+v", items)
	}
	big, err := scaledImage(items[1].path, thumbWidth)
	if err != nil {
		t.Fatal(err)
	}
	if b := big.Bounds(); b.Dx() != thumbWidth || b.Dy() != 1520*thumbWidth/2400 {
		t.Fatalf("scaled to %v", b)
	}
	// 本来就小的不放大
	small, _ := scaledImage(items[0].path, thumbWidth)
	if b := small.Bounds(); b.Dx() != 300 || b.Dy() != 200 {
		t.Fatalf("small scaled to %v", b)
	}
	if got := claudeImageDir("../x", ""); got != "" {
		t.Fatalf("bad session id gave %q", got)
	}
}

// pngBase64 是一张 w×h 的 PNG 的 base64。
func pngBase64(t *testing.T, w, h int) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), buf.Bytes()
}

// 图片面板列出 Claude 工具返回的图片（截图、Read 读的图）和 Codex 生成的图片，缩略图解得出来，点开写出原图。
func TestAgentImages(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "s1.jsonl")
	shot, shotRaw := pngBase64(t, 300, 600)
	read, _ := pngBase64(t, 800, 400)
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u1","name":"mcp__iphone-use__phone_screenshot","input":{}}]}}`,
		`{"type":"user","timestamp":"2026-10-19T06:43:23Z","message":{"content":[{"type":"tool_result","tool_use_id":"u1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + shot + `"}}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u2","name":"Read","input":{"file_path":"/tmp/x/preview.png"}}]}}`,
		`{"type":"user","timestamp":"2026-10-19T06:44:23Z","message":{"content":[{"type":"tool_result","tool_use_id":"u2","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + read + `"}}]}]}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	imgs, err := history.NewImageScanner(transcript).Scan()
	if err != nil {
		t.Fatal(err)
	}
	made := toolImageItems(transcript, imgs)
	if len(made) != 2 || made[0].label != "preview.png" || made[1].label != "phone_screenshot" || made[0].source() != "/tmp/x/preview.png" {
		t.Fatalf("claude items %+v", made)
	}
	thumb, err := decodeItem(made[1], thumbWidth)
	if err != nil || thumb.Bounds().Dx() != 300 || thumb.Bounds().Dy() != 600 {
		t.Fatalf("thumb %v %v", thumb.Bounds(), err)
	}
	path, err := writeToolImage(made[1])
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if b, _ := os.ReadFile(path); !bytes.Equal(b, shotRaw) || filepath.Ext(path) != ".png" {
		t.Fatalf("opened image %s differs: %d bytes", path, len(b))
	}

	// Codex：~/.codex/generated_images/<会话 ID>/，新的在前
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex", "generated_images", "019f-codex")
	os.MkdirAll(dir, 0o755)
	writePNG(t, filepath.Join(dir, "ig_abc.png"), 64, 64)
	writePNG(t, filepath.Join(dir, "exec-1.png"), 64, 32)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), nil, 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(dir, "ig_abc.png"), old, old)
	codex := codexImages("019f-codex")
	if len(codex) != 2 || codex[0].label != "代码产出" || codex[1].label != "生成的图片" {
		t.Fatalf("codex items %+v", codex)
	}
	if _, err := decodeItem(codex[1], thumbWidth); err != nil {
		t.Fatal(err)
	}
	if got := codexImages("../x"); got != nil {
		t.Fatalf("bad session id gave %v", got)
	}
}
