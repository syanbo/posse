package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"posse/internal/history"
	"posse/internal/rpc"
	"posse/internal/trash"
)

type historyTrashParams struct {
	Session string `json:"session"`
	Path    string `json:"path"`
}

func (d *Daemon) historyTrash(ctx context.Context, raw json.RawMessage) (any, error) {
	var p historyTrashParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Session == "" || p.Path == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "session and path required")
	}
	// 先取得当前文件身份和 Codex 分段；索引等待不占用终端恢复锁。
	if err := d.refreshHistoryFiles(ctx); err != nil {
		return nil, err
	}
	moveErr := d.moveHistoryToTrash(ctx, p)
	// 多文件会话可能已移动一部分；失败也刷新，且绝不重做已经移动的路径。
	refreshErr := d.refreshHistoryFiles(ctx)
	return map[string]bool{"trashed": moveErr == nil}, errors.Join(moveErr, refreshErr)
}

func (d *Daemon) moveHistoryToTrash(ctx context.Context, p historyTrashParams) error {
	d.restoreMu.Lock()
	defer d.restoreMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := d.findHistoryPath(ctx, p.Path)
	if err != nil {
		return err
	}
	if s.ID != p.Session {
		return rpc.Errorf(rpc.CodeConflict, "session file no longer matches the selected session")
	}
	if d.sessionRunning(s) != nil {
		return rpc.Errorf(rpc.CodeConflict, "cannot move a running session to trash")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if d.cfg.HistoryRoots != nil {
		home = d.cfg.HistoryRoots.Home
	}
	paths := history.TrashPaths(home, s)
	if len(paths) == 0 {
		return rpc.Errorf(rpc.CodeConflict, "session file is outside the supported history directories")
	}
	if err := history.CheckTrashSession(ctx, s); err != nil {
		return rpc.Errorf(rpc.CodeConflict, "%v", err)
	}
	move := d.history.moveToTrash
	if move == nil {
		move = trash.Move
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := move(path); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) refreshHistoryFiles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h := &d.history
	h.mu.Lock()
	h.revision++
	h.lastRun = time.Time{}
	old := h.running
	h.mu.Unlock()
	// 旧轮可能已经扫描过源文件，必须等它结束再开新一轮。
	if old != nil {
		select {
		case <-old:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if d.refreshHistory(ctx, true, 30*time.Minute) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return rpc.Errorf(rpc.CodeBusy, "history refresh is still running")
	}
	h.mu.Lock()
	lastErr := h.lastErr
	h.mu.Unlock()
	if lastErr != "" {
		return rpc.Errorf(rpc.CodeInternal, "refresh history index: %s", lastErr)
	}
	return nil
}
