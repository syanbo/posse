package mobile

import (
	"context"
	"encoding/json"
	"posse/internal/errtext"
	"time"
)

// 可选接口让旧 Host 保持原有能力；device 来自握手，不能由请求冒充。
type discussionHost interface {
	Discussions(context.Context, string, string, json.RawMessage) (any, error)
}

func (c *conn) discussion(id, method string, params json.RawMessage) error {
	h, ok := c.srv.host.(discussionHost)
	if !ok || !c.hasCapability("posse.discussions.v1") {
		return errorf("method_not_found", "Unknown method: %s", method)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Minute)
	defer cancel()
	res, err := h.Discussions(ctx, c.device.ID, method, params)
	translateDiscussion(res, c.language())
	c.replyBulk(id, res, err)
	return nil
}

// 原因同时保留原值和显示值，手机仍按原值判断可用的恢复操作。
func translateDiscussion(v any, lang errtext.Lang) {
	switch x := v.(type) {
	case []any:
		for _, child := range x {
			translateDiscussion(child, lang)
		}
	case map[string]any:
		for _, key := range []string{"reason", "paused", "error", "listenError"} {
			if msg, ok := x[key].(string); ok && msg != "" {
				x[key+"Text"] = errtext.MessageIn(lang, msg)
			}
		}
		for _, child := range x {
			translateDiscussion(child, lang)
		}
	}
}
