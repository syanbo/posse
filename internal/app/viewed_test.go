package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func execLine(t *testing.T, js string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "input": js}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func imageViewLine(t *testing.T, kind, path string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": kind, "item": map[string]any{"type": "ImageView", "id": "exec-1", "path": path}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// Codex 的会话文件里 exec 的 view_image 调用带着完整路径：双引号（带转义）、单引号、相对路径都认，
// 没写完的行留到下一次，别的行里提到 view_image 不算。
func TestReadViewedImages(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "rollout.jsonl")
	first := execLine(t, `image((await tools.view_image({path:"/out/set 1/01-crop.png"})).image_url);`) +
		`{"type":"response_item","payload":{"type":"message","content":[{"type":"output_text","text":"view_image({path:\"/no/such.png\"})"}]}}` + "\n" +
		execLine(t, `const a = await tools.view_image({ path: 'shots/02.png' }); const b = await tools.view_image({path:"/out/a\"b.png"}); await tools.view_image({path: p});`)
	half := execLine(t, `tools.view_image({path:"/out/03.png"})`)
	os.WriteFile(transcript, []byte(first+half[:len(half)-5]), 0o644)

	got, next := readViewedImages(transcript, 0, "/base")
	want := []string{"/out/set 1/01-crop.png", "/base/shots/02.png", `/out/a"b.png`}
	if len(got) != len(want) || next != int64(len(first)) {
		t.Fatalf("got %q next %d, want %q next %d", got, next, want, len(first))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	os.WriteFile(transcript, []byte(first+half), 0o644)
	if got, next := readViewedImages(transcript, next, "/base"); len(got) != 1 || got[0] != "/out/03.png" || next != int64(len(first)+len(half)) {
		t.Fatalf("after the line was finished: %q next %d", got, next)
	}
	// 新版 Codex 看完图有 ImageView 事件，代码里拼出来的路径也在：file:// 要解码，相对的、别的事件不算
	events := execLine(t, `for (const n of names) image((await tools.view_image({path: dir + "/" + n})).image_url);`) +
		imageViewLine(t, "item_completed", "file:///tmp/set%201/04-follow.jpg") +
		imageViewLine(t, "item_completed", "/tmp/plain.png") +
		imageViewLine(t, "item_completed", "rel.png") +
		imageViewLine(t, "item_started", "file:///tmp/started.png")
	os.WriteFile(transcript, []byte(first+half+events), 0o644)
	if got, _ := readViewedImages(transcript, int64(len(first)+len(half)), ""); len(got) != 2 || got[0] != "/tmp/set 1/04-follow.jpg" || got[1] != "/tmp/plain.png" {
		t.Fatalf("ImageView events: %q", got)
	}
	// 文件换成更短的：从头读
	os.WriteFile(transcript, []byte(half), 0o644)
	if got, _ := readViewedImages(transcript, next, ""); len(got) != 1 || got[0] != "/out/03.png" {
		t.Fatalf("after the file was replaced: %q", got)
	}
}

// Codex 终端里只打印了文件名的图片（Viewed image 04-home.png）：终端目录下没有这个文件时，
// 按会话里看过的路径找到它；同名的取最后看的那张；别的 agent、没看过的、已经删掉的不算链接。
func TestViewedImageLink(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	os.MkdirAll(filepath.Join(out, "set-1"), 0o755)
	os.MkdirAll(filepath.Join(out, "set-2"), 0o755)
	one, two := filepath.Join(out, "set-1", "04-home.png"), filepath.Join(out, "set-2", "04-home.png")
	gone := filepath.Join(out, "set-2", "05-gone.png")
	os.WriteFile(one, []byte("x"), 0o644)
	os.WriteFile(two, []byte("x"), 0o644)
	transcript := filepath.Join(dir, "rollout.jsonl")
	var lines string
	for _, p := range []string{one, two, gone} {
		lines += execLine(t, `image((await tools.view_image({path:"`+p+`"})).image_url);`)
	}
	os.WriteFile(transcript, []byte(lines), 0o644)
	cwd := filepath.Join(dir, "cwd")
	os.MkdirAll(cwd, 0o755)
	os.WriteFile(filepath.Join(cwd, "here.png"), []byte("x"), 0o644)

	a := previewApp(t)
	a.workspaces[0].Terminals = append(a.workspaces[0].Terminals,
		terminalInfo{ID: "tx", Agent: "codex", AgentState: &agentState{SessionID: "s", TranscriptPath: transcript}},
		terminalInfo{ID: "tc", Agent: "claude", AgentState: &agentState{SessionID: "s", TranscriptPath: transcript}})
	codex, claude := &termView{id: "tx", dir: cwd}, &termView{id: "tc", dir: cwd}

	// 第一次问时还没读会话文件：先不是链接，后台读完后就是了
	if a.linkUsable(codex, "04-home.png") {
		t.Fatal("usable before the session file was read")
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.viewed["tx"] == nil || a.viewed["tx"].busy {
		if time.Now().After(deadline) {
			t.Fatal("session file never read")
		}
		a.runPending()
		time.Sleep(5 * time.Millisecond)
	}
	if p, local := a.linkPath(codex, "04-home.png"); p != two || !local {
		t.Fatalf("04-home.png = %q, want the one viewed last %q", p, two)
	}
	if !a.linkUsable(codex, "04-home.png") {
		t.Fatal("still cached as unusable after the session file was read")
	}
	if p, _ := a.linkPath(codex, "set-1/04-home.png:3"); p != one {
		t.Fatalf("set-1/04-home.png:3 = %q, want %q", p, one)
	}
	if p, _ := a.linkPath(codex, "here.png"); p != filepath.Join(cwd, "here.png") {
		t.Fatalf("a file under the terminal's directory = %q", p)
	}
	for _, link := range []string{"05-gone.png", "home.png", "99-none.png", "../04-home.png", "04-home.txt"} {
		if p, _ := a.linkPath(codex, link); p != "" {
			t.Fatalf("%s = %q, want none", link, p)
		}
	}
	if p, _ := a.linkPath(claude, "04-home.png"); p != "" {
		t.Fatalf("in a claude terminal = %q, want none", p)
	}
}
