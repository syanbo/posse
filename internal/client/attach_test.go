package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"

	"posse/internal/daemon"
)

// startDaemon 在进程内运行常驻进程。socket 路径有长度上限，所以不用 t.TempDir()。
func startDaemon(t *testing.T) *Client {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "hv")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := daemon.Run(ctx, daemon.Config{Home: home}); err != nil {
			t.Errorf("daemon: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		os.RemoveAll(home)
	})
	c := &Client{Home: home, Socket: daemon.SocketPath(home)}
	for i := 0; i < 100 && !c.Up(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	return c
}

func createTerminal(t *testing.T, c *Client, command ...string) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	var p struct{ ID string }
	if err := c.Call(ctx, "project.add", map[string]any{"path": dir, "name": filepath.Base(dir)}, &p); err != nil {
		t.Fatal(err)
	}
	var term struct{ ID string }
	if err := c.Call(ctx, "terminal.create", map[string]any{"workspace": filepath.Base(dir) + "/main", "command": command}, &term); err != nil {
		t.Fatal(err)
	}
	return term.ID
}

func waitText(t *testing.T, ui *terminal.Terminal, want string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if text := ui.Text(); strings.Contains(text, want) {
			return text
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; screen:\n%s", want, ui.Text())
	return ""
}

func TestAttachSnapshotStreamAndQueries(t *testing.T) {
	c := startDaemon(t)
	// 先输出带颜色的内容进回滚区，接入后再发三次设备查询：
	// 只能收到三份回复，多出来的会被最后一次 read 读到
	script := `for i in $(seq 1 300); do printf '\033[3%dmline %d 中文\033[0m\n' $((i%7)) $i; done
sleep 1.5; stty raw -echo
for i in 1 2 3; do printf '\033[c'; IFS= read -t 3 -r -d c a; printf 'reply%d=%s\r\n' $i "${a#?}"; done
IFS= read -t 1 -r -d c extra; stty sane; printf 'extra=[%s]\n' "${extra#?}"; exit 5`
	id := createTerminal(t, c, "/bin/bash", "--norc", "-c", script)
	time.Sleep(500 * time.Millisecond)

	conn := c.Attach(id)
	ui, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer ui.Close()
	ui.Resize(100, 30)

	text := waitText(t, ui, "extra=")
	for _, want := range []string{"line 1 中文", "line 300 中文", "reply1=[?", "reply3=[?", "extra=[]"} {
		if !strings.Contains(text, want) {
			t.Errorf("UI terminal lacks %q", want)
		}
	}
	// 接入前的输出只能出现一次
	if n := strings.Count(text, "line 300 中文"); n != 1 {
		t.Errorf("line 300 appears %d times", n)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exited, _ := conn.Exited(); exited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if exited, code := conn.Exited(); !exited || code != 5 {
		t.Errorf("Exited() = %v, %d; want true, 5", exited, code)
	}
}

func TestAttachInputAndResize(t *testing.T) {
	c := startDaemon(t)
	id := createTerminal(t, c, "/bin/bash", "--norc", "-i")
	conn := c.Attach(id)
	ui, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer ui.Close()
	ui.Resize(90, 25)
	time.Sleep(300 * time.Millisecond)

	ui.Send([]byte("echo from-ui $((6*7)); stty size\r"))
	text := waitText(t, ui, "from-ui 42")
	waitText(t, ui, "25 90")

	// 常驻进程那份屏幕与界面一致
	var r struct{ Lines []string }
	if err := c.Call(context.Background(), "terminal.read", map[string]any{"terminal": id, "lines": 10}, &r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.Lines, "\n"), "from-ui 42") {
		t.Errorf("daemon screen lacks the output; UI:\n%s\ndaemon:\n%s", text, strings.Join(r.Lines, "\n"))
	}
}
