package files

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileKinds(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "hello\r\nworld\n")
	write(t, filepath.Join(dir, "b.bin"), "PK\x00\x01")
	c, err := ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || c.Kind != Text || c.Text != "hello\r\nworld\n" || c.ReadOnly {
		t.Fatalf("text: %+v %v", c, err)
	}
	c, err = ReadFile(filepath.Join(dir, "b.bin"))
	if err != nil || c.Kind != Binary || c.Text != "" {
		t.Fatalf("binary: %+v %v", c, err)
	}
	c, err = ReadFile(dir)
	if err != nil || c.Kind != Directory {
		t.Fatalf("dir: %+v %v", c, err)
	}
	if _, err := ReadFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file read without error")
	}
	big := filepath.Join(dir, "big.txt")
	write(t, big, strings.Repeat("x", ReadOnlySize+1))
	if c, _ := ReadFile(big); c.Kind != Text || !c.ReadOnly {
		t.Fatalf("big file should open read-only: kind=%v ro=%v", c.Kind, c.ReadOnly)
	}
}

// 保存要保留权限位，写完的磁盘戳要和随后 stat 的一致（据此忽略自己这次写入），
// 符号链接写到它指向的文件，链接本身不被替换。
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")
	write(t, p, "old")
	os.Chmod(p, 0o755)
	before := StatFile(p)
	st, err := WriteFile(p, func(w io.Writer) error { _, err := io.WriteString(w, "#!/bin/sh\necho hi\n"); return err })
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if !st.Same(StatFile(p)) || st.Same(before) {
		t.Fatal("stamp after write should match the file and differ from before")
	}
	link := filepath.Join(dir, "link.sh")
	os.Symlink(p, link)
	if _, err := WriteFile(link, func(w io.Writer) error { _, err := io.WriteString(w, "via link"); return err }); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a file")
	}
	if b, _ := os.ReadFile(p); string(b) != "via link" {
		t.Fatalf("target content %q", b)
	}
	// 写失败时不留临时文件，原文件不变
	if _, err := WriteFile(p, func(w io.Writer) error { return io.ErrUnexpectedEOF }); err == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(p); string(b) != "via link" {
		t.Fatalf("file changed after failed write: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// agent 常用"写临时文件再改名"保存：磁盘戳要能看出变化，哪怕大小没变。
func TestStampSeesRenameReplace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	write(t, p, "package a")
	before := StatFile(p)
	tmp := filepath.Join(dir, "a.go.tmp")
	write(t, tmp, "package b")
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	if before.Same(StatFile(p)) {
		t.Fatal("rename-replace not detected")
	}
	os.Remove(p)
	if !StatFile(p).Missing || before.Same(StatFile(p)) {
		t.Fatal("removal not detected")
	}
}

func TestTreeRowsAndExpansion(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "b.txt"), "")
	write(t, filepath.Join(root, "A.txt"), "")
	write(t, filepath.Join(root, "src", "main.go"), "")
	write(t, filepath.Join(root, "src", "lib", "x.go"), "")
	write(t, filepath.Join(root, ".git", "HEAD"), "")
	tr := NewTree(root)
	load := func(dirs []string) {
		for _, d := range dirs {
			es, st, err := ReadDir(d)
			tr.Set(d, es, st, err)
		}
	}
	load(tr.SetExpanded(root, true))
	names := func() []string {
		var out []string
		for _, r := range tr.Rows() {
			out = append(out, strings.Repeat(" ", r.Depth)+r.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"src", "A.txt", "b.txt"}) {
		t.Fatalf("root rows %q", got)
	}
	load(tr.SetExpanded(filepath.Join(root, "src"), true))
	if got := names(); !slices.Equal(got, []string{"src", " lib", " main.go", "A.txt", "b.txt"}) {
		t.Fatalf("expanded rows %q", got)
	}
	// 收起上级后，下级的展开状态还在，但不再算看得见
	load(tr.SetExpanded(filepath.Join(root, "src", "lib"), true))
	tr.SetExpanded(filepath.Join(root, "src"), false)
	if v := tr.Visible(); !slices.Equal(v, []string{root}) {
		t.Fatalf("visible %q", v)
	}
	// Reveal 展开各级上级并报告要读的目录
	tr2 := NewTree(root)
	need := tr2.Reveal(filepath.Join(root, "src", "lib", "x.go"))
	want := []string{root, filepath.Join(root, "src"), filepath.Join(root, "src", "lib")}
	if !slices.Equal(need, want) {
		t.Fatalf("reveal needs %q, want %q", need, want)
	}
	// 目录被删掉：Set 报错后它和下面的展开状态都去掉
	tr.Set(filepath.Join(root, "src"), nil, Stamp{}, os.ErrNotExist)
	if slices.Contains(tr.Expansions(), filepath.Join(root, "src", "lib")) {
		t.Fatal("expansion of removed dir kept")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestListFilesGitIgnore(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	write(t, filepath.Join(root, ".gitignore"), "build/\n*.log\n")
	write(t, filepath.Join(root, "main.go"), "package main")
	write(t, filepath.Join(root, "build", "out.bin"), "x")
	write(t, filepath.Join(root, "app.log"), "x")
	write(t, filepath.Join(root, "pkg", "util.go"), "package pkg")
	// 指向目录的符号链接：git 当一个文件记，树里是目录，不算忽略
	os.Symlink(t.TempDir(), filepath.Join(root, "shared"))
	git(t, root, "add", "main.go")
	l, err := ListFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Git || !slices.Equal(l.Files, []string{".gitignore", "main.go", filepath.Join("pkg", "util.go"), "shared"}) {
		t.Fatalf("files %q git=%v", l.Files, l.Git)
	}
	cases := []struct {
		rel  string
		dir  bool
		want bool
	}{
		{"main.go", false, false}, {"app.log", false, true}, {"build", true, true},
		{"pkg", true, false}, {filepath.Join("build", "out.bin"), false, true}, {"shared", true, false},
	}
	for _, c := range cases {
		if got := l.Ignored(filepath.Join(root, c.rel), c.dir); got != c.want {
			t.Errorf("Ignored(%s) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestListFilesWalk(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "")
	write(t, filepath.Join(root, "node_modules", "x", "index.js"), "")
	write(t, filepath.Join(root, "docs", "b.md"), "")
	l, err := ListFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if l.Git || !slices.Equal(l.Files, []string{"a.txt", filepath.Join("docs", "b.md")}) {
		t.Fatalf("files %q git=%v", l.Files, l.Git)
	}
	if l.Ignored(filepath.Join(root, "node_modules"), true) {
		t.Fatal("nothing is ignored outside git")
	}
}

// 工作区下面有几个仓库：各仓库按自己的忽略规则列，软链过去的仓库也列，仓库外面的普通目录遍历。
func TestListRepos(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	a := filepath.Join(root, "a")
	ext := filepath.Join(base, "ext")
	for _, r := range []string{a, ext} {
		os.MkdirAll(r, 0o755)
		git(t, r, "init", "-q")
		write(t, filepath.Join(r, ".gitignore"), "*.log\n")
		write(t, filepath.Join(r, "main.go"), "package main")
		write(t, filepath.Join(r, "x.log"), "x")
	}
	// a 里嵌着一个仓库：外层报成 nested/，它的文件由它自己列
	nested := filepath.Join(a, "nested")
	os.MkdirAll(nested, 0o755)
	git(t, nested, "init", "-q")
	write(t, filepath.Join(nested, "n.go"), "package n")
	if err := os.Symlink(ext, filepath.Join(root, "ext")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "notes", "todo.md"), "")
	write(t, filepath.Join(root, "notes", "x.log"), "")
	l, err := ListRepos(context.Background(), root, []string{a, nested, filepath.Join(root, "ext")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join("a", ".gitignore"), filepath.Join("a", "main.go"), filepath.Join("a", "nested", "n.go"),
		filepath.Join("ext", ".gitignore"), filepath.Join("ext", "main.go"),
		filepath.Join("notes", "todo.md"), filepath.Join("notes", "x.log"),
	}
	if !l.Git || !slices.Equal(l.Files, want) {
		t.Fatalf("files %q", l.Files)
	}
	cases := []struct {
		rel  string
		dir  bool
		want bool
	}{
		{filepath.Join("a", "x.log"), false, true}, {filepath.Join("ext", "x.log"), false, true},
		{filepath.Join("notes", "x.log"), false, false}, {"notes", true, false}, {"a", true, false},
		{"ext", true, false}, {filepath.Join("a", "nested"), true, false},
	}
	for _, c := range cases {
		if got := l.Ignored(filepath.Join(root, c.rel), c.dir); got != c.want {
			t.Errorf("Ignored(%s) = %v, want %v", c.rel, got, c.want)
		}
	}
	// 只有一个仓库、就是工作区本身：和 ListFiles 一样
	l, err = ListRepos(context.Background(), a, []string{a})
	if err != nil || !slices.Equal(l.Files, []string{".gitignore", "main.go"}) {
		t.Fatalf("single files %q %v", l.Files, err)
	}
	// 工作区本身是仓库、里面还嵌着仓库：外层照它的 .gitignore 列，被忽略的不列、照样算忽略
	l, err = ListRepos(context.Background(), a, []string{a, nested})
	if want := []string{".gitignore", "main.go", filepath.Join("nested", "n.go")}; err != nil || !slices.Equal(l.Files, want) {
		t.Fatalf("root repo with nested: %q %v", l.Files, err)
	}
	if !l.Ignored(filepath.Join(a, "x.log"), false) || l.Ignored(filepath.Join(a, "nested"), true) {
		t.Fatal("ignore rules of the root repo lost")
	}
}

func TestFuzzyRanking(t *testing.T) {
	files := []string{
		"internal/app/view.go",
		"internal/app/files_view.go",
		"third_party/mygo/ui/editor/view.go",
		"internal/vt/render.go",
		"docs/overview.md",
	}
	got := Fuzzy(files, "view", 10)
	if len(got) != 4 {
		t.Fatalf("matches %v", got)
	}
	// 文件名以 query 开头、路径短的排第一
	if got[0].Path != "internal/app/view.go" {
		t.Fatalf("first %q", got[0].Path)
	}
	if m := Fuzzy(files, "iavg", 10); len(m) == 0 || m[0].Path != "internal/app/view.go" {
		t.Fatalf("initials: %v", m)
	}
	m := Fuzzy(files, "VIEW", 1)
	if len(m) != 1 || m[0].Path != "internal/app/view.go" {
		t.Fatalf("limit/case: %v", m)
	}
	for i, p := range m[0].Pos {
		if lowerByte(m[0].Path[p]) != "view"[i] {
			t.Fatalf("pos %v doesn't point at query", m[0].Pos)
		}
	}
	if n := len(Fuzzy(files, "", 3)); n != 3 {
		t.Fatalf("empty query returns %d", n)
	}
	if p, line := SplitLine("view.go:42"); p != "view.go" || line != 42 {
		t.Fatalf("SplitLine = %q %d", p, line)
	}
	if p, line := SplitLine("a:b"); p != "a:b" || line != 0 {
		t.Fatalf("SplitLine no line = %q %d", p, line)
	}
}

func BenchmarkFuzzy100k(b *testing.B) {
	files := make([]string, 100000)
	for i := range files {
		files[i] = fmt.Sprintf("pkg%d/sub%d/module_%d/file_%d.go", i%97, i%13, i%1000, i)
	}
	for b.Loop() {
		Fuzzy(files, "mod12fil", 50)
	}
}

// 限定目录：清单里的只搜那个目录；被忽略的目录不在清单里，用 ListDir 现列，依赖目录也照搜
func TestSearchDir(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "Hello")
	write(t, filepath.Join(root, "app", "b.go"), "Hello")
	write(t, filepath.Join(root, "apple", "c.go"), "Hello")
	write(t, filepath.Join(root, "dist", "node_modules", "d.js"), "Hello")
	write(t, filepath.Join(root, "dist", ".git", "e"), "Hello")
	paths := func(l *List, q Query) []string {
		var out []string
		if _, err := Search(context.Background(), l, q, nil, func(h []Hit) {
			for _, x := range h {
				out = append(out, filepath.ToSlash(x.Path))
			}
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	l, _ := ListFiles(context.Background(), root)
	if got := paths(l, Query{Text: "hello", Dir: "app"}); !slices.Equal(got, []string{"app/b.go"}) {
		t.Fatalf("dir app: %q", got)
	}
	d, err := ListDir(context.Background(), root, "dist")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(d, Query{Text: "hello", Dir: "dist"}); !slices.Equal(got, []string{"dist/node_modules/d.js"}) {
		t.Fatalf("ignored dir: %q", got)
	}
	all, _ := ListAll(context.Background(), root)
	if got := paths(all, Query{Text: "hello"}); !slices.Equal(got, []string{"a.go", "app/b.go", "apple/c.go"}) {
		t.Fatalf("all: %q", got) // dist 下只有依赖目录和 .git，都跳过
	}
	if _, err := ListDir(context.Background(), root, "gone"); err == nil {
		t.Fatal("missing dir should fail")
	}
}

func TestSearch(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a\n\nfunc Hello() {}\n// hello again, hello\n")
	write(t, filepath.Join(root, "b.md"), "Hello 世界\n")
	write(t, filepath.Join(root, "c.bin"), "Hello\x00")
	write(t, filepath.Join(root, "vendor", "v.go"), "Hello")
	l, _ := ListFiles(context.Background(), root)
	run := func(q Query, overlay map[string]string) []Hit {
		var out []Hit
		if _, err := Search(context.Background(), l, q, overlay, func(h []Hit) { out = append(out, h...) }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	hits := run(Query{Text: "hello"}, nil)
	var got []string
	for _, h := range hits {
		got = append(got, fmt.Sprintf("%s:%d:%v", filepath.ToSlash(h.Path), h.Line, h.Spans))
	}
	want := []string{"a.go:2:[[5 10]]", "a.go:3:[[3 8] [16 21]]", "b.md:0:[[0 5]]", "vendor/v.go:0:[[0 5]]"}
	if !slices.Equal(got, want) {
		t.Fatalf("hits\n got %q\nwant %q", got, want)
	}
	if n := len(run(Query{Text: "hello", MatchCase: true}, nil)); n != 1 {
		t.Fatalf("match case: %d hits", n)
	}
	if n := len(run(Query{Text: `H\w+o\(`, Regex: true, MatchCase: true}, nil)); n != 1 {
		t.Fatalf("regex: %d hits", n)
	}
	if hs := run(Query{Text: "hello", Include: "*.go", Exclude: "vendor"}, nil); len(hs) != 2 || hs[0].Path != "a.go" {
		t.Fatalf("globs: %v", hs)
	}
	// 打开着的文件搜编辑器里的内容
	if hs := run(Query{Text: "unsaved"}, map[string]string{"b.md": "unsaved text"}); len(hs) != 1 || hs[0].Path != "b.md" {
		t.Fatalf("overlay: %v", hs)
	}
	if _, err := Search(context.Background(), l, Query{Text: "(", Regex: true}, nil, func([]Hit) {}); err == nil {
		t.Fatal("bad regex should error")
	}
	// 取消后尽快返回
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, l, Query{Text: "hello"}, nil, func([]Hit) {}); err == nil {
		t.Fatal("canceled search should report it")
	}
}

func TestSearchTruncates(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "many.txt"), strings.Repeat("x\n", MaxHits+10))
	l, _ := ListFiles(context.Background(), root)
	n := 0
	trunc, err := Search(context.Background(), l, Query{Text: "x"}, nil, func(h []Hit) { n += len(h) })
	if err != nil || !trunc || n != MaxHits {
		t.Fatalf("trunc=%v n=%d err=%v", trunc, n, err)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		g, rel string
		want   bool
	}{
		{"*.go", "internal/app/view.go", true},
		{"*.go", "README.md", false},
		{"internal/app/", "internal/app/view.go", true},
		{"internal/app", "internal/app/view.go", true},
		{"internal/*.go", "internal/x.go", true},
		{"**/testdata", "a/testdata/b.txt", true},
		{"node_modules", "web/node_modules/x/y.js", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.g, c.rel); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v", c.g, c.rel, got)
		}
	}
}

// 符号链接带上它指向的路径：指向目录的照样算目录、排在前面，指向文件的算文件。
func TestReadDirSymlinks(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	write(t, filepath.Join(other, "lib", "x.go"), "")
	write(t, filepath.Join(other, "notes.md"), "")
	write(t, filepath.Join(root, "plain.txt"), "")
	os.Mkdir(filepath.Join(root, "src"), 0o755)
	if err := os.Symlink(filepath.Join(other, "lib"), filepath.Join(root, "lib")); err != nil {
		t.Skip("symlink unavailable:", err)
	}
	os.Symlink("../notes.md", filepath.Join(root, "notes.md"))
	es, _, err := ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{Name: "lib", Dir: true, Link: filepath.Join(other, "lib")}, {Name: "src", Dir: true},
		{Name: "notes.md", Link: "../notes.md"}, {Name: "plain.txt"}}
	if !slices.Equal(es, want) {
		t.Fatalf("entries %+v", es)
	}
	tr := NewTree(root)
	tr.Set(root, es, Stamp{}, nil)
	tr.SetExpanded(root, true)
	if r := tr.Rows()[0]; r.Name != "lib" || r.Link == "" {
		t.Fatalf("row %+v should carry the link", r)
	}
}
