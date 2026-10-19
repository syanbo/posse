package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	tailChunk = 1 << 20
	tailLimit = 16 << 20 // 从末尾往前最多读这么远
)

// lastLine 从 path 末尾往前找含 marker 的行，交给 parse，第一个 parse 成功的就是结果。
func lastLine[T any](path string, marker []byte, parse func([]byte) (T, bool)) (T, bool) {
	var zero T
	f, err := os.Open(path)
	if err != nil {
		return zero, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return zero, false
	}
	return scanBack(f, fi.Size(), 0, marker, parse)
}

// scanBack 在 [from, end) 里从后往前逐行找。
func scanBack[T any](r io.ReaderAt, end, from int64, marker []byte, parse func([]byte) (T, bool)) (T, bool) {
	var zero T
	var tail []byte // 上一块开头不完整的那一行，拼到这一块后面
	for read := int64(0); end > from && read < tailLimit; {
		n := min(int64(tailChunk), end-from)
		buf := make([]byte, n, int(n)+len(tail))
		if _, err := r.ReadAt(buf, end-n); err != nil && err != io.EOF {
			return zero, false
		}
		end -= n
		read += n
		buf = append(buf, tail...)
		first := 0
		if end > from {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				tail = buf
				continue
			}
			first = i + 1
		}
		tail = buf[:first]
		lines := buf[first:]
		for len(lines) > 0 {
			i := bytes.LastIndexByte(bytes.TrimRight(lines, "\n"), '\n')
			line := lines[i+1:]
			lines = lines[:i+1]
			if bytes.Contains(line, marker) {
				if v, ok := parse(line); ok {
					return v, true
				}
			}
		}
	}
	return zero, false
}

// num 认 JSON 里的数字和写成字符串的数字（Connect 协议把 int64 写成字符串）。
type num struct {
	v  float64
	ok bool
}

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		n.v, n.ok = v, true
	}
	return nil
}

// epochMs 把秒或毫秒的时间戳统一成毫秒：1e10 以下是秒。
func epochMs(v float64) int64 {
	if v <= 0 {
		return 0
	}
	if v < 1e10 {
		return int64(v * 1000)
	}
	return int64(v)
}

func isoMs(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func clampPercent(v float64) float64 {
	return min(100, max(0, v))
}

func decode(line []byte, v any) bool {
	return json.Unmarshal(line, v) == nil
}
