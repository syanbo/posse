package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"posse/internal/history"
)

// Codex 看图时只打印文件名（Viewed image 04-home.png），图片常不在终端的目录里，按相对路径找不到。
// 完整路径在会话文件里：看完图有一条 item_completed 的 ImageView 事件，带解析好的 file:// 路径（代码里拼出来的也在）；
// 没有这条事件的旧版只能读 exec 代码里 tools.view_image({path:"…"}) 的字面量。这里按终端记下这些路径，
// 相对路径找不到时拿它们的结尾去对。
type viewedImages struct {
	transcript string
	off        int64     // 会话文件读到的位置
	at         time.Time // 上次读完的时间
	busy       bool
	paths      []string // 看过的图片，后看的在后
}

const (
	viewedRefresh = 2 * time.Second
	viewedKeep    = 2000 // 每个终端最多记几张
)

// viewedImage 是这个终端的 Codex 会话看过的、路径以 link 结尾的图片里最后看的那张，没有返回空。
// 持着终端的锁调用：只查记下的路径，会话文件在后台读，读到新的清掉这个终端的链接缓存。
func (a *app) viewedImage(v *termView, link string) string {
	rel := filepath.Clean(linePos.ReplaceAllString(strings.TrimSpace(link), ""))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || !isImagePath(rel) {
		return ""
	}
	_, ti := a.findTerminal(v.id)
	if ti == nil || ti.Agent != "codex" || ti.AgentState == nil {
		return ""
	}
	transcript := history.TranscriptPath("codex", ti.AgentState.SessionID, ti.AgentState.TranscriptPath)
	if transcript == "" {
		return ""
	}
	vi := a.viewed[v.id]
	if vi == nil || vi.transcript != transcript {
		if a.viewed == nil || len(a.viewed) > 32 {
			a.viewed = map[string]*viewedImages{}
		}
		vi = &viewedImages{transcript: transcript}
		a.viewed[v.id] = vi
	}
	if !vi.busy && time.Since(vi.at) >= viewedRefresh {
		vi.busy = true
		id, off, base := v.id, vi.off, a.linkBase(v)
		go func() {
			paths, next := readViewedImages(transcript, off, base)
			a.update(func() {
				if a.viewed[id] != vi {
					return
				}
				vi.busy, vi.at, vi.off = false, time.Now(), next
				if len(paths) == 0 {
					return
				}
				vi.paths = append(vi.paths, paths...)
				if n := len(vi.paths) - viewedKeep; n > 0 {
					vi.paths = append([]string(nil), vi.paths[n:]...)
				}
				// 之前按相对路径没找到、记成打不开的，现在可能找得到了
				for k := range a.linkChecks {
					if strings.HasPrefix(k, id+"\x00") {
						delete(a.linkChecks, k)
					}
				}
			})
		}()
	}
	suffix := string(filepath.Separator) + rel
	for i := len(vi.paths) - 1; i >= 0; i-- {
		if p := vi.paths[i]; strings.HasSuffix(p, suffix) {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

// viewImageCall 是 exec 的代码里一句看图调用的路径字面量（双引号或单引号）。
var viewImageCall = regexp.MustCompile(`view_image\(\s*\{\s*path\s*:\s*("(?:[^"\\]|\\.)*"|'[^'\\]*')`)

// readViewedImages 从会话文件的 off 起读写完的行，返回里面看过的图片的绝对路径（相对的接在 base 后面）和读到的位置。
// 文件变短了（换过）从头读。
func readViewedImages(transcript string, off int64, base string) (paths []string, next int64) {
	f, err := os.Open(transcript)
	if err != nil {
		return nil, off
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() < off {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, off
	}
	next = off
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// 没写完的行留给下一次
			return paths, next
		}
		next += int64(len(line))
		event := bytes.Contains(line, []byte(`"ImageView"`))
		if !event && !bytes.Contains(line, []byte("view_image")) {
			continue
		}
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type  string `json:"type"`
				Input string `json:"input"`
				Item  struct {
					Type string `json:"type"`
					Path string `json:"path"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		if event && l.Type == "event_msg" && l.Payload.Type == "item_completed" && l.Payload.Item.Type == "ImageView" {
			if p := imageViewPath(l.Payload.Item.Path); p != "" {
				paths = append(paths, p)
			}
			continue
		}
		if l.Type != "response_item" || l.Payload.Type != "custom_tool_call" {
			continue
		}
		for _, m := range viewImageCall.FindAllStringSubmatch(l.Payload.Input, -1) {
			lit := m[1]
			p := lit[1 : len(lit)-1]
			if lit[0] == '"' {
				if s, err := strconv.Unquote(lit); err == nil {
					p = s
				}
			}
			if p == "" {
				continue
			}
			if !filepath.IsAbs(p) {
				if base == "" {
					continue
				}
				p = filepath.Join(base, p)
			}
			paths = append(paths, filepath.Clean(p))
		}
	}
}

// imageViewPath 把 ImageView 事件的路径（file:// 网址，也接受普通绝对路径）换成本地绝对路径，认不出返回空。
func imageViewPath(s string) string {
	if strings.HasPrefix(s, "file://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Path
	}
	if !filepath.IsAbs(s) {
		return ""
	}
	return filepath.Clean(s)
}
