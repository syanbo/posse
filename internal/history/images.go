package history

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// ToolImage 是 Claude 会话里工具返回的一张图片：agent 截的屏、读进来的图片文件等。
// 图片内容以 base64 内嵌在会话文件里，这里只记位置，用到时按 Offset 读回那一行（ReadToolImage）。
type ToolImage struct {
	Offset int64     // 所在行在会话文件里的起点
	Index  int       // 这一行里的第几张
	At     time.Time // 那一行的时间
	Tool   string    // 返回它的工具，如 Read、mcp__iphone-use__phone_screenshot
	Path   string    // Read 读的文件；截图之类没有
	Media  string    // 如 image/png
}

// ImageScanner 增量读 Claude 会话文件，收集工具返回的图片。不是并发安全的。
type ImageScanner struct {
	path   string
	offset int64
	tools  map[string]toolRef // tool_use 的 ID → 工具名和读的文件
	images []ToolImage
}

type toolRef struct{ name, path string }

func NewImageScanner(path string) *ImageScanner {
	return &ImageScanner{path: path, tools: map[string]toolRef{}}
}

// Path 是扫描的会话文件。
func (s *ImageScanner) Path() string { return s.path }

var imageMarkers = [][]byte{[]byte(`"type":"image"`), []byte(`"type": "image"`), []byte(`"tool_use"`)}

// Scan 读文件新增的完整行，返回到目前为止的图片，按会话里的顺序；文件变短说明被重写，从头再读。
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
	if fi.Size() < s.offset {
		s.offset, s.tools, s.images = 0, map[string]toolRef{}, nil
	}
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return nil, err
	}
	base := s.offset
	n, _, err := scanLines(f, func(line []byte, end int64) {
		if containsAny(line, imageMarkers) {
			s.line(line, base+end-int64(len(line)))
		}
	})
	s.offset += n
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

// ReadToolImage 读回 Scan 找到的一张图片的内容。
func ReadToolImage(path string, img ToolImage) ([]byte, error) {
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
	var l claudeImageLine
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(l.Message.Content, &blocks); err != nil {
		return nil, err
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
	return nil, errors.New("会话文件里没有这张图片了")
}
