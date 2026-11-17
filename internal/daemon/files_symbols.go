package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (d *Daemon) filesSymbols(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.FileSymbolsParams
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
	symbols, truncated, err := files.FindSymbols(ctx, l, overlay, p.Query)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	return fileSearchReply(ctx, rpc.FileSymbolsResult{Symbols: symbols, Truncated: truncated || l.Truncated})
}

func (d *Daemon) filesDefinition(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.FileDefinitionParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Root); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	if len(p.Text) > files.MaxSize {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "definition source exceeds %d bytes", files.MaxSize)
	}
	if p.Offset < 0 || p.Offset > len(p.Text) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "definition offset must be within the source text")
	}
	overlay, err := fileSearchOverlay(ctx, p.Overlay)
	if err != nil {
		return nil, err
	}
	p.Root, p.Path = filepath.Clean(p.Root), filepath.Clean(p.Path)
	start := strings.LastIndexByte(p.Text[:p.Offset], '\n') + 1
	end := len(p.Text)
	if n := strings.IndexByte(p.Text[start:], '\n'); n >= 0 {
		end = start + n
	}
	line := p.Text[start:end]
	lk := files.LinkAt(line, p.Offset-start)
	var out rpc.FileDefinitionResult
	switch lk.Kind {
	case files.LinkPath:
		if path, line, ok := files.ResolveImport(p.Root, p.Path, lk.Text); ok {
			out.Path, out.Line = path, max(1, line)
		}
		return fileSearchReply(ctx, out)
	case files.LinkWord:
		if !files.IsCode(p.Path) {
			return fileSearchReply(ctx, out)
		}
	default:
		// URL 由窗口决定是否打开；目标服务不产生系统界面动作。
		return fileSearchReply(ctx, out)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ln := strings.Count(p.Text[:start], "\n")
	qual := lk.Qual
	if qual != "" {
		if typ := files.TypeOf(p.Path, p.Text, qual, ln); typ != "" {
			qual = typ
		}
	}
	from := ""
	if rel, err := filepath.Rel(p.Root, p.Path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		from = rel
	}
	q := files.DefinitionQuery{Word: lk.Text, Qual: qual, From: from, Prefer: files.PreferFor(p.Root, p.Path, line)}
	l, err := targetFileList(ctx, rpc.FileListParams{Root: p.Root, SingleRepo: p.SingleRepo})
	if err != nil {
		return nil, err
	}
	// 当前快照独立于 overlay，未保存的 import 也参与优先级推断。
	named, all := files.Imports(l, p.Root, p.Path, p.Text)
	q.Imported = all
	if path := named[q.Word]; path != "" {
		q.Prefer = path
	} else if path := named[qual]; qual != "" && path != "" {
		q.Prefer = path
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out.Definitions, err = files.FindDefinitions(ctx, l, overlay, q)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	return fileSearchReply(ctx, out)
}
