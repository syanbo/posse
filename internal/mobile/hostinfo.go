package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"time"

	"posse/internal/errtext"
	"posse/internal/rpc"
)

const (
	hostInfoTimeout       = 8 * time.Second
	hostMemoryTimeout     = 5 * time.Second
	hostAutostartTimeout  = 38 * time.Second
	hostInfoParamLimit    = 4 << 10
	hostMemoryParamLimit  = 16 << 10
	hostAutostartArgLimit = 4 << 10
	hostGlobalConcurrency = 4
	hostConnConcurrency   = 2
)

// HostInfo 是手机端「主机详情」页读取的实时节点概览与开机自启状态。
type HostInfo struct {
	Version          string               `json:"version"`
	Commit           string               `json:"commit,omitempty"`
	Build            string               `json:"build,omitempty"`
	Platform         string               `json:"platform"`
	Arch             string               `json:"arch"`
	Edition          string               `json:"edition"`
	Managed          bool                 `json:"managed"`
	Pid              int                  `json:"pid"`
	StartedAt        int64                `json:"startedAt,omitempty"`
	Home             string               `json:"home"`
	Terminals        int                  `json:"terminals"`
	RunningTerminals int                  `json:"runningTerminals"`
	DaemonMemoryKB   int64                `json:"daemonMemoryKB"`
	Autostart        *rpc.AutostartStatus `json:"autostart,omitempty"`
	AutostartError   string               `json:"autostartError,omitempty"`
}

// HostInfoProvider 是可选接口，供常驻进程向声明 posse.host.v1 的手机客户端暴露节点详情、内存明细与托管自启开关。
type HostInfoProvider interface {
	HostInfo(ctx context.Context) (HostInfo, error)
	HostMemory(ctx context.Context, expanded []string) (rpc.DaemonMemoryResult, error)
	SetHostAutostart(ctx context.Context, enabled bool) (rpc.AutostartStatus, error)
}

func (s *Server) tryAcquireHostSlot() bool {
	for {
		cur := s.hostInFlight.Load()
		if cur >= hostGlobalConcurrency {
			return false
		}
		if s.hostInFlight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (s *Server) releaseHostSlot() {
	s.hostInFlight.Add(-1)
}

func tryIncBounded(counter *atomic.Int32, limit int32) bool {
	for {
		cur := counter.Load()
		if cur >= limit {
			return false
		}
		if counter.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (c *conn) acquireHostSlots(write bool) (func(), error) {
	if write {
		if !tryIncBounded(&c.hostWriteInFlight, 1) {
			return nil, errorf("busy", "another startup service update is in progress")
		}
	}
	if !tryIncBounded(&c.hostInFlight, hostConnConcurrency) {
		if write {
			c.hostWriteInFlight.Add(-1)
		}
		return nil, errorf("busy", "too many host detail requests in progress")
	}
	if !c.srv.tryAcquireHostSlot() {
		c.hostInFlight.Add(-1)
		if write {
			c.hostWriteInFlight.Add(-1)
		}
		return nil, errorf("busy", "too many host detail requests in progress")
	}
	return func() {
		c.srv.releaseHostSlot()
		c.hostInFlight.Add(-1)
		if write {
			c.hostWriteInFlight.Add(-1)
		}
	}, nil
}

// parseStrictObject 严格校验 JSON 对象：拒绝非对象、尾随数据、未知键、重复键及大小写别名。
func parseStrictObject(raw json.RawMessage, allowed map[string]bool) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]json.RawMessage{}, true
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := ktok.(string)
		if !ok || !allowed[key] {
			return nil, false
		}
		if _, dup := fields[key]; dup {
			return nil, false
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, false
		}
		fields[key] = val
	}
	endTok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if endDelim, ok := endTok.(json.Delim); !ok || endDelim != '}' {
		return nil, false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func (c *conn) dispatchHostInfo(id string, raw json.RawMessage) error {
	if !c.hasCapability("posse.host.v1") {
		return errorf("method_not_found", "Unknown method: %s", "posse.host.info")
	}
	p, ok := c.srv.host.(HostInfoProvider)
	if !ok {
		return errorf("method_not_found", "Unknown method: %s", "posse.host.info")
	}
	if len(raw) > hostInfoParamLimit {
		return errorf("bad_params", "invalid host info parameters")
	}
	if _, ok := parseStrictObject(raw, map[string]bool{}); !ok {
		return errorf("bad_params", "invalid host info parameters")
	}
	release, err := c.acquireHostSlots(false)
	if err != nil {
		return err
	}
	return c.async(id, func() error {
		defer release()
		ctx, cancel := context.WithTimeout(c.ctx, hostInfoTimeout)
		defer cancel()
		info, err := p.HostInfo(ctx)
		if err != nil {
			c.replyOrFail(id, nil, err)
			return nil
		}
		if info.AutostartError != "" {
			info.AutostartError = errtext.MessageIn(c.language(), info.AutostartError)
		}
		return c.reply(id, info)
	})
}

func (c *conn) dispatchHostMemory(id string, raw json.RawMessage) error {
	if !c.hasCapability("posse.host.v1") {
		return errorf("method_not_found", "Unknown method: %s", "posse.host.memory")
	}
	p, ok := c.srv.host.(HostInfoProvider)
	if !ok {
		return errorf("method_not_found", "Unknown method: %s", "posse.host.memory")
	}
	if len(raw) > hostMemoryParamLimit {
		return errorf("bad_params", "daemon.memory parameters exceed size limit")
	}
	fields, ok := parseStrictObject(raw, map[string]bool{"expanded": true})
	if !ok {
		return errorf("bad_params", "invalid daemon.memory parameters")
	}
	var expanded []string
	if v, has := fields["expanded"]; has {
		if err := json.Unmarshal(v, &expanded); err != nil {
			return errorf("bad_params", "invalid daemon.memory parameters")
		}
	}
	release, err := c.acquireHostSlots(false)
	if err != nil {
		return err
	}
	return c.async(id, func() error {
		defer release()
		ctx, cancel := context.WithTimeout(c.ctx, hostMemoryTimeout)
		defer cancel()
		res, err := p.HostMemory(ctx, expanded)
		c.replyOrFail(id, res, err)
		return nil
	})
}

func (c *conn) dispatchHostAutostartSet(id string, raw json.RawMessage) error {
	if !c.hasCapability("posse.host.v1") {
		return errorf("method_not_found", "Unknown method: %s", "posse.host.autostartSet")
	}
	p, ok := c.srv.host.(HostInfoProvider)
	if !ok {
		return errorf("method_not_found", "Unknown method: %s", "posse.host.autostartSet")
	}
	if len(raw) > hostAutostartArgLimit {
		return errorf("bad_params", "invalid server.autostart.set parameters")
	}
	fields, ok := parseStrictObject(raw, map[string]bool{"enabled": true})
	if !ok {
		return errorf("bad_params", "invalid server.autostart.set parameters")
	}
	rawEnabled, has := fields["enabled"]
	if !has {
		return errorf("bad_params", "invalid server.autostart.set parameters")
	}
	var enabled *bool
	if err := json.Unmarshal(rawEnabled, &enabled); err != nil || enabled == nil {
		return errorf("bad_params", "invalid server.autostart.set parameters")
	}
	release, err := c.acquireHostSlots(true)
	if err != nil {
		return err
	}
	return c.async(id, func() error {
		defer release()
		ctx, cancel := context.WithTimeout(c.ctx, hostAutostartTimeout)
		defer cancel()
		st, err := p.SetHostAutostart(ctx, *enabled)
		c.replyOrFail(id, st, err)
		return nil
	})
}
