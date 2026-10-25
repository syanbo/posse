package mobile

import (
	"bytes"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"posse/internal/media"
)

// 手机的"图片与视频"页（Posse 扩展，声明了 posse.media.v1 的连接才有），和桌面的图片面板列的一样：
//   - session.media 列出会话里 agent 生成的（最多 mediaMadeMax 项）和粘贴的，新的在前；
//   - session.mediaThumb 给一张缩略图（JPEG），视频是第一帧；
//   - session.mediaRead 按段读原图或视频，手机下到本地再看。
//
// 手机只能按 session.media 给过的 ID 读，读不到任意文件。
const (
	mediaMadeMax    = 40  // 同桌面图片面板的 maxMadeImages
	mediaThumbWidth = 480 // 手机两列的格子约 180pt，3 倍屏下够用
	mediaCacheMax   = 8   // 记住几个会话的扫描进度
)

type mediaParams struct {
	Agent          string `json:"agent"`
	SessionID      string `json:"sessionId"`
	TranscriptPath string `json:"transcriptPath"`
	ID             string `json:"id"`
	Offset         int64  `json:"offset"`
	Limit          int    `json:"limit"`
}

// mediaSession 是一个会话的扫描进度和扫到的全部条目（ID → 条目）。
type mediaSession struct {
	mu      sync.Mutex
	scanner *media.Scanner
	items   map[string]media.Item
	used    time.Time
}

// mediaCache 让同一个会话再列时只读会话文件新增的部分，几台手机、几条连接共用。
type mediaCache struct {
	mu       sync.Mutex
	sessions map[string]*mediaSession
}

func (mc *mediaCache) get(a mediaParams) *mediaSession {
	key := a.Agent + "\n" + a.SessionID + "\n" + a.TranscriptPath
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if mc.sessions == nil {
		mc.sessions = map[string]*mediaSession{}
	}
	ms := mc.sessions[key]
	if ms == nil {
		if len(mc.sessions) >= mediaCacheMax {
			var oldest string
			for k, v := range mc.sessions {
				if oldest == "" || v.used.Before(mc.sessions[oldest].used) {
					oldest = k
				}
			}
			delete(mc.sessions, oldest)
		}
		ms = &mediaSession{scanner: media.NewScanner(a.Agent, a.SessionID, a.TranscriptPath)}
		mc.sessions[key] = ms
	}
	ms.used = time.Now()
	return ms
}

func (ms *mediaSession) scan() (pasted, made []media.Item) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	pasted, made = ms.scanner.Scan()
	ms.items = map[string]media.Item{}
	for _, it := range slices.Concat(pasted, made) {
		ms.items[it.Key] = it
	}
	return pasted, made
}

// item 找手机给的 ID；没见过的先扫一遍（手机刚连上、常驻进程刚交接过）。
func (ms *mediaSession) item(id string) (media.Item, bool) {
	ms.mu.Lock()
	it, ok := ms.items[id]
	ms.mu.Unlock()
	if !ok {
		ms.scan()
		ms.mu.Lock()
		it, ok = ms.items[id]
		ms.mu.Unlock()
	}
	return it, ok
}

// mediaThumbs 一次只做一张缩略图：截图解码后一张十几 MB，常驻进程不该因为手机翻得快涨上去。
var mediaThumbs = make(chan struct{}, 1)

// mediaMethod 处理 session.media*；不认识的方法返回 false。扫会话文件、解码图片要一会儿，
// 放到自己的 goroutine 里，不挡住同一连接上的终端输入。
func (c *conn) mediaMethod(req request, p json.RawMessage) bool {
	var run func(a mediaParams) (any, error)
	switch req.Method {
	case "session.media":
		run = c.srv.mediaList
	case "session.mediaThumb":
		run = c.srv.mediaThumb
	case "session.mediaRead":
		run = c.srv.mediaRead
	default:
		return false
	}
	if !c.hasCapability("posse.media.v1") {
		c.fail(req.ID, "method_not_found", "Unknown method: "+req.Method)
		return true
	}
	var a mediaParams
	if err := json.Unmarshal(p, &a); err != nil {
		c.fail(req.ID, "invalid_params", err.Error())
		return true
	}
	if !media.Supported(a.Agent) {
		c.fail(req.ID, "unsupported", "目前只支持 Claude、Codex、Grok 和 Cursor 会话")
		return true
	}
	go func() {
		res, err := run(a)
		c.replyBulk(req.ID, res, err)
	}()
	return true
}

func (s *Server) mediaList(a mediaParams) (any, error) {
	pasted, made := s.media.get(a).scan()
	rows := func(items []media.Item) []map[string]any {
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			kind := "image"
			if it.Video {
				kind = "video"
			}
			row := map[string]any{"id": it.Key, "kind": kind, "label": it.Label, "source": it.Source(), "at": ms(it.At)}
			if it.Path != "" {
				if fi, err := os.Stat(it.Path); err == nil {
					row["size"] = fi.Size()
				}
			}
			out = append(out, row)
		}
		return out
	}
	return map[string]any{
		"made":    rows(made[:min(len(made), mediaMadeMax)]),
		"madeAll": len(made),
		"pasted":  rows(pasted),
	}, nil
}

func (s *Server) mediaThumb(a mediaParams) (any, error) {
	it, ok := s.media.get(a).item(a.ID)
	if !ok {
		return nil, errorf("not_found", "media %s not found", a.ID)
	}
	mediaThumbs <- struct{}{}
	defer func() { <-mediaThumbs }()
	img, err := media.Decode(it, mediaThumbWidth)
	if err != nil {
		return nil, errorf("thumb_failed", "%v", err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	b := img.Bounds()
	return map[string]any{"content": buf.Bytes(), "width": b.Dx(), "height": b.Dy()}, nil
}

// mediaRead 按段读原图或视频，同 files.readRange：每段最多 rangeMax，size 是总大小。
// 工具返回的图片在会话文件里，每段都重新取一次那一行：图片最大也就几 MB。
func (s *Server) mediaRead(a mediaParams) (any, error) {
	it, ok := s.media.get(a).item(a.ID)
	if !ok {
		return nil, errorf("not_found", "media %s not found", a.ID)
	}
	n := a.Limit
	if n <= 0 || n > rangeMax {
		n = rangeMax
	}
	if a.Offset < 0 {
		return nil, errorf("bad_params", "offset")
	}
	var r io.ReaderAt
	var size int64
	if it.Video {
		f, err := os.Open(it.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return nil, err
		}
		r, size = f, fi.Size()
	} else {
		b, err := media.ReadImage(it)
		if err != nil {
			return nil, err
		}
		r, size = bytes.NewReader(b), int64(len(b))
	}
	b := make([]byte, n)
	got, err := r.ReadAt(b, a.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return map[string]any{"content": b[:got], "size": size, "mime": it.Mime()}, nil
}
