package term

import (
	"sync"
	"testing"
	"time"
)

// 程序清空标题后，Title 和标题回调都要跟着变成空的。
func TestEmptyTitleClears(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	tm, err := Start(Options{ID: "t_t", Dir: t.TempDir(),
		OnTitle: func(_ *Terminal, s string) { mu.Lock(); seen = append(seen, s); mu.Unlock() },
		Command: []string{"/bin/sh", "-c", "printf '\\033]2;Action Required\\007'; sleep 0.3; printf '\\033]2;\\007'; sleep 0.3; echo done; sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	waitFor(t, "done", func() bool { return tm.ContainsText("done") })
	mu.Lock()
	defer mu.Unlock()
	if tm.Title() != "" || len(seen) == 0 || seen[len(seen)-1] != "" {
		t.Fatalf("title %q, callbacks %q", tm.Title(), seen)
	}
}
