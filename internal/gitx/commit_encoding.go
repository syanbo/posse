package gitx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

func decodeCommitObject(ctx context.Context, raw []byte) ([]byte, error) {
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, ErrLogOutput
	}
	headers, _, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		return nil, ErrLogOutput
	}
	charset := "UTF-8"
	declared := false
	for _, line := range bytes.Split(headers, []byte("\n")) {
		if value, ok := bytes.CutPrefix(line, []byte("encoding ")); ok {
			if declared || len(value) > 128 {
				return nil, ErrLogOutput
			}
			declared = true
			charset = string(value)
		}
	}
	if strings.EqualFold(charset, "UTF-8") || strings.EqualFold(charset, "UTF8") {
		if !utf8.Valid(raw) {
			return nil, ErrLogOutput
		}
		return raw, ctx.Err()
	}
	encoding, err := ianaindex.MIME.Encoding(charset)
	if err != nil || encoding == nil {
		return nil, fmt.Errorf("unsupported Git commit encoding %q", charset)
	}
	budget, _ := ctx.Value(readLimitKey{}).(*readBudget)
	if budget == nil {
		return nil, ErrReadLimit
	}
	// 转码可能放大正文；输出仍记入同一次读取的预算。
	decoded := &readBuffer{budget: budget, cancel: func() {}}
	if _, err := io.Copy(decoded, transform.NewReader(bytes.NewReader(raw), encoding.NewDecoder())); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 某些 decoder 用 U+FFFD 代替坏字节而不报错；回编码核对避免静默改写。
	restored, err := io.ReadAll(io.LimitReader(transform.NewReader(bytes.NewReader(decoded.Bytes()), encoding.NewEncoder()), int64(len(raw))+1))
	if err != nil || !bytes.Equal(restored, raw) {
		return nil, ErrLogOutput
	}
	return decoded.Bytes(), ctx.Err()
}
