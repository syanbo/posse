package history

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// CheckTrashSession 从源文件重新核对身份。增量索引只比较尾部和磁盘戳，不能替代删除前的核对。
func CheckTrashSession(ctx context.Context, s Session) error {
	paths := s.Parts
	if len(paths) == 0 {
		paths = []string{s.Path}
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := sessionIDFromPath(path)
		var st fileState
		switch s.Agent {
		case "claude", "codex":
			var p parser = claudeParser{}
			if s.Agent == "codex" {
				p = codexParser{}
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 64<<10), maxLine)
			for scanner.Scan() {
				if err := ctx.Err(); err != nil {
					f.Close()
					return err
				}
				p.line(scanner.Bytes(), &st)
				if st.SessionID != "" {
					break
				}
			}
			err = scanner.Err()
			f.Close()
			if err != nil {
				return err
			}
		case "grok":
			err := (grokParser{}).meta(filepath.Join(filepath.Dir(path), "summary.json"), &st)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if st.SessionID != "" {
			id = st.SessionID
		}
		if id != s.ID {
			return errors.New("session file no longer matches the selected session")
		}
	}
	return nil
}

// TrashPaths 只认目标用户的会话目录；home 由服务端决定，不接受客户端指定。
func TrashPaths(home string, s Session) []string {
	if home == "" || s.Path == "" || s.ID == "" {
		return nil
	}
	under := func(path string, root ...string) bool {
		base := filepath.Join(append([]string{home}, root...)...)
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		// 父目录的软链接不能把一个看似属于会话目录的路径引到别处。
		base, err = filepath.EvalSymlinks(base)
		if err != nil {
			return false
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return false
		}
		rel, err = filepath.Rel(base, parent)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	isDir := func(path string) bool {
		fi, err := os.Lstat(path)
		return err == nil && fi.IsDir()
	}
	switch s.Agent {
	case "claude":
		if !under(s.Path, ".claude", "projects") {
			return nil
		}
		out := []string{s.Path}
		if dir := strings.TrimSuffix(s.Path, ".jsonl"); filepath.Base(dir) == s.ID && isDir(dir) {
			out = append(out, dir)
		}
		return out
	case "codex":
		parts := s.Parts
		if len(parts) == 0 {
			parts = []string{s.Path}
		}
		for _, p := range parts {
			if !under(p, ".codex") {
				return nil
			}
		}
		return parts
	case "grok":
		if dir := filepath.Dir(s.Path); filepath.Base(dir) == s.ID && under(dir, ".grok", "sessions") {
			return []string{dir}
		}
	case "cursor":
		if dir := filepath.Dir(s.Path); filepath.Base(dir) == s.ID && under(dir, ".cursor", "projects") {
			return []string{dir}
		}
	}
	return nil
}
