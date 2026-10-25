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

	"posse/internal/agent"
	"posse/internal/rpc"
	plan "posse/internal/usage"
)

func cmdUsage(e *env, args []string) error {
	var st struct {
		Usages []plan.Usage `json:"usages"`
	}
	return e.do("usage.get", nil, &st, func() {
		if len(st.Usages) == 0 {
			fmt.Fprintln(e.out, "no usage yet: it updates as Claude, Codex, Grok or Cursor run in Posse")
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
				name := windowName(w.Minutes)
				if w.Name != "" {
					name = w.Name + " " + name
				}
				line := fmt.Sprintf("  %-10s %3.0f%% used", name, w.Percent)
				if w.ResetsAt > 0 {
					line += ", resets " + time.UnixMilli(w.ResetsAt).Local().Format("Jan 2 15:04")
				}
				fmt.Fprintln(e.out, line)
			}
			if u.Spend != nil {
				fmt.Fprintf(e.out, "  plan spend   $%.2f of $%.2f\n", u.Spend.Used/100, u.Spend.Limit/100)
			}
			if u.OnDemand != nil {
				fmt.Fprintf(e.out, "  on-demand    $%.2f\n", u.OnDemand.Used/100)
			}
			if u.Resets != nil {
				fmt.Fprintf(e.out, "  reset credits %d\n", u.Resets.Count)
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

// cmdStatusline 是 posse 注入给 Claude 的 statusLine：把用量转给常驻进程，自己不输出。
// --settings 会盖掉项目和用户配的 statusLine，这里按 Claude 的顺序（项目优先）替它跑一遍，输出原样交回。
func cmdStatusline(e *env, args []string) error {
	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	reportUsage(e, payload)
	cmd, dir := projectStatusLine(payload)
	if cmd == "" {
		cmd = userStatusLine()
	}
	if cmd != "" {
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
	socket := os.Getenv("POSSE_SOCKET")
	if socket == "" {
		socket = e.socket
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rpc.Call(ctx, socket, "usage.report", map[string]any{"agent": "claude", "payload": json.RawMessage(payload)}, nil)
}

// userStatusLine 是用户级设置里配的 statusLine 命令，在 Claude 的工作目录里跑。
func userStatusLine() string {
	sl, _ := agent.UserStatusLine()
	cmd, _ := sl["command"].(string)
	if isPosseStatusLine(cmd) {
		return ""
	}
	return cmd
}

// isPosseStatusLine 认出 posse 自己的 statusline，替用户跑命令时不再调回来。
func isPosseStatusLine(cmd string) bool {
	return strings.HasSuffix(strings.TrimSpace(cmd), " statusline")
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
			!isPosseStatusLine(s.StatusLine.Command) {
			return s.StatusLine.Command, dir
		}
	}
	return "", ""
}
