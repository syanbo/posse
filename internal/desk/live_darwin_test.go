//go:build darwin

package desk

import (
	"testing"

	"github.com/ebitengine/purego/objc"
)

// 换屏、缩放之后旧流的样本队列在后台排空，期间旧流交来的样本和迟到的停止回调都要认出来丢掉：
// 旧尺寸的帧不能进新编码器，旧流停了也不能把现在的流当成停了。这里不起采集，engine 只有 live，
// 走到要用 mac 的地方就会崩，所以没崩就是在入口丢掉了。
func TestOldStreamCallbacksDropped(t *testing.T) {
	e := &engine{}
	e.live.Store(0x5000)
	e.onSample(0x7000, 0x1234)

	prev := active
	active = e
	defer func() { active = prev }()
	streamDidStop(0, 0, objc.ID(0x7000), 0)
}
