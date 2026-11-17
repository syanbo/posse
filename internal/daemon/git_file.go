package daemon

import (
	"context"
	"encoding/json"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func gitFileIdentity(ctx context.Context, path string, expected rpc.GitRepositoryIdentity) (rpc.GitRepositoryIdentity, error) {
	if err := filePath(path); err != nil {
		return rpc.GitRepositoryIdentity{}, err
	}
	if expected.Root == "" || expected.RootInode == 0 || expected.GitInode == 0 || expected.CommonInode == 0 {
		return rpc.GitRepositoryIdentity{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git repository identity")
	}
	id, err := gitx.RepositoryIdentityChecked(ctx, path)
	if err != nil {
		return id, gitReadError(ctx, err)
	}
	if id != expected {
		return id, gitReadError(ctx, gitx.ErrRepositoryChanged)
	}
	return id, nil
}
func (d *Daemon) gitFile(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitFileParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidFileQuery(p.Source, p.Revision, p.File) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git file query")
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	v, err := gitx.ResolveFile(ctx, id.Root, p.Source, p.Revision, p.File)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	return gitReadResult(ctx, rpc.GitFileResult{Identity: id, File: v})
}
func (d *Daemon) gitBlobRange(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitBlobRangeParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidBlobOID(p.OID) || p.Offset < 0 || p.Limit < 1 || p.Limit > files.RangeLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git file query")
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	size, err := gitx.BlobSize(ctx, id.Root, p.OID)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	data, err := gitx.ReadBlobRange(ctx, id.Root, p.OID, size, p.Offset, p.Limit)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	return gitReadResult(ctx, rpc.GitBlobRangeResult{Identity: id, OID: p.OID, Size: size, Offset: p.Offset, Data: data, EOF: p.Offset+int64(len(data)) == size})
}
