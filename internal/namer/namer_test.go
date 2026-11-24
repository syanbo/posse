package namer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var day = time.Date(2026, 9, 4, 10, 0, 0, 0, time.Local)

func TestNormalize(t *testing.T) {
	long := strings.Repeat("长", MaxSummary(false))
	for _, c := range []struct {
		in   string
		en   bool
		want string
	}{
		{"0101｜修复｜登录按钮样式", false, "0904｜修复｜登录按钮样式"}, // 日期换成今天
		{"文档｜补充 README", false, "0904｜文档｜补充 README"},
		{"fix | login button", false, "0904｜修复｜login button"},
		{"0904|功能|会话命名", false, "0904｜开发｜会话命名"},
		{"0904｜feat｜会话命名", false, "0904｜开发｜会话命名"},
		{"0904｜配置｜接上 Posse", false, "0904｜杂项｜接上 Posse"},
		{"【调研】｜resume 数据源", false, "0904｜调研｜resume 数据源"},
		{"0904｜调研｜" + long, false, "0904｜调研｜" + long},
		// 类型词后面跟着分隔符才算重复；连着写的是词的一部分
		{"0904｜修复｜修复：登录按钮样式", false, "0904｜修复｜登录按钮样式"},
		{"0904｜修复｜修复率统计", false, "0904｜修复｜修复率统计"},
		{"0904｜开发｜开发者工具接入", false, "0904｜开发｜开发者工具接入"},
		{"0904｜文档｜文档", false, "0904｜文档｜文档"},
		{"  0904｜杂项｜整理  目录。 ", false, "0904｜杂项｜整理 目录"},
		{"0904｜未知｜xx", false, ""},
		{"登录按钮样式", false, ""},
		{"0904｜修复", false, ""},
		{"0904｜调研｜" + long + "长", false, ""},
		{"0904｜重构｜拆分路由\n说明", false, ""},
		{"   ", false, ""},
		// 英文界面：类型写英文，中文类型词也认，摘要放宽到 40 个字符
		{"0101｜fix｜login button style", true, "0904｜fix｜login button style"},
		{"0904｜修复｜login button", true, "0904｜fix｜login button"},
		{"0904|Feature|session names", true, "0904｜feat｜session names"},
		{"0904｜配置｜wire up Posse", true, "0904｜chore｜wire up Posse"},
		{"0904｜fix｜fix: login button", true, "0904｜fix｜login button"},
		{"0904｜fix｜fixture loader", true, "0904｜fix｜fixture loader"},
		{"0904｜docs｜" + strings.Repeat("a", MaxSummary(true)), true, "0904｜docs｜" + strings.Repeat("a", MaxSummary(true))},
		{"0904｜docs｜" + strings.Repeat("a", MaxSummary(true)+1), true, ""},
	} {
		got, err := Normalize(c.in, day, c.en)
		if got != c.want || (c.want == "") != errors.Is(err, ErrFormat) {
			t.Errorf("Normalize(%q, en=%v) = %q, %v; want %q", c.in, c.en, got, err, c.want)
		}
	}
}

func TestWriteCodex(t *testing.T) {
	home := t.TempDir()
	// 旧版本的库留着也不该被选中
	os.WriteFile(filepath.Join(home, "state_4.sqlite"), nil, 0o644)
	path := filepath.Join(home, "state_5.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT NOT NULL, history_mode TEXT NOT NULL, name TEXT)`,
		`INSERT INTO threads VALUES ('t1', '首条消息', 'paginated', '自动标题')`,
		`INSERT INTO threads VALUES ('t2', '旧标题', 'legacy', NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	// session_index 末尾缺换行时先补上
	index := filepath.Join(home, "session_index.jsonl")
	os.WriteFile(index, []byte(`{"id":"old"}`), 0o644)

	const title = "0904｜feat｜会话命名"
	if err := WriteCodex(home, "t1", title); err != nil {
		t.Fatal(err)
	}
	if err := WriteCodex(home, "t2", title); err != nil {
		t.Fatal(err)
	}
	if err := WriteCodex(home, "missing", title); err == nil || !strings.Contains(err.Error(), "no thread missing") {
		t.Fatalf("missing thread: %v", err)
	}
	db, _ = sql.Open("sqlite", path)
	defer db.Close()
	row := func(id string) (name sql.NullString, ttl string) {
		db.QueryRow(`SELECT name, title FROM threads WHERE id = ?`, id).Scan(&name, &ttl)
		return
	}
	if n, ttl := row("t1"); n.String != title || ttl != "首条消息" {
		t.Fatalf("paginated %q %q", n.String, ttl)
	}
	if n, ttl := row("t2"); n.Valid || ttl != title {
		t.Fatalf("legacy %v %q", n, ttl)
	}
	lines := strings.Split(strings.TrimSpace(readFile(t, index)), "\n")
	if len(lines) != 3 {
		t.Fatalf("index %q", lines)
	}
	var e struct{ ID, ThreadName, UpdatedAt string }
	json.Unmarshal([]byte(strings.NewReplacer("thread_name", "threadname", "updated_at", "updatedat").Replace(lines[1])), &e)
	if e.ID != "t1" || e.ThreadName != title || len(e.UpdatedAt) != len("2026-09-04T10:00:00.000000Z") {
		t.Fatalf("entry %+v", e)
	}
}

func TestWriteGrok(t *testing.T) {
	home, cwd := t.TempDir(), "/Users/me/my proj"
	write := func(group string) string {
		p := filepath.Join(home, "sessions", group, "s1", "summary.json")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(`{"generated_title":"自动标题","title_is_manual":false,"num_messages":4}`), 0o600)
		return p
	}
	const title = "0904｜调研｜resume 数据源"
	p := write("%2FUsers%2Fme%2Fmy%20proj")
	if err := WriteGrok(home, "s1", cwd, title); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal([]byte(readFile(t, p)), &got)
	if got["generated_title"] != title || got["session_summary"] != title || got["title_is_manual"] != true || got["num_messages"] != 4.0 {
		t.Fatalf("summary %v", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// 目录名对不上时按会话 ID 找
	os.RemoveAll(filepath.Join(home, "sessions"))
	p = write("long-path-slug-1a2b3c")
	if err := WriteGrok(home, "s1", "/elsewhere", title); err != nil || !strings.Contains(readFile(t, p), title) {
		t.Fatalf("by id: %v", err)
	}
	os.WriteFile(p, []byte("[1, 2]"), 0o600)
	if err := WriteGrok(home, "s1", cwd, title); err == nil || readFile(t, p) != "[1, 2]" {
		t.Fatalf("non-object overwritten: %v", err)
	}
	if err := WriteGrok(home, "nope", cwd, title); err == nil {
		t.Fatal("missing session")
	}
}

func TestWriteAntigravity(t *testing.T) {
	const sid = "ce5e30fa-8b92-42ae-8769-e19e80bdc9da"
	const title = "1124｜修复｜原生标题"
	newServer := func(allowPath string) (*httptest.Server, *[]string) {
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.URL.Path != allowPath {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("X-Antigravity-CSRF-Token") != "tok" {
				http.Error(w, "", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": sid, "annotations": map[string]string{"title": title}})
		}))
		t.Cleanup(srv.Close)
		return srv, &paths
	}
	// 新版 agy 1.5.16+：直接命中新路径，不发多余请求。
	srv, paths := newServer("/product.v1.ConversationService/UpdateConversation")
	if err := WriteAntigravity(context.Background(), srv.Listener.Addr().String(), "tok", sid, title); err != nil {
		t.Fatalf("new endpoint: %v", err)
	}
	if !slices.Equal(*paths, []string{"/product.v1.ConversationService/UpdateConversation"}) {
		t.Fatalf("new endpoint paths = %v", *paths)
	}
	// 非 404 错误（如 401）不回退旧路径。
	*paths = nil
	if err := WriteAntigravity(context.Background(), srv.Listener.Addr().String(), "bad", sid, title); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("expected HTTP 401, got %v", err)
	}
	if !slices.Equal(*paths, []string{"/product.v1.ConversationService/UpdateConversation"}) {
		t.Fatalf("401 should not fall back, paths = %v", *paths)
	}
	// 旧版 agy：新路径 404 时回退到 jetski.product.v1 路径。
	legacySrv, legacyPaths := newServer("/jetski.product.v1.ConversationService/UpdateConversation")
	if err := WriteAntigravity(context.Background(), legacySrv.Listener.Addr().String(), "tok", sid, title); err != nil {
		t.Fatalf("legacy endpoint fallback: %v", err)
	}
	if !slices.Equal(*legacyPaths, antigravityUpdatePaths) {
		t.Fatalf("legacy fallback paths = %v", *legacyPaths)
	}
	// 非回环地址拒绝请求。
	if err := WriteAntigravity(context.Background(), "example.com:8080", "tok", sid, title); err == nil {
		t.Fatal("non-loopback address accepted")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
