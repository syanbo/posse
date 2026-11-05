package media

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/history"
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
	items := ListImages(dir)
	if len(items) != 2 || items[0].N != 10 || items[1].N != 2 {
		t.Fatalf("items = %+v", items)
	}
	big, err := ScaledImage(items[1].Path, 512)
	if err != nil {
		t.Fatal(err)
	}
	if b := big.Bounds(); b.Dx() != 512 || b.Dy() != 1520*512/2400 {
		t.Fatalf("scaled to %v", b)
	}
	// 本来就小的不放大
	small, _ := ScaledImage(items[0].Path, 512)
	if b := small.Bounds(); b.Dx() != 300 || b.Dy() != 200 {
		t.Fatalf("small scaled to %v", b)
	}
	if got := ClaudeImageDir("../x", ""); got != "" {
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
	imgs, err := history.NewImageScanner("claude", transcript).Scan()
	if err != nil {
		t.Fatal(err)
	}
	_, made := SessionItems(transcript, imgs)
	if len(made) != 2 || made[0].Label != "preview.png" || made[1].Label != "phone_screenshot" || made[0].Source() != "/tmp/x/preview.png" {
		t.Fatalf("claude items %+v", made)
	}
	thumb, err := Decode(made[1], 512)
	if err != nil || thumb.Bounds().Dx() != 300 || thumb.Bounds().Dy() != 600 {
		t.Fatalf("thumb %v %v", thumb.Bounds(), err)
	}
	path, err := WriteToolImage(made[1])
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
	codex := CodexImages("019f-codex")
	if len(codex) != 2 || codex[0].Tag != TagCodeOutput || codex[1].Tag != TagGenerated {
		t.Fatalf("codex items %+v", codex)
	}
	if _, err := Decode(codex[1], 512); err != nil {
		t.Fatal(err)
	}
	if got := CodexImages("../x"); got != nil {
		t.Fatalf("bad session id gave %v", got)
	}
}

// TestPNGBytes：PNG 原样返回，别的格式转成 PNG（剪贴板的图片只认 PNG）。
func TestPNGBytes(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{200, 10, 10, 255})
	var pb, jb bytes.Buffer
	if err := png.Encode(&pb, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jb, img, nil); err != nil {
		t.Fatal(err)
	}
	pp, jp := filepath.Join(dir, "a.png"), filepath.Join(dir, "a.jpg")
	os.WriteFile(pp, pb.Bytes(), 0o600)
	os.WriteFile(jp, jb.Bytes(), 0o600)
	got, err := PNGBytes(pp)
	if err != nil || !bytes.Equal(got, pb.Bytes()) {
		t.Fatalf("PNG 应原样返回：%v", err)
	}
	got, err = PNGBytes(jp)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(got))
	if err != nil || dec.Bounds().Dx() != 3 || dec.Bounds().Dy() != 2 {
		t.Fatalf("JPEG 转出的不是同尺寸的 PNG：%v", err)
	}
	if _, err := PNGBytes(filepath.Join(dir, "missing.png")); err == nil {
		t.Fatal("文件不在时要报错")
	}
}

func TestCodexEmbeddedCopies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex", "generated_images", "session")
	os.MkdirAll(dir, 0o700)
	encoded, raw := pngBase64(t, 12, 8)
	duplicate := filepath.Join(dir, "exec-copy.png")
	os.WriteFile(duplicate, raw, 0o600)
	other := filepath.Join(dir, "ig-other.png")
	writePNG(t, other, 4, 2)
	transcript := filepath.Join(home, "rollout.jsonl")
	line := `{"type":"response_item","payload":{"type":"custom_tool_call_output","output":[{"type":"input_image","image_url":"data:image/png;base64,` + encoded + `"}]}}` + "\n"
	os.WriteFile(transcript, []byte(line+line), 0o600)
	s := NewScanner("codex", "session", transcript)
	check := func(want int) []Item {
		t.Helper()
		pasted, made := s.Scan()
		if len(pasted) != 0 || len(made) != want {
			t.Fatalf("pasted=%d made=%+v", len(pasted), made)
		}
		return made
	}
	made := check(3) // 两次查看都留着，只隐藏目录副本。
	var snapshot Item
	count := 0
	for _, it := range made {
		if it.Path == duplicate {
			t.Fatal("directory duplicate kept")
		}
		if it.Tool != nil {
			snapshot = it
			count++
		}
	}
	if count != 2 {
		t.Fatal("repeated views were merged")
	}
	check(3)
	if len(s.digests) != 2 {
		t.Fatal("directory hashes not cached")
	}
	// 原文件被覆盖或删除后，仍能读回当时的图片；目录内容改变后重新参与列表。
	writePNG(t, duplicate, 17, 19)
	check(4)
	os.Remove(duplicate)
	check(3)
	if b, err := ReadImage(snapshot); err != nil || !bytes.Equal(b, raw) {
		t.Fatalf("snapshot changed: %v", err)
	}
	img, err := Decode(snapshot, 512)
	if err != nil || img.Bounds().Dx() != 12 || img.Bounds().Dy() != 8 {
		t.Fatalf("decode failed: %v", err)
	}
	// 会话重写后旧摘要不能继续隐藏目录中的文件。
	os.WriteFile(duplicate, raw, 0o600)
	os.WriteFile(transcript, []byte("{}\n"), 0o600)
	check(2)
}

func TestCodexSnapshotRewrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	first, _ := pngBase64(t, 12, 8)
	second, raw := pngBase64(t, 8, 12)
	write := func(data string) {
		t.Helper()
		line := `{"type":"response_item","payload":{"type":"custom_tool_call_output","output":[{"type":"input_image","image_url":"data:image/png;base64,` + data + `"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(first)
	s := NewScanner("codex", "test", path)
	_, made := s.Scan()
	if len(made) != 1 {
		t.Fatalf("images=%d", len(made))
	}
	old := made[0]
	write(second)
	if _, err := ReadImage(old); err == nil {
		t.Error("stale snapshot read a replacement image at the same offset")
	}
	_, made = s.Scan()
	if len(made) != 1 {
		t.Fatalf("images after rewrite=%d", len(made))
	}
	if made[0].Key == old.Key {
		t.Error("replacement reused the old thumbnail cache key")
	}
	if got, err := ReadImage(made[0]); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("replacement read: %v", err)
	}
}
