package client

import (
	"context"

	"posse/internal/rpc"
)

func (c *Client) SearchSymbols(ctx context.Context, p rpc.FileSymbolsParams) (rpc.FileSymbolsResult, error) {
	var out rpc.FileSymbolsResult
	err := c.Call(ctx, "files.symbols", p, &out)
	return out, err
}

func (c *Client) FindDefinition(ctx context.Context, p rpc.FileDefinitionParams) (rpc.FileDefinitionResult, error) {
	var out rpc.FileDefinitionResult
	err := c.Call(ctx, "files.definition", p, &out)
	return out, err
}
