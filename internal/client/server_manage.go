package client

import (
	"context"

	"posse/internal/rpc"
)

func (c *Client) RemoteInfo(ctx context.Context) (rpc.RemoteInfo, error) {
	var out rpc.RemoteInfo
	err := c.Call(ctx, "daemon.info", nil, &out)
	return out, err
}

func (c *Client) DaemonMemory(ctx context.Context, expanded []string) (rpc.DaemonMemoryResult, error) {
	var out rpc.DaemonMemoryResult
	err := c.Call(ctx, "daemon.memory", rpc.DaemonMemoryParams{Expanded: expanded}, &out)
	return out, err
}

func (c *Client) AutostartStatus(ctx context.Context) (rpc.AutostartStatus, error) {
	var out rpc.AutostartStatus
	err := c.Call(ctx, "server.autostart.status", nil, &out)
	return out, err
}

func (c *Client) PrepareSetAutostart(enabled bool) (*DesktopOperation, error) {
	v := enabled
	return c.PrepareDesktopOperation("server.autostart.set", rpc.AutostartSetParams{Enabled: &v})
}
