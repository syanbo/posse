package term

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func scanAll(chunks ...string) []string {
	var s progScanner
	var got []string
	for _, c := range chunks {
		buf := []byte(c)
		s.feed(buf, func(b []byte) { got = append(got, string(b)) })
		// 读协程会复用缓冲区：扫完就改掉，留着引用的话结果会跟着变
		for i := range buf {
			buf[i] = 'X'
		}
	}
	return got
}

func TestProgScannerSplits(t *testing.T) {
	seq := "ab\x1b]7501;state=working:app=codex\x1b\\cd\x1b]7501;?\x07ef"
	want := []string{"state=working:app=codex", "?"}
	if got := scanAll(seq); !reflect.DeepEqual(got, want) {
		t.Fatalf("whole: %q", got)
	}
	// 每个位置切开都一样
	for i := 1; i < len(seq); i++ {
		if got := scanAll(seq[:i], seq[i:]); !reflect.DeepEqual(got, want) {
			t.Fatalf("split at %d: %q", i, got)
		}
	}
	if got := scanAll(strings.Split(seq, "")...); !reflect.DeepEqual(got, want) {
		t.Fatalf("byte by byte: %q", got)
	}
}

func TestProgScannerDrops(t *testing.T) {
	// 中途换成别的序列的作废，后面的照常
	if got := scanAll("\x1b]7501;state=idle\x1b[0m\x1b]7501;state=done\x07"); !reflect.DeepEqual(got, []string{"state=done"}) {
		t.Fatalf("interrupted: %q", got)
	}
	// 超长的不交出来，也不一直攒着
	long := "\x1b]7501;state=idle:msg=" + strings.Repeat("A", progMaxLen)
	var s progScanner
	s.feed([]byte(long), func([]byte) { t.Fatal("emitted overlong") })
	if len(s.pend) != 0 {
		t.Fatalf("kept %d bytes", len(s.pend))
	}
	if got := scanAll(long+"\x07", "\x1b]7501;state=idle\x07"); !reflect.DeepEqual(got, []string{"state=idle"}) {
		t.Fatalf("after overlong: %q", got)
	}
}

func TestParseProgramStatus(t *testing.T) {
	// 实测 Claude Code 2.1.318 提问时报的
	st, probe, ok := parseProgramStatus([]byte("state=blocked:app=claude-code:kind=question:msg=5L2g5pu05Zac5qyi57qi6Imy6L+Y5piv6JOd6Imy77yf"))
	if !ok || probe || st != (ProgramStatus{State: "blocked", Kind: "question", App: "claude-code", Msg: "你更喜欢红色还是蓝色？", Progress: -1}) {
		t.Fatalf("blocked: %+v %v %v", st, probe, ok)
	}
	if _, probe, _ := parseProgramStatus([]byte("?")); !probe {
		t.Fatal("probe")
	}
	if st, _, ok := parseProgramStatus([]byte("state=working:progress=40:kind=question:x:bogus=1")); !ok || st.Progress != 40 || st.Kind != "" {
		t.Fatalf("working: %+v", st)
	}
	for _, body := range []string{
		"state=sleeping",                                   // 不认识的状态
		"app=codex",                                        // 没有状态
		"state=working:id=us-east",                         // 子记录
		"state=clear:id=us-east",                           // 只清子记录
		"state=blocked:msg=%%%",                            // base64 解不开
		"state=blocked:msg=G1sybUhp",                       // ESC[2mHi：带控制字符
		"state=blocked:msg=" + strings.Repeat("QUFB", 700), // 解出来超过 2048 字节
	} {
		if _, _, ok := parseProgramStatus([]byte(body)); ok {
			t.Errorf("accepted %q", body)
		}
	}
	// 方向控制字符去掉
	if st, _, ok := parseProgramStatus([]byte("state=blocked:msg=YeKArmI=")); !ok || st.Msg != "ab" {
		t.Fatalf("bidi: %+v %v", st, ok)
	}
}

// 探测的应答先于内核对紧跟着的 CSI c 的应答（Claude 靠先后判断支不支持），状态交给 OnProgramStatus。
func TestProgramStatusProbeAndReport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "reply")
	script := `stty raw -echo; printf '\033]7501;?\033\\\033[c'; dd bs=1 count=10 of=` + out + ` 2>/dev/null;` +
		`printf '\033]7501;state=done:app=codex:msg=b2s=\007'; sleep 5`
	got := make(chan ProgramStatus, 4)
	tm, err := Start(Options{ID: "t_p", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script},
		OnProgramStatus: func(_ *Terminal, st ProgramStatus) { got <- st }})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(0)
	select {
	case st := <-got:
		if st.State != "done" || st.App != "codex" || st.Msg != "ok" {
			t.Fatalf("report %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no report")
	}
	b, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(b, progReply) {
		t.Fatalf("reply %q %v", b, err)
	}
}
