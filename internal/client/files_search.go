package client

import (
	"context"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (c *Client) ListFiles(ctx context.Context, p rpc.FileListParams) (*files.List, error) {
	var out *files.List
	if err := c.Call(ctx, "files.list", p, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SearchFiles(ctx context.Context, p rpc.FileSearchParams) (rpc.FileSearchResult, error) {
	var out rpc.FileSearchResult
	err := c.Call(ctx, "files.search", p, &out)
	return out, err
}
