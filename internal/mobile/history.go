package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"posse/internal/rpc"
)

// 手机端历史（Posse 扩展，声明了 posse.history.v1 的连接才有）：列表、搜索和"在电脑上继续"，
// 转给常驻进程的 history.* 接口；看会话内容走 nativeChat.readSession（按会话文件读）。
func (c *conn) history(id, method string, p json.RawMessage) error {
	if !c.hasCapability("posse.history.v1") {
		return errorf("method_not_found", "Unknown method: %s", method)
	}
	if len(p) == 0 {
		p = json.RawMessage("{}")
	}
	// 索引过期时查询会先等最多 3 秒的增量更新
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	res, err := c.srv.host.History(ctx, method, p)
	if err != nil {
		return historyError(method, err)
	}
	return c.reply(id, res)
}

// historyError 把常驻进程的报错换成手机上看得懂的话（英文源文本，c.fail 按连接的语言翻译），错误码照旧。
func historyError(method string, err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return errorf("history", "%v", err)
	}
	msg := re.Message
	if method == "history.resume" {
		switch {
		case re.Code == rpc.CodeNotFound && strings.Contains(msg, "no workspace contains"):
			msg = "no workspace on the computer contains the session’s folder; add the folder as a project on the computer first"
		case re.Code == rpc.CodeNotFound:
			msg = "the computer can no longer find this session"
		case re.Code == rpc.CodeConflict && strings.Contains(msg, "no longer exists"):
			msg = "the session’s original folder no longer exists on the computer"
		}
	}
	return errorf(re.Code, "%s", msg)
}
