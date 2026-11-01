package daemon

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"posse/internal/gitx"
	"posse/internal/store"
)

// 项目根目录下的 .posse/ 放 agent 要留给人看的东西（任务报告、计划、交接），各工作区共用：
// worktree 删掉时报告还在。终端经 POSSE_NOTES 拿到这个目录；目录由 agent 用到时自己建。
// git 项目把它写进共用 git 目录的 info/exclude（所有 worktree 都生效，不改仓库里的 .gitignore）。

const notesDirName = ".posse"

// excludeLine 是写进 info/exclude 的一行
const excludeLine = "/" + notesDirName + "/"

var notesExcluded sync.Map // 项目 ID → 本进程已经确认过 exclude

func notesDir(p store.Project) string { return filepath.Join(p.Path, notesDirName) }

// notesEnv 给终端的 POSSE_NOTES；git 项目在后台确认一次 exclude。
func (d *Daemon) notesEnv(w store.Workspace) []string {
	p, err := d.store.FindProject(w.ProjectID)
	if err != nil {
		return nil
	}
	if p.Kind == "git" {
		if _, done := notesExcluded.LoadOrStore(p.ID, true); !done {
			go func() {
				if err := excludeNotes(p.Path); err != nil {
					log.Printf("notes exclude %s: %v", p.Path, err)
					notesExcluded.Delete(p.ID)
				}
			}()
		}
	}
	return []string{"POSSE_NOTES=" + notesDir(p)}
}

// excludeNotes 往仓库的 info/exclude 里补一行 /.posse/，已经有了不动。
func excludeNotes(repo string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	common := gitx.CommonDir(ctx, repo)
	if common == "" {
		return nil
	}
	path := filepath.Join(common, "info", "exclude")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range bytes.Split(b, []byte("\n")) {
		if string(bytes.TrimSpace(l)) == excludeLine {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	add := excludeLine + "\n"
	if len(b) > 0 && b[len(b)-1] != '\n' {
		add = "\n" + add
	}
	_, err = f.WriteString(add)
	return err
}
