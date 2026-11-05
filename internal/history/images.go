package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ToolImage 是会话里的一张图片：agent 工具返回的（截的屏、读进来的图片文件等），或用户粘贴的。
// 内嵌在会话文件里的只记位置，用到时按 Offset 读回那一行（ReadToolImage）；
// OnDisk 的是 agent 自己存下的文件，内容就是 Path。
type ToolImage struct {
	Offset int64             // 所在行在会话文件里的起点
	Index  int               // 这一行里的第几张
	At     time.Time         // 那一行的时间；grok、cursor 的会话文件没有时间
	Tool   string            // 返回它的工具，如 Read、mcp__iphone-use__phone_screenshot
	Path   string            // 读的文件、粘贴的图片的文件；截图之类没有
	Media  string            // 如 image/png
	Pasted bool              // 用户粘贴的
	N      int               // 粘贴的图片的序号（[Image #N]），不知道时是 0
	OnDisk bool              // 内容在 Path，不在会话文件里
	Digest [sha256.Size]byte // Codex 工具图片的内容摘要，用来排除生成目录中的副本
}

// ImageScanner 增量读会话文件收集图片，不是并发安全的。各家记下的不同：
//   - claude：工具返回的（粘贴的另在会话的图片目录里）
//   - codex：用户粘贴和工具返回的内嵌图片；生成目录里的图片由 media 合并
//   - grok：工具返回的，和粘贴的（存在会话目录的 assets 里，用户消息的 <image_files> 列出路径）
//   - cursor：只有粘贴的（存在项目的 assets 里，同样列在 <image_files>）；工具输出不进会话文件
type ImageScanner struct {
	agent  string
	path   string
	offset int64
	tail   []byte             // offset 前的最后几个字节，用来发现文件被改写
	tools  map[string]toolRef // 工具调用的 ID → 工具名和读的文件
	last   *toolRef           // grok：上一行是工具结果时是它的工具
	images []ToolImage
}

type toolRef struct{ name, path string }

func NewImageScanner(agent, path string) *ImageScanner {
	return &ImageScanner{agent: agent, path: path, tools: map[string]toolRef{}}
}

// Path 是扫描的会话文件。
func (s *ImageScanner) Path() string { return s.path }

var imageMarkers = map[string][][]byte{
	"claude": {[]byte(`"type":"image"`), []byte(`"type": "image"`), []byte(`"tool_use"`)},
	"codex":  {[]byte(`"input_image"`), []byte(`"custom_tool_call"`), []byte(`"function_call"`), []byte(`"custom_tool_call_output"`), []byte(`"function_call_output"`)},
	"grok":   {[]byte(`"tool_calls"`), []byte(`"tool_result"`), []byte(`"type":"image"`), []byte(`<image_files>`)},
	"cursor": {[]byte(`<image_files>`)},
}

// Scan 读文件新增的完整行，返回到目前为止的图片，按会话里的顺序。
// 文件变短，或 offset 前的字节和上次不一样（grok 压缩上下文时整个重写会话文件，
// 不一定变短），说明被重写了，记下的偏移都作废，从头再读。
func (s *ImageScanner) Scan() ([]ToolImage, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < s.offset || !tailMatches(f, s.offset, s.tail) {
		s.offset, s.tail, s.tools, s.last, s.images = 0, nil, map[string]toolRef{}, nil, nil
	}
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return nil, err
	}
	base := s.offset
	markers := imageMarkers[s.agent]
	parse := map[string]func([]byte, int64){"claude": s.line, "codex": s.codexLine, "grok": s.grokLine, "cursor": s.cursorLine}[s.agent]
	if parse == nil {
		return nil, nil
	}
	n, tail, err := scanLines(f, func(line []byte, end int64) {
		if s.agent == "grok" {
			// grok 的 MCP 截图紧跟在工具结果后面，得知道上一行是什么
			if !containsAny(line, markers) {
				s.last = nil
				return
			}
		} else if !containsAny(line, markers) {
			return
		}
		parse(line, base+end-int64(len(line)))
	})
	s.offset += n
	if n > 0 {
		s.tail = tail
	}
	return s.images, err
}

type claudeImageLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	Input     struct {
		FilePath string `json:"file_path"`
	} `json:"input"`
	Content json.RawMessage `json:"content"`
	Source  struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

func (s *ImageScanner) line(b []byte, offset int64) {
	var l claudeImageLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	var blocks []claudeBlock
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return
	}
	switch l.Type {
	case "assistant":
		for _, bl := range blocks {
			if bl.Type == "tool_use" {
				s.tools[bl.ID] = toolRef{bl.Name, bl.Input.FilePath}
			}
		}
	case "user":
		// 只认工具结果里的：用户消息里直接带的图片是粘贴的，另有图片目录；
		// api-request-blob 之类的记录是请求的副本，类型不是 user
		index := 0
		for _, bl := range blocks {
			if bl.Type != "tool_result" {
				continue
			}
			var items []claudeBlock
			if json.Unmarshal(bl.Content, &items) != nil {
				continue
			}
			ref := s.tools[bl.ToolUseID]
			for _, it := range items {
				if it.Type != "image" || it.Source.Type != "base64" {
					continue
				}
				s.images = append(s.images, ToolImage{Offset: offset, Index: index, At: l.Timestamp, Tool: ref.name, Path: ref.path, Media: it.Source.MediaType})
				index++
			}
		}
	}
}

// Codex 只取 response_item 中的图片；item_completed 可能重复记录同一份工具结果。
type codexImageBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

type codexImageLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   struct {
		Type    string            `json:"type"`
		Role    string            `json:"role"`
		Name    string            `json:"name"`
		CallID  string            `json:"call_id"`
		Content []codexImageBlock `json:"content"`
		Output  json.RawMessage   `json:"output"`
	} `json:"payload"`
}

var codexImageTag = regexp.MustCompile(`^<image name=\[Image #(\d+)\] path="(.*)">$`)

func codexImages(b []byte) (urls []string, imgs []ToolImage, ok bool) {
	var l codexImageLine
	if json.Unmarshal(b, &l) != nil {
		return nil, nil, false
	}
	return l.images()
}

func (l codexImageLine) images() (urls []string, imgs []ToolImage, ok bool) {
	if l.Type != "response_item" {
		return nil, nil, false
	}
	blocks := l.Payload.Content
	pasted := l.Payload.Type == "message" && l.Payload.Role == "user"
	switch {
	case pasted:
	case l.Payload.Type == "function_call_output" || l.Payload.Type == "custom_tool_call_output":
		// 旧格式的纯文本输出不含图片，不从文本路径或 JS 推断产物。
		if json.Unmarshal(l.Payload.Output, &blocks) != nil {
			return nil, nil, true
		}
	default:
		return nil, nil, false
	}
	var n int
	var path string
	for _, c := range blocks {
		switch c.Type {
		case "input_text":
			if pasted {
				if m := codexImageTag.FindStringSubmatch(c.Text); m != nil {
					n, _ = strconv.Atoi(m[1])
					path = m[2]
				}
			}
		case "input_image":
			imgs = append(imgs, ToolImage{Index: len(imgs), At: l.Timestamp, Path: path, Media: dataMedia(c.ImageURL), Pasted: pasted, N: n})
			urls = append(urls, c.ImageURL)
			n, path = 0, ""
		}
	}
	return urls, imgs, true
}

func (s *ImageScanner) codexLine(b []byte, offset int64) {
	var l codexImageLine
	if json.Unmarshal(b, &l) != nil || l.Type != "response_item" {
		return
	}
	switch l.Payload.Type {
	case "custom_tool_call", "function_call":
		s.tools[l.Payload.CallID] = toolRef{name: l.Payload.Name}
		return
	}
	urls, imgs, _ := l.images()
	for i := range imgs {
		if imgs[i].Pasted {
			continue
		}
		imgs[i].Tool = s.tools[l.Payload.CallID].name
		if imgs[i].Tool == "" {
			imgs[i].Tool = "tool"
		}
		// 摘要只在增量扫描时计算，不保留解码后的原图，也不在每次刷新时重读历史。
		if _, data, ok := strings.Cut(urls[i], ";base64,"); ok {
			h := sha256.New()
			if _, err := io.Copy(h, base64.NewDecoder(base64.StdEncoding, strings.NewReader(data))); err == nil {
				copy(imgs[i].Digest[:], h.Sum(nil))
			}
		}
	}
	delete(s.tools, l.Payload.CallID)
	s.add(imgs, offset)
}

// grokImageLine 是 grok 会话里的一行：助手的工具调用、工具结果（read_file 读的图片在 images 里），
// 或用户消息（MCP 截图由 grok 在工具结果后面另起一条 synthetic 的用户消息带上）。没有时间。
type grokImageLine struct {
	Type       string          `json:"type"`
	Synthetic  string          `json:"synthetic_reason"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []grokToolCall  `json:"tool_calls"`
	Content    json.RawMessage `json:"content"`
	Images     []grokImage     `json:"images"`
}

type grokImage struct {
	Type string `json:"type"`
	Text string `json:"text"`
	URL  string `json:"url"`
}

// grokImages 列出一行里工具返回的图片的 data URL：工具结果的 images，或紧跟在工具结果后面的 synthetic 用户消息。
// 用户自己的消息里的图片是粘贴的，另有文件，不算。
func grokImages(l grokImageLine) []string {
	var urls []string
	switch {
	case l.Type == "tool_result":
		for _, im := range l.Images {
			urls = append(urls, im.URL)
		}
	case l.Type == "user" && l.Synthetic != "":
		var blocks []grokImage
		json.Unmarshal(l.Content, &blocks)
		for _, bl := range blocks {
			if bl.Type == "image" {
				urls = append(urls, bl.URL)
			}
		}
	}
	return urls
}

func (s *ImageScanner) grokLine(b []byte, offset int64) {
	var l grokImageLine
	if json.Unmarshal(b, &l) != nil {
		s.last = nil
		return
	}
	last := s.last
	s.last = nil
	for _, c := range l.ToolCalls {
		ref := toolRef{name: c.Name}
		var a struct {
			ToolName   string `json:"tool_name"`
			TargetFile string `json:"target_file"`
		}
		json.Unmarshal([]byte(c.Arguments), &a)
		if a.ToolName != "" {
			ref.name = a.ToolName // use_tool 调的 MCP 工具
		}
		ref.path = a.TargetFile
		s.tools[c.ID] = ref
	}
	switch {
	case l.Type == "tool_result":
		ref := s.tools[l.ToolCallID]
		s.last = &ref
		s.add(urlImages(grokImages(l), ref), offset)
	case l.Type == "user" && l.Synthetic != "":
		if last != nil {
			s.add(urlImages(grokImages(l), toolRef{name: last.name}), offset)
		}
	case l.Type == "user":
		var blocks []grokImage
		json.Unmarshal(l.Content, &blocks)
		for _, bl := range blocks {
			if bl.Type == "text" {
				s.add(imageFiles(bl.Text), offset)
			}
		}
	}
}

func (s *ImageScanner) cursorLine(b []byte, offset int64) {
	var l struct {
		Role    string `json:"role"`
		Message struct {
			Content []grokImage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &l) != nil || l.Role != "user" {
		return
	}
	for _, bl := range l.Message.Content {
		if bl.Type == "text" {
			s.add(imageFiles(bl.Text), offset)
		}
	}
}

// imageFilesRe 认 grok、cursor 在用户消息前面写的粘贴图片清单：
// <image_files>\nThe following images were provided by the user and saved to ...:\n1. /path\n...</image_files>
var (
	imageFilesRe = regexp.MustCompile(`(?s)<image_files>(.*?)</image_files>`)
	imageFileRe  = regexp.MustCompile(`(?m)^(\d+)\. (/.+)$`)
)

// imageFiles 列出一段用户消息里粘贴的图片文件，按清单里的序号；没写时间，用文件的修改时间。
func imageFiles(text string) []ToolImage {
	m := imageFilesRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	var out []ToolImage
	for _, f := range imageFileRe.FindAllStringSubmatch(m[1], -1) {
		n, _ := strconv.Atoi(f[1])
		path := strings.TrimSpace(f[2])
		im := ToolImage{Path: path, Pasted: true, N: n, OnDisk: true}
		if fi, err := os.Stat(path); err == nil {
			im.At = fi.ModTime()
		}
		out = append(out, im)
	}
	return out
}

func urlImages(urls []string, ref toolRef) []ToolImage {
	out := make([]ToolImage, len(urls))
	for i, u := range urls {
		out[i] = ToolImage{Index: i, Tool: ref.name, Path: ref.path, Media: dataMedia(u)}
	}
	return out
}

func (s *ImageScanner) add(imgs []ToolImage, offset int64) {
	for _, im := range imgs {
		if !im.OnDisk {
			im.Offset = offset
		}
		s.images = append(s.images, im)
	}
}

// dataMedia 取 data URL 的媒体类型，如 image/png。
func dataMedia(url string) string {
	media, _, _ := strings.Cut(strings.TrimPrefix(url, "data:"), ";")
	return media
}

func decodeDataURL(url string) ([]byte, error) {
	_, data, ok := strings.Cut(url, ";base64,")
	if !ok {
		return nil, errors.New("not an embedded image")
	}
	return base64.StdEncoding.DecodeString(data)
}

var errImageGone = errors.New("the session file no longer has this image")

// ReadToolImage 读回 Scan 找到的一张图片的内容。
func ReadToolImage(path string, img ToolImage) ([]byte, error) {
	if img.OnDisk {
		return os.ReadFile(img.Path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(img.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	b, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if urls, _, ok := codexImages(b); ok {
		if img.Index >= len(urls) {
			return nil, errImageGone
		}
		data, err := decodeDataURL(urls[img.Index])
		if err != nil {
			return nil, err
		}
		// 会话重写后同一偏移可能还是图片，需确认读到的仍是这份快照。
		if img.Digest != ([sha256.Size]byte{}) && sha256.Sum256(data) != img.Digest {
			return nil, errImageGone
		}
		return data, nil
	}
	var g grokImageLine
	if json.Unmarshal(b, &g) == nil && (g.Type == "tool_result" || g.Synthetic != "") {
		urls := grokImages(g)
		if img.Index >= len(urls) {
			return nil, errImageGone
		}
		return decodeDataURL(urls[img.Index])
	}
	// 解析不了说明偏移处已经不是当初那一行（会话文件被重写、还没重扫），不是坏数据
	var l claudeImageLine
	if json.Unmarshal(b, &l) != nil {
		return nil, errImageGone
	}
	var blocks []claudeBlock
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return nil, errImageGone
	}
	index := 0
	for _, bl := range blocks {
		if bl.Type != "tool_result" {
			continue
		}
		var items []claudeBlock
		if json.Unmarshal(bl.Content, &items) != nil {
			continue
		}
		for _, it := range items {
			if it.Type != "image" || it.Source.Type != "base64" {
				continue
			}
			if index == img.Index {
				return base64.StdEncoding.DecodeString(it.Source.Data)
			}
			index++
		}
	}
	return nil, errImageGone
}
