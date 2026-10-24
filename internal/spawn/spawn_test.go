package spawn

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStart(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	extraR, extraW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer extraR.Close()
	// 一个不该被继承的描述符：子进程里探测 fd 不应打开
	leak, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer leak.Close()

	script := `echo "$PWD"; echo $$ $(ps -o sess= -p $$ | tr -d ' ') $(ps -o pgid= -p $$ | tr -d ' '); echo extra >&3; ` +
		`if (: >&` + strconv.Itoa(int(leak.Fd())) + `) 2>/dev/null; then echo leaked; else echo closed; fi`
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.ExtraFiles = []*os.File{extraW}
	p, err := Start(cmd)
	extraW.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.Wait()
	if err != nil || !st.Success() {
		t.Fatalf("wait: %v %v", st, err)
	}
	extra, _ := io.ReadAll(extraR)
	if string(extra) != "extra\n" {
		t.Fatalf("extra fd: %q", extra)
	}
	b, _ := os.ReadFile(out.Name())
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("output: %q", b)
	}
	if lines[0] != dir {
		t.Fatalf("dir %q, want %q", lines[0], dir)
	}
	// 新会话：进程组号等于自己的 pid
	if f := strings.Fields(lines[1]); len(f) != 3 || f[0] != f[2] {
		t.Fatalf("not a session leader: %q", lines[1])
	}
	if lines[2] != "closed" {
		t.Fatalf("fd %d leaked into child", leak.Fd())
	}
}
