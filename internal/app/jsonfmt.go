package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"posse/locales"
)

func isJSON(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

// formatJSON 把 JSON 文件整理成缩进格式（键的顺序不变），作为一次编辑：能撤销，保存照常按 ⌘S。
// 语法错误时在编辑器顶上提示是第几行，不改内容。
func (a *app) formatJSON(m locales.Messages, d *fileDoc) {
	if d.kind != docText || d.readOnly || !isJSON(d.path) {
		return
	}
	src := d.ed.Buffer.String()
	out, err := formatJSONText(m, src)
	switch {
	case err != nil:
		d.hint = err.Error()
	case out == src:
		d.hint = m.FilesJSONAlreadyFormatted()
	default:
		line := d.ed.CursorPos().Line
		d.ed.Select(0, len(src))
		d.ed.Insert(out)
		d.ed.GoTo(editor.Pos{Line: min(line, strings.Count(out, "\n"))})
		d.hint = a.withKey(m.FilesJSONFormatted(), "editor.undo")
	}
	d.hintAt = time.Now()
}

// formatJSONText 按文件原来的缩进（制表符、几个空格，看不出来时两个空格）重排，结尾的换行照原样留着。
func formatJSONText(m locales.Messages, src string) (string, error) {
	var b bytes.Buffer
	// Indent 把结尾的空白原样抄过去，先去掉，最后按原样补一个换行
	body := strings.TrimRight(src, " \t\r\n")
	if err := json.Indent(&b, []byte(body), "", jsonIndent(src)); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			off := min(int(se.Offset), len(body))
			line := strings.Count(body[:off], "\n") + 1
			return "", errors.New(m.FilesJSONSyntaxError(line))
		}
		return "", errors.New(m.FilesJSONFormatFailed(err.Error()))
	}
	out := b.String()
	if strings.HasSuffix(src, "\n") {
		out += "\n"
	}
	return out, nil
}

// jsonIndent 取第一行有缩进的行开头的空白当一级缩进。
func jsonIndent(src string) string {
	for _, l := range strings.Split(src, "\n")[1:] {
		switch {
		case strings.HasPrefix(l, "\t"):
			return "\t"
		case strings.HasPrefix(l, " "):
			n := len(l) - len(strings.TrimLeft(l, " "))
			if n == 2 || n == 4 {
				return strings.Repeat(" ", n)
			}
			return "  "
		}
	}
	return "  "
}
