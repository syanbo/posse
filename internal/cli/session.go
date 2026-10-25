package cli

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"strings"
)

//go:embed session_guide.md
var sessionGuide string

// sessionEnv 是各 agent 给工具命令注入的当前会话 ID。
var sessionEnv = map[string]string{"claude": "CLAUDE_CODE_SESSION_ID", "codex": "CODEX_THREAD_ID", "grok": "GROK_SESSION_ID"}

func cmdSessionGuide(e *env, args []string) error {
	fs := flag.NewFlagSet("session guide", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	fmt.Fprint(e.out, sessionGuide)
	return nil
}

func cmdSessionTitle(e *env, args []string) error {
	fs := flag.NewFlagSet("session title", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: posse session title '<MMDD｜类型｜摘要>' (rules: posse session guide)")
	}
	id := caller()
	if id == "" {
		return fmt.Errorf("not in a Posse terminal: run it inside the session to name")
	}
	params := map[string]any{"terminal": id, "title": strings.Join(pos, " ")}
	// 嵌套时外层 agent 的变量也会继承下来，由常驻进程按终端里的 agent 挑对应的那个
	ids := map[string]string{}
	for name, v := range sessionEnv {
		if s := os.Getenv(v); s != "" {
			ids[name] = s
		}
	}
	params["sessionIds"] = ids
	if wd, err := os.Getwd(); err == nil {
		params["cwd"] = wd
	}
	var r struct{ Title, Agent, Applies string }
	return e.do("session.title", params, &r, func() {
		fmt.Fprintln(e.out, r.Title)
		fmt.Fprintln(e.out, r.Applies)
	})
}
