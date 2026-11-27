package client

import (
	"context"
	"time"

	"posse/internal/rpc"
	"posse/internal/tslink"
)

type MobilePairResult struct {
	URL       string   `json:"url"`
	Code      string   `json:"code"`
	Endpoint  string   `json:"endpoint"`
	Endpoints []string `json:"endpoints,omitempty"`
	Locals    []string `json:"locals,omitempty"`
}

type MobileDevice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Phone    string    `json:"phone,omitempty"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
	Links    int       `json:"links,omitempty"`
	Online   bool      `json:"online,omitempty"`
}

type RelayStatusResult struct {
	Configured bool   `json:"configured"`
	Admin      bool   `json:"admin"`
	Name       string `json:"name"`
	State      string `json:"state"`
}

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

func (c *Client) MobilePair(ctx context.Context, rotate bool, addr string) (MobilePairResult, error) {
	var out MobilePairResult
	err := c.Call(ctx, "mobile.pair", map[string]any{"rotate": rotate, "address": addr}, &out)
	return out, err
}

func (c *Client) MobileDevices(ctx context.Context) ([]MobileDevice, error) {
	var out []MobileDevice
	err := c.Call(ctx, "mobile.devices", nil, &out)
	return out, err
}

func (c *Client) MobileRemoveDevice(ctx context.Context, deviceID string) error {
	return c.Call(ctx, "mobile.rm", map[string]any{"device": deviceID}, nil)
}

func (c *Client) PrepareMobileRemoveDevice(deviceID string) (*DesktopOperation, error) {
	return c.PrepareDesktopOperation("mobile.rm", map[string]any{"device": deviceID})
}

func (c *Client) RelayStatus(ctx context.Context) (RelayStatusResult, error) {
	var out RelayStatusResult
	err := c.Call(ctx, "relay.status", nil, &out)
	return out, err
}

func (c *Client) TailscaleStatus(ctx context.Context) (tslink.Status, error) {
	var out tslink.Status
	err := c.Call(ctx, "tailscale.status", nil, &out)
	return out, err
}
