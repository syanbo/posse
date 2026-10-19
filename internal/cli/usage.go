package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hive/internal/rpc"
	plan "hive/internal/usage"
)

func cmdUsage(e *env, args []string) error {
	var st struct {
		Usages []plan.Usage `json:"usages"`
	}
	return e.do("usage.get", nil, &st, func() {
		if len(st.Usages) == 0 {
			fmt.Fprintln(e.out, "no usage yet: it updates as Claude, Codex, Grok or Cursor run in Hive")
			return
		}
		now := time.Now()
		for _, u := range st.Usages {
			head := u.Agent
			if u.Plan != "" {
				head += " (" + u.Plan + ")"
			}
			fmt.Fprintf(e.out, "%s, as of %s ago\n", head, now.Sub(time.UnixMilli(u.UpdatedAt)).Round(time.Minute))
			for _, w := range u.Windows {
				line := fmt.Sprintf("  %-8s %3.0f%% used", windowName(w.Minutes), w.Percent)
				if w.ResetsAt > 0 {
					line += ", resets " + time.UnixMilli(w.ResetsAt).Local().Format("Jan 2 15:04")
				}
				fmt.Fprintln(e.out, line)
			}
			if u.Spend != nil {
				fmt.Fprintf(e.out, "  spend    $%.2f of $%.2f\n", u.Spend.Used/100, u.Spend.Limit/100)
			}
		}
	})
}

func windowName(minutes int) string {
	switch {
	case minutes == 0:
		return "period"
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dd", (minutes+12*60)/(24*60))
}

// cmdUsageReport 把 Claude statusLine 的输入转给常驻进程，不输出。
// 用户自己配了 statusLine 时，可以在自己的脚本里把输入原样交给它。
func cmdUsageReport(e *env, args []string) error {
	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	reportUsage(e, payload)
	return nil
}

// cmdStatusline 是 hive 注入给 Claude 的 statusLine（只在用户没配 statusLine 时）：把用量转给常驻进程，
// 自己不输出。项目里配了 statusLine 时 --settings 会盖掉它，这里替它跑一遍。
func cmdStatusline(e *env, args []string) error {
	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	reportUsage(e, payload)
	if cmd, dir := projectStatusLine(payload); cmd != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
		c.Dir, c.Stdin, c.Stdout, c.Stderr = dir, bytes.NewReader(payload), e.out, e.errOut
		c.Run()
	}
	return nil
}

// reportUsage 只在有用量时发，从不拉起常驻进程：它由 Claude 同步调用，不能拖慢状态行。
func reportUsage(e *env, payload []byte) {
	if !bytes.Contains(payload, []byte(`"rate_limits"`)) || !json.Valid(payload) {
		return
	}
	socket := os.Getenv("HIVE_SOCKET")
	if socket == "" {
		socket = e.socket
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rpc.Call(ctx, socket, "usage.report", map[string]any{"agent": "claude", "payload": json.RawMessage(payload)}, nil)
}

// projectStatusLine 找项目里配的 statusLine：本地设置优先，和 Claude 的顺序一样。
func projectStatusLine(payload []byte) (cmd, dir string) {
	var in struct {
		Cwd       string `json:"cwd"`
		Workspace struct {
			ProjectDir string `json:"project_dir"`
		} `json:"workspace"`
	}
	json.Unmarshal(payload, &in)
	dir = in.Workspace.ProjectDir
	if dir == "" {
		dir = in.Cwd
	}
	if dir == "" {
		return "", ""
	}
	for _, name := range []string{"settings.local.json", "settings.json"} {
		b, err := os.ReadFile(filepath.Join(dir, ".claude", name))
		if err != nil {
			continue
		}
		var s struct {
			StatusLine *struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"statusLine"`
		}
		if json.Unmarshal(b, &s) == nil && s.StatusLine != nil && s.StatusLine.Command != "" &&
			!strings.HasSuffix(strings.TrimSpace(s.StatusLine.Command), " statusline") {
			return s.StatusLine.Command, dir
		}
	}
	return "", ""
}
