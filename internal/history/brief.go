package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// briefLimit 是交给新会话的最近会话条数。
const briefLimit = 3

// briefLineRunes 是交接里最后一句保留的字数。
const briefLineRunes = 160

// Brief 是这个目录（含子目录）最近几条会话的交接：标题、最后一句用户或助手的话、查看命令。
// 只读已经建好的索引，不更新。没有会话时返回空字符串。skipID 是正在开的这一条，不写进去。
func (x *Index) Brief(ctx context.Context, cwd, skipID string) (string, error) {
	if strings.TrimRight(cwd, "/") == "" {
		return "", nil
	}
	ss, err := x.Sessions(ctx, ListOptions{Cwd: cwd, Limit: briefLimit + 4})
	if err != nil || len(ss) == 0 {
		return "", err
	}
	var lines []string
	for _, s := range ss {
		if s.ID == "" || s.ID == skipID {
			continue
		}
		last, err := x.lastSaid(ctx, s.Path)
		if err != nil {
			return "", err
		}
		lines = append(lines, briefLine(s, last))
		if len(lines) == briefLimit {
			break
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "Recent sessions in this directory. Read one with `posse history show <id>`.\n" + strings.Join(lines, "\n"), nil
}

func briefLine(s Session, last string) string {
	title := oneLine(s.Title, 80)
	if title == "" {
		title = s.ID
	}
	line := fmt.Sprintf("- %s (%s %s)", title, s.Agent, s.ID)
	if last != "" {
		line += ": " + last
	}
	return line
}

// lastSaid 是这条会话文件里最后一句用户或助手的话。思考和工具调用不算。
func (x *Index) lastSaid(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	var text string
	err := x.db.QueryRowContext(ctx, `
		SELECT m.text FROM messages m
		JOIN sessions s ON s.id = m.session
		WHERE s.path = ? AND m.role IN ('user', 'assistant')
		ORDER BY m.seq DESC LIMIT 1`, path).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return oneLine(text, briefLineRunes), nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
