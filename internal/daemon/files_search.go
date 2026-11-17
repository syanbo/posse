package daemon

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

const (
	fileSearchOverlayLimit = 128
	fileSearchOverlayBytes = 8 << 20
	fileSearchReplyBytes   = 4 << 20
)

func targetFileList(ctx context.Context, p rpc.FileListParams) (*files.List, error) {
	if err := filePath(p.Root); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.Root = filepath.Clean(p.Root)
	// Discover 会跳过读不了的目录，先核实根目录可读，免得把失败回成空清单。
	f, err := os.OpenFile(p.Root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fileRPCError(err)
	}
	_, err = f.ReadDir(1)
	f.Close()
	if err != nil && err != io.EOF {
		return nil, fileRPCError(err)
	}
	if p.All {
		l, err := files.ListAll(ctx, p.Root)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fileRPCError(err)
		}
		return l, nil
	}
	discovery := gitx.Discover(ctx, p.Root, !p.SingleRepo)
	var repos []string
	for _, repo := range discovery.Repos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		repos = append(repos, repo.Root)
		repos = append(repos, repo.Aliases...)
	}
	l, err := files.ListRepos(ctx, p.Root, repos)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	l.Truncated = l.Truncated || discovery.Partial
	return l, nil
}

func (d *Daemon) filesList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.FileListParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	l, err := targetFileList(ctx, p)
	if err != nil {
		return nil, err
	}
	return fileSearchReply(ctx, l)
}

func fileSearchRelative(path string) (string, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(path) || strings.ContainsRune(path, 0) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", rpc.Errorf(rpc.CodeBadRequest, "file search paths must be relative and stay within the root")
	}
	return clean, nil
}

func (d *Daemon) filesSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.FileSearchParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Root); err != nil {
		return nil, err
	}
	dir, err := fileSearchRelative(p.Query.Dir)
	if err != nil {
		return nil, err
	}
	if dir == "." {
		dir = ""
	}
	p.Query.Dir = dir
	overlay, err := fileSearchOverlay(ctx, p.Overlay)
	if err != nil {
		return nil, err
	}
	if _, err := p.Query.Compile(); err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid file search pattern: %v", err)
	}
	l, err := targetFileList(ctx, rpc.FileListParams{Root: p.Root, SingleRepo: p.SingleRepo})
	if err != nil {
		return nil, err
	}
	l, err = files.ScopedList(ctx, l, p.Query.Dir)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	var out rpc.FileSearchResult
	truncated, err := files.Search(ctx, l, p.Query, overlay, func(hits []files.Hit) {
		out.Hits = append(out.Hits, hits...)
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	out.Truncated = truncated || l.Truncated
	return fileSearchReply(ctx, out)
}

func fileSearchOverlay(ctx context.Context, input map[string]string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(input) > fileSearchOverlayLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file search overlay exceeds %d entries", fileSearchOverlayLimit)
	}
	overlay := make(map[string]string, len(input))
	total := 0
	for path, text := range input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel, err := fileSearchRelative(path)
		if err != nil {
			return nil, err
		}
		if rel == "." {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "file search overlay path must name a file")
		}
		if _, exists := overlay[rel]; exists {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "file search overlay contains duplicate normalized paths")
		}
		total += len(text)
		if len(text) > files.ReadOnlySize || total > fileSearchOverlayBytes {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "file search overlay exceeds the content limit")
		}
		overlay[rel] = text
	}
	return overlay, nil
}

func fileSearchReply(ctx context.Context, value any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 加上实际 RPC 外壳和换行；RawMessage 让发送侧复用已核实大小的内容。
	if len(data)+len("{\"result\":}\n") > fileSearchReplyBytes {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file list or search exceeds the response limit; narrow the root or search scope")
	}
	return json.RawMessage(data), nil
}
