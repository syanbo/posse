package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 真的 git 经 trace2 报给常驻进程：只记最外层、会改动仓库的命令，带上终端、仓库和退出码；重启后读回来
func TestGitTrace(t *testing.T) {
	d, w := newTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.openGitTrace(ctx)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "r")
	env := append(os.Environ(), gitTraceEnv(d.cfg.Home)...)
	env = append(env, "HIVE_TERMINAL_ID="+s.ID, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		cmd.Run()
	}
	git(filepath.Dir(repo), "init", "-q", repo)
	git(repo, "status")
	git(repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	git(repo, "pull") // 没有远端，失败；里面的 fetch 子进程不另记
	var recs []gitTraceRecord
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if recs, _ = d.gitTrace.since(0); len(recs) >= 3 {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	recs, seq := d.gitTrace.since(0)
	var got []string
	for _, r := range recs {
		got = append(got, strings.Join(r.Args, " "))
	}
	if len(recs) != 3 || got[0] != "init -q "+repo || !strings.HasSuffix(got[1], "commit -q --allow-empty -m x") || got[2] != "pull" {
		t.Fatalf("records %q", got)
	}
	c := recs[1]
	if c.Terminal != s.ID || c.Agent != "claude" || !strings.HasSuffix(c.Dir, "/r") || c.Code != 0 || c.Start.IsZero() {
		t.Fatalf("commit record %+v", c)
	}
	if recs[2].Code == 0 {
		t.Fatalf("pull should fail: %+v", recs[2])
	}
	if after, _ := d.gitTrace.since(recs[1].Seq); len(after) != 1 {
		t.Fatalf("since: %+v", after)
	}

	// 重启：读回记录，序号接着往下数
	g := &gitTrace{path: d.gitTrace.path}
	g.load()
	if back, s2 := g.since(0); len(back) != 3 || s2 != seq {
		t.Fatalf("reloaded %d records, seq %d (want %d)", len(back), s2, seq)
	}
	g.add(gitTraceRecord{Args: []string{"push"}})
	if _, s3 := g.since(0); s3 != seq+1 {
		t.Fatalf("seq after reload %d", s3)
	}
}
