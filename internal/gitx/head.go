package gitx

import (
	"os"
	"path/filepath"
	"strings"
)

// Head 返回 dir 所在仓库当前检出的分支，HEAD 游离时返回提交哈希的前 8 位，不是 git 仓库时返回空串。
// 直接读 HEAD 文件而不调 git：界面每秒对每个工作区都要问一次。
func Head(dir string) string {
	head, _ := HeadRef(dir)
	return head
}

// HeadRef 和 Head 一样，另外说明 HEAD 是不是游离的（这时返回的是哈希，不是分支名）。
func HeadRef(dir string) (head string, detached bool) {
	for d := dir; ; {
		dot := filepath.Join(d, ".git")
		if fi, err := os.Stat(dot); err == nil {
			gitDir := dot
			if !fi.IsDir() {
				// worktree 和子模块的 .git 是一行 "gitdir: <路径>"
				b, err := os.ReadFile(dot)
				if err != nil {
					return "", false
				}
				p, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
				if !ok {
					return "", false
				}
				gitDir = strings.TrimSpace(p)
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(d, gitDir)
				}
			}
			b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return "", false
			}
			head := strings.TrimSpace(string(b))
			if ref, ok := strings.CutPrefix(head, "ref:"); ok {
				return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/"), false
			}
			return head[:min(8, len(head))], true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}
