package media

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/history"
)

func antigravityMedia(ctx context.Context, transcript string) (pasted, made []Item, err error) {
	root := history.AntigravityBrainDir(transcript)
	if root == "" {
		return nil, nil, nil
	}
	metadata := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".user_uploaded" {
				return filepath.SkipDir
			}
			return nil
		}
		// 不跟随会话目录中的软链接，把扫描限制在实际产物内。
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		video := false
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mp4", ".mov", ".m4v", ".webm":
			video = true
		}
		if !video && !IsImageFile(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		metadata += 256 + len(path)*2
		if metadata > history.MediaScanLimit {
			return history.ErrMediaScanLimit
		}
		it := Item{Key: path, Path: path, Label: entry.Name(), At: info.ModTime(), Video: video}
		if strings.HasPrefix(rel, ".user_uploaded"+string(filepath.Separator)) {
			it.Tag = TagPasted
			pasted = append(pasted, it)
		} else {
			made = append(made, it)
		}
		return nil
	})
	return
}

func uniqueAntigravityMedia(pasted, made []Item) ([]Item, []Item) {
	seen := map[string]bool{}
	unique := func(items []Item) []Item {
		out := items[:0]
		for _, it := range items {
			key := it.Path
			if it.Tool != nil && it.Tool.OnDisk {
				key = it.Tool.Path
			}
			if key != "" && seen[key] {
				continue
			}
			if key != "" {
				seen[key] = true
			}
			out = append(out, it)
		}
		return out
	}
	return unique(pasted), unique(made)
}
