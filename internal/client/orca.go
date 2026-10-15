package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"hive/internal/rpc"
)

// ImportOrca 把 Orca 登记的项目和文件夹工作区导入 hive；已存在的跳过，可重复执行。
// 返回逐项结果；dry 为 true 时只列出将要导入的内容。
func (c *Client) ImportOrca(ctx context.Context, dry bool) ([]string, error) {
	var repos struct{ Repos []orcaRepo }
	if err := orcaJSON(&repos, "repo", "list"); err != nil {
		return nil, err
	}
	var wts struct{ Worktrees []orcaWorktree }
	if err := orcaJSON(&wts, "worktree", "list", "--limit", "1000"); err != nil {
		return nil, err
	}
	mainName := map[string]string{}
	var extra []orcaWorktree
	for _, w := range wts.Worktrees {
		if w.IsMainWorktree {
			mainName[w.RepoID] = safeName(w.DisplayName)
		} else {
			extra = append(extra, w)
		}
	}
	repoByID := map[string]orcaRepo{}
	var report []string
	for _, r := range repos.Repos {
		repoByID[r.ID] = r
		name := safeName(r.DisplayName)
		line := fmt.Sprintf("project %s (%s) main=%s", name, r.Path, mainName[r.ID])
		if !dry {
			err := c.Call(ctx, "project.add", map[string]any{"path": r.Path, "name": name, "setupScript": r.HookSettings.Scripts.Setup, "mainName": mainName[r.ID]}, nil)
			line += ": " + outcome(err)
		}
		report = append(report, line)
	}
	for _, w := range extra {
		r, ok := repoByID[w.RepoID]
		if !ok {
			continue
		}
		line := fmt.Sprintf("workspace %s/%s", safeName(r.DisplayName), safeName(w.DisplayName))
		if w.Path != r.Path {
			// Orca 管理的 git worktree 目录不在 hive 的 worktrees 下，不接管，需要时在 hive 里重建
			report = append(report, line+": skipped (git worktree at "+w.Path+")")
			continue
		}
		if !dry {
			err := c.Call(ctx, "workspace.create", map[string]any{"project": r.Path, "name": safeName(w.DisplayName), "mode": "folder"}, nil)
			line += ": " + outcome(err)
		}
		report = append(report, line)
	}
	return report, nil
}

func outcome(err error) string {
	var re *rpc.Error
	switch {
	case err == nil:
		return "added"
	case errors.As(err, &re) && re.Code == rpc.CodeConflict:
		return "exists"
	default:
		return "error: " + err.Error()
	}
}

const orcaBundledCLI = "/Applications/Orca.app/Contents/Resources/bin/orca"

type orcaRepo struct {
	ID, Path, DisplayName, Kind string
	HookSettings                struct {
		Scripts struct{ Setup string }
	}
}

type orcaWorktree struct {
	RepoID, Path, DisplayName string
	IsMainWorktree            bool
}

func orcaJSON(v any, args ...string) error {
	bin, err := exec.LookPath("orca")
	if err != nil {
		if _, statErr := os.Stat(orcaBundledCLI); statErr != nil {
			return errors.New("orca CLI not found")
		}
		bin = orcaBundledCLI
	}
	out, err := exec.Command(bin, append(args, "--json")...).Output()
	if err != nil {
		return fmt.Errorf("orca %s: %w", strings.Join(args, " "), err)
	}
	var resp struct {
		OK     bool
		Result json.RawMessage
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("orca %s failed: %s", strings.Join(args, " "), out)
	}
	return json.Unmarshal(resp.Result, v)
}

// safeName 把 Orca 的显示名转成 hive 允许的名称。
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|~^`, r) {
			return '-'
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.TrimLeft(s, "-.")
	if s == "" {
		return "workspace"
	}
	return s
}
