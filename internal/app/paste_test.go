package app

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/transfer"
)

// 访达复制的文件（文本只是文件名）贴路径；文本是别的内容时照常贴文本。
func TestCopiedFiles(t *testing.T) {
	clip := func(text string, paths ...string) transfer.Data {
		var urls []string
		for _, p := range paths {
			urls = append(urls, (&url.URL{Scheme: "file", Path: p}).String())
		}
		reps := []transfer.Representation{transfer.Bytes(transfer.FileList, []byte(strings.Join(urls, "\n")))}
		if text != "" {
			reps = append(reps, transfer.Bytes(transfer.Text, []byte(text)))
		}
		return transfer.New(transfer.NewItem(reps...))
	}
	one, two := "/tmp/a b/rec.mp4", "/tmp/shot.png"
	for _, tc := range []struct {
		name string
		d    transfer.Data
		want []string
	}{
		{"only files", clip("", one), []string{one}},
		{"finder copy", clip("rec.mp4", one), []string{one}},
		{"finder copy, two files", clip("rec.mp4\rshot.png", one, two), []string{one, two}},
		{"real text with an attachment", clip("see the recording", one), nil},
		{"no files", transfer.TextData("hello"), nil},
	} {
		if got := copiedFiles(tc.d); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
