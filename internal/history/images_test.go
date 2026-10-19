package history

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestImageScanner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	img := func(data string) string {
		return fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"%s"}}`, base64.StdEncoding.EncodeToString([]byte(data)))
	}
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/tmp/a.png"}}]}}`,
		`{"type":"user","timestamp":"2026-10-19T06:43:23.481Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[` + img("one") + `]}]}}`,
		// 请求的副本，不算
		`{"type":"api-request-blob","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[` + img("dup") + `]}]}}`,
		// 用户粘贴的图片，不算
		`{"type":"user","message":{"content":[` + img("pasted") + `]}}`,
	}
	write := func(ls ...string) {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		for _, l := range ls {
			f.WriteString(l + "\n")
		}
		f.Close()
	}
	write(lines...)
	s := NewImageScanner(path)
	got, err := s.Scan()
	if err != nil || len(got) != 1 || got[0].Tool != "Read" || got[0].Path != "/tmp/a.png" || got[0].At.IsZero() {
		t.Fatalf("first scan %+v %v", got, err)
	}
	// 增量：新的截图，一行里两张
	write(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"mcp__iphone-use__phone_screenshot","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"ok"},`+img("two")+`,`+img("three")+`]}]}}`)
	got, _ = s.Scan()
	if len(got) != 3 || got[2].Tool != "mcp__iphone-use__phone_screenshot" || got[2].Index != 1 {
		t.Fatalf("second scan %+v", got)
	}
	for i, want := range []string{"one", "two", "three"} {
		b, err := ReadToolImage(path, got[i])
		if err != nil || string(b) != want {
			t.Fatalf("image %d = %q %v", i, b, err)
		}
	}
}
