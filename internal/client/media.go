package client

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"posse/internal/media"
	"posse/internal/rpc"
)

func (c *Client) MediaList(ctx context.Context, source rpc.MediaSource) (rpc.MediaListResult, error) {
	var out rpc.MediaListResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for session media")
	}
	if err := c.Call(ctx, "media.list", source, &out); err != nil {
		return rpc.MediaListResult{}, err
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > rpc.MediaListLimit || len(out.Made) > 40 || out.MadeAll < len(out.Made) {
		return rpc.MediaListResult{}, errors.New("invalid session media list response")
	}
	return out, nil
}

func (c *Client) MediaPath(ctx context.Context, item media.Item) (string, error) {
	return c.mediaPath(ctx, rpc.MediaPathParams{Item: item})
}

func (c *Client) MediaOriginal(ctx context.Context, item media.Item) (string, error) {
	return c.mediaPath(ctx, rpc.MediaPathParams{Item: item, Original: true})
}

func (c *Client) mediaPath(ctx context.Context, params rpc.MediaPathParams) (string, error) {
	if c.Desktop == nil {
		return "", rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for session media")
	}
	var out rpc.MediaPathResult
	if err := c.Call(ctx, "media.path", params, &out); err != nil {
		return "", err
	}
	if !filepath.IsAbs(out.Path) || strings.ContainsRune(out.Path, 0) {
		return "", errors.New("invalid session media path response")
	}
	return out.Path, nil
}
